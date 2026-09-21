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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cli-utils/pkg/kstatus/status"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	esv1 "github.com/external-secrets/external-secrets/apis/externalsecrets/v1"

	atlasv1 "github.com/ariga/atlas-operator/api/v1alpha1"
	rikamiv1 "github.com/b-zago/rikami-operator/api/v1"
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

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Vessel object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/reconcile
func (r *VesselReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

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

	// SSA apply logic
	var (
		applyErrs   []error
		failedNames []string
	)

	for _, server := range vessel.Spec.Servers {
		srv := NewServer(vessel, profile, server)
		if err := r.applyServer(ctx, srv); err != nil {
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
	// end SSA
	// encapsulate into a func later too

	if err := r.prune(ctx, vessel); err != nil {
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

	vLabel := map[string]string{vesselLabel: vessel.Name}

	// check status of all children here to set the current status to work on
	// by determing what kinds to look for first
	gvks := vessel.GetOwnedGVKList()
	resourceCount := 0
	okCount := 0
	progressing := false

	for _, gvk := range gvks {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)
		err := r.List(ctx, list, client.InNamespace(vessel.Namespace), client.MatchingLabels(vLabel))
		if err != nil {
			log.Error(err, "could not list owned resources")
			return ctrl.Result{}, err
		}

		resourceCount += len(list.Items)

		for _, item := range list.Items {
			// some kind of guard here to check if we should use custom status check or go with kstatus behaviour
			// for now lets just use kstatus
			result, err := status.Compute(&item)
			if err != nil {
				log.Error(err, "could not compute resource status")
				return ctrl.Result{}, reconcile.TerminalError(err)
			}
			// below in each case also collect the info about the resources so that we can communicate clearly what is at what state currently (or at least as much as we can communicate it)
			// for this we can check for every kstatus Status value for every resource and act accordingly
			switch result.Status.String() {
			case "Current":
				okCount++
			case "InProgress":
				progressing = true
			}
		}
	}

	// this needs more work for different cases/scenarios
	// like for example when some resources are progressing but some are failed we can set progressing and degraded conditions to true (instead of blindly doing unknown maybe)
	// something to think about
	original = vessel.DeepCopy()
	requeueReconcile := false
	if okCount != resourceCount && progressing {
		vessel.SetStatusFullyProgressing(ctx, &rikamiv1.VesselNewStatusInfo{Reason: rikamiv1.ReasonResourcesInProgress, Message: "some resources are in progress"})
		requeueReconcile = true
	} else if okCount != resourceCount {
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
	// end checking children status
	// encapsulate into a func later

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
		Complete(r)
}

func (r *VesselReconciler) updateStatus(ctx context.Context, v *rikamiv1.Vessel, original *rikamiv1.Vessel) error {
	patch := client.MergeFrom(original)
	if err := r.Status().Patch(ctx, v, patch); err != nil {
		logf.FromContext(ctx).Error(err, "failed to update vessel status")
		return err
	}
	return nil
}

func (r *VesselReconciler) applyServer(ctx context.Context, server *VesselServerResource) error {
	for _, secret := range server.ExternalSecrets {

		err := r.Apply(ctx, client.ApplyConfigurationFromUnstructured(secret), client.FieldOwner(FieldOwnerName), client.ForceOwnership)
		if err != nil {
			return err
		}
	}

	for _, db := range server.Databases {

		err := r.Apply(ctx, client.ApplyConfigurationFromUnstructured(db.ExternalSecret), client.FieldOwner(FieldOwnerName), client.ForceOwnership)
		if err != nil {
			return err
		}

		err = r.Apply(ctx, client.ApplyConfigurationFromUnstructured(db.AtlasSchema), client.FieldOwner(FieldOwnerName), client.ForceOwnership)
		if err != nil {
			return err
		}
	}

	err := r.Apply(ctx, server.Deployment, client.FieldOwner(FieldOwnerName), client.ForceOwnership)
	if err != nil {
		return err
	}
	err = r.Apply(ctx, server.Service, client.FieldOwner(FieldOwnerName), client.ForceOwnership)
	if err != nil {
		return err
	}
	err = r.Apply(ctx, server.HTTPRoute, client.FieldOwner(FieldOwnerName), client.ForceOwnership)
	if err != nil {
		return err
	}
	return nil
}

func (r *VesselReconciler) prune(ctx context.Context, v *rikamiv1.Vessel) error {
	vesselReq, err := labels.NewRequirement(vesselLabel, selection.Equals, []string{v.Name})
	if err != nil {
		return err
	}
	sel := labels.NewSelector().Add(*vesselReq)

	if len(v.Spec.Servers) > 0 {
		specServers := make([]string, len(v.Spec.Servers))
		for idx, server := range v.Spec.Servers {
			specServers[idx] = server.Name
		}
		serverReq, err := labels.NewRequirement(serverLabel, selection.NotIn, specServers)
		if err != nil {
			return err
		}
		sel = sel.Add(*serverReq)
	}

	var errs []error
	for _, gvk := range v.GetOwnedGVKList() {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)

		if err := r.List(
			ctx, list,
			client.InNamespace(v.Namespace),
			client.MatchingLabelsSelector{Selector: sel},
		); err != nil {
			errs = append(errs, fmt.Errorf("listing %s: %w", gvk.Kind, err))
			continue
		}

		for i := range list.Items {
			obj := &list.Items[i]
			err := r.Delete(ctx, obj, client.Preconditions{UID: ptr.To(obj.GetUID())})
			if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
				errs = append(errs, fmt.Errorf("deleting %s %s: %w", gvk.Kind, obj.GetName(), err))
			}
		}
	}
	return utilerrors.NewAggregate(errs)
}
