/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	esv1 "github.com/external-secrets/external-secrets/apis/externalsecrets/v1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cli-utils/pkg/kstatus/status"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	atlasv1 "github.com/ariga/atlas-operator/api/v1alpha1"
	rikamiv1 "github.com/b-zago/rikami-operator/api/v1alpha1"
)

const (
	FieldOwnerName     = "vessel-controller"
	vesselProfileField = "spec.profile"
	vesselLabel        = "rikami.zagoapps.com/vessel"
	serverLabel        = "rikami.zagoapps.com/server"
)

// VesselReconciler reconciles a Vessel object
type VesselReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=rikami.zagoapps.com,resources=vessels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rikami.zagoapps.com,resources=vessels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rikami.zagoapps.com,resources=vessels/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rikami.zagoapps.com,resources=profiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=external-secrets.io,resources=externalsecrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=db.atlasgo.io,resources=atlasschemas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete

// Reconcile builds every child resource for the Vessel from its Profile,
// server-side applies them, prunes anything no longer in the spec, and then
// sets the Vessel's conditions from the health of its children.
func (r *VesselReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	applied := appliedSet{}

	vessel := &rikamiv1.Vessel{}
	err := r.Get(ctx, req.NamespacedName, vessel)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Vessel not found. Ignoring.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get vessel")
		return ctrl.Result{}, err
	}

	original := vessel.DeepCopy()

	if len(vessel.Status.Conditions) == 0 {
		vessel.SetStatusUnknown(ctx, &rikamiv1.VesselNewStatusInfo{Reason: rikamiv1.ReasonReconcileInProgress, Message: "reconciliation in progress"})
		statusErr := r.updateStatus(ctx, vessel, original)
		if statusErr != nil {
			return ctrl.Result{}, statusErr
		}

	}

	profileName := vessel.Spec.Profile

	profile := &rikamiv1.Profile{}
	err = r.Get(ctx, client.ObjectKey{Namespace: vessel.Namespace, Name: profileName}, profile)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Error(err, "profile not found")
			original = vessel.DeepCopy()
			vessel.SetStatusFullyDegraded(ctx, &rikamiv1.VesselNewStatusInfo{Reason: rikamiv1.ReasonProfileMissing, Message: "profile not found"})
			statusErr := r.updateStatus(ctx, vessel, original)
			if statusErr != nil {
				return ctrl.Result{}, statusErr
			}
			return ctrl.Result{}, reconcile.TerminalError(err)
		}
		log.Error(err, "failed to get profile")
		return ctrl.Result{}, err
	}

	// Apply
	var (
		applyErrs   []error
		failedNames []string
	)

	for _, server := range vessel.Spec.Servers {
		srv := NewServer(vessel, profile, server)
		if err := r.applyServer(ctx, srv, applied); err != nil {
			log.Error(err, "failed to apply server", "server", server.Name)
			applyErrs = append(applyErrs, fmt.Errorf("server %s: %w", server.Name, err))
			failedNames = append(failedNames, server.Name)
		}
	}

	if err := errors.Join(applyErrs...); err != nil {
		original = vessel.DeepCopy()
		vessel.SetStatusFullyDegraded(ctx, &rikamiv1.VesselNewStatusInfo{
			Reason: rikamiv1.ReasonApplyFailed,
			Message: fmt.Sprintf("failed to apply %d of %d servers: %s",
				len(failedNames), len(vessel.Spec.Servers), strings.Join(failedNames, ", ")),
		})
		if statusErr := r.updateStatus(ctx, vessel, original); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err
	}

	// Prune
	if err := r.prune(ctx, vessel, applied); err != nil {
		log.Error(err, "failed to prune orphaned resources")
		original = vessel.DeepCopy()
		vessel.SetStatusFullyDegraded(ctx, &rikamiv1.VesselNewStatusInfo{
			Reason:  rikamiv1.ReasonApplyFailed, // or a ReasonPruneFailed
			Message: "failed to remove orphaned resources",
		})
		if statusErr := r.updateStatus(ctx, vessel, original); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err
	}

	// Health
	health, err := r.childrenHealth(ctx, vessel)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Status
	// TODO: when some children are progressing and others failed, set both
	// Progressing and Degraded instead of picking one.
	original = vessel.DeepCopy()
	requeueReconcile := false
	if health.current != health.total && health.progressing {
		vessel.SetStatusFullyProgressing(ctx, &rikamiv1.VesselNewStatusInfo{Reason: rikamiv1.ReasonResourcesInProgress, Message: "some resources are in progress"})
		requeueReconcile = true
	} else if health.current != health.total {
		vessel.SetStatusFullyDegraded(ctx, &rikamiv1.VesselNewStatusInfo{Reason: rikamiv1.ReasonResourcesUnhealthy, Message: "some resources seem unhealthy"})
		requeueReconcile = true
	}

	statusErr := r.updateStatus(ctx, vessel, original)
	if statusErr != nil {
		return ctrl.Result{}, statusErr
	}

	if requeueReconcile {
		return ctrl.Result{RequeueAfter: time.Second * 10}, nil
	}

	original = vessel.DeepCopy()
	vessel.SetStatusFullyAvailable(ctx, &rikamiv1.VesselNewStatusInfo{Reason: rikamiv1.ReasonSucceeded, Message: "reconcile succeeded and everything looks healthy"})
	statusErr = r.updateStatus(ctx, vessel, original)
	if statusErr != nil {
		return ctrl.Result{}, statusErr
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VesselReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&rikamiv1.Vessel{},
		vesselProfileField,
		func(o client.Object) []string {
			vessel, ok := o.(*rikamiv1.Vessel)
			if !ok || vessel.Spec.Profile == "" {
				return nil
			}
			return []string{vessel.Spec.Profile}
		},
	); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&rikamiv1.Vessel{}).
		Named("vessel").
		Watches(&rikamiv1.Profile{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			log := logf.FromContext(ctx)

			vesselList := &rikamiv1.VesselList{}

			err := r.List(ctx, vesselList, client.InNamespace(obj.GetNamespace()), client.MatchingFields{vesselProfileField: obj.GetName()})
			if err != nil {
				log.Error(err, "could not list vessels")
				return nil
			}

			reqs := make([]reconcile.Request, len(vesselList.Items))

			for idx, item := range vesselList.Items {
				reqs[idx] = reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      item.Name,
						Namespace: item.Namespace,
					},
				}
			}

			return reqs
		})).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&gwv1.HTTPRoute{}).
		Owns(&esv1.ExternalSecret{}).
		Owns(&atlasv1.AtlasSchema{}).
		Owns(&batchv1.Job{}).
		Owns(&monitoringv1.ServiceMonitor{}).
		Owns(&autoscalingv2.HorizontalPodAutoscaler{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

// childHealth summarises the kstatus of every child resource.
type childHealth struct {
	total       int
	current     int
	progressing bool
}

// childrenHealth lists every resource kind the Vessel owns and computes each
// one's status with kstatus.
func (r *VesselReconciler) childrenHealth(ctx context.Context, v *rikamiv1.Vessel) (childHealth, error) {
	var h childHealth
	for _, gvk := range v.GetOwnedGVKList() {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)
		if err := r.List(ctx, list, client.InNamespace(v.Namespace), client.MatchingLabels{vesselLabel: v.Name}); err != nil {
			return h, fmt.Errorf("listing %s: %w", gvk.Kind, err)
		}

		h.total += len(list.Items)
		for i := range list.Items {
			result, err := status.Compute(&list.Items[i])
			if err != nil {
				return h, reconcile.TerminalError(fmt.Errorf("computing status of %s %s: %w", gvk.Kind, list.Items[i].GetName(), err))
			}
			switch result.Status {
			case status.CurrentStatus:
				h.current++
			case status.InProgressStatus:
				h.progressing = true
			}
		}
	}
	return h, nil
}

func (r *VesselReconciler) updateStatus(ctx context.Context, v *rikamiv1.Vessel, original *rikamiv1.Vessel) error {
	patch := client.MergeFrom(original)
	if err := r.Status().Patch(ctx, v, patch); err != nil {
		logf.FromContext(ctx).Error(err, "failed to update vessel status")
		return err
	}
	return nil
}

// applyServer applies every resource built for a server, then recurses into
// its in-cluster services. Ordering is best effort for now: dependencies
// (secrets, schemas) go first, and anything not ready yet settles over the
// following reconciles.
func (r *VesselReconciler) applyServer(ctx context.Context, server *VesselServerResource, applied appliedSet) error {
	for _, secret := range server.ExternalSecrets {
		if err := r.applyUnstructured(ctx, secret, applied); err != nil {
			return err
		}
	}

	for _, db := range server.Databases {
		for _, obj := range []*unstructured.Unstructured{db.MigratorSecret, db.AppSecret, db.AtlasSchema} {
			if err := r.applyUnstructured(ctx, obj, applied); err != nil {
				return err
			}
		}
		if job := db.SeedJob; job != nil {
			if err := r.applyTyped(ctx, job, job.APIVersion, job.Kind, job.Name, applied); err != nil {
				return err
			}
		}
	}

	for _, service := range server.Services {
		if err := r.applyServer(ctx, NewService(server.Vessel, server.Profile, &service), applied); err != nil {
			return err
		}
	}

	d := server.Deployment
	if err := r.applyTyped(ctx, d, d.APIVersion, d.Kind, d.Name, applied); err != nil {
		return err
	}

	if hpa := server.HPA; hpa != nil {
		if err := r.applyTyped(ctx, hpa, hpa.APIVersion, hpa.Kind, hpa.Name, applied); err != nil {
			return err
		}
	}

	svc := server.Service
	if err := r.applyTyped(ctx, svc, svc.APIVersion, svc.Kind, svc.Name, applied); err != nil {
		return err
	}

	if route := server.HTTPRoute; route != nil {
		if err := r.applyTyped(ctx, route, route.APIVersion, route.Kind, route.Name, applied); err != nil {
			return err
		}
	}

	if server.ServiceMonitor != nil {
		if err := r.applyUnstructured(ctx, server.ServiceMonitor, applied); err != nil {
			return err
		}
	}

	return nil
}

// applyTyped server-side applies a typed apply configuration and records it
// in the applied set so it survives pruning.
func (r *VesselReconciler) applyTyped(ctx context.Context, obj runtime.ApplyConfiguration, apiVersion, kind, name *string, applied appliedSet) error {
	if err := r.Apply(ctx, obj, client.FieldOwner(FieldOwnerName), client.ForceOwnership); err != nil {
		return fmt.Errorf("applying %s %s: %w", *kind, *name, err)
	}
	applied.add(*apiVersion, *kind, *name)
	return nil
}

// applyUnstructured does the same for third-party CRDs built as unstructured.
func (r *VesselReconciler) applyUnstructured(ctx context.Context, obj *unstructured.Unstructured, applied appliedSet) error {
	if err := r.Apply(ctx, client.ApplyConfigurationFromUnstructured(obj), client.FieldOwner(FieldOwnerName), client.ForceOwnership); err != nil {
		return fmt.Errorf("applying %s %s: %w", obj.GetKind(), obj.GetName(), err)
	}
	applied.add(obj.GetAPIVersion(), obj.GetKind(), obj.GetName())
	return nil
}

func (r *VesselReconciler) prune(ctx context.Context, v *rikamiv1.Vessel, applied appliedSet) error {
	log := logf.FromContext(ctx)

	if len(applied) == 0 && len(v.Spec.Servers) > 0 {
		return fmt.Errorf("refusing to prune: applied set empty with %d servers in spec", len(v.Spec.Servers))
	}

	var errs []error
	for _, gvk := range v.GetOwnedGVKList() {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)

		if err := r.List(
			ctx, list,
			client.InNamespace(v.Namespace),
			client.MatchingLabels{vesselLabel: v.Name},
		); err != nil {
			errs = append(errs, fmt.Errorf("listing %s: %w", gvk.Kind, err))
			continue
		}

		for i := range list.Items {
			obj := &list.Items[i]

			if applied.has(obj.GroupVersionKind().GroupKind(), obj.GetName()) {
				continue // we just applied this, keep it
			}

			log.Info("pruning orphaned resource",
				"kind", obj.GetKind(), "name", obj.GetName())

			err := r.Delete(ctx, obj, client.Preconditions{UID: ptr.To(obj.GetUID())})
			if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
				errs = append(errs, fmt.Errorf("deleting %s %s: %w", obj.GetKind(), obj.GetName(), err))
			}
		}
	}
	return utilerrors.NewAggregate(errs)
}
