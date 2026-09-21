package controller

import (
	"cmp"
	"strings"

	atlasv1 "github.com/ariga/atlas-operator/api/v1alpha1"
	rikamiv1 "github.com/b-zago/rikami-operator/api/v1"
	esv1 "github.com/external-secrets/external-secrets/apis/externalsecrets/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1ac "sigs.k8s.io/gateway-api/applyconfiguration/apis/v1"
)

// resources types

// VesselResourceSource source of given resource
type VesselResourceSource struct {
	*rikamiv1.Vessel
	*rikamiv1.Profile
}

// Database encapsulate atlasschema and externalsecret
type Database struct {
	AtlasSchema    *unstructured.Unstructured
	ExternalSecret *unstructured.Unstructured
}

// VesselServerResource server resource
type VesselServerResource struct {
	*VesselResourceSource
	Server          *rikamiv1.VesselServer
	Deployment      *appsv1ac.DeploymentApplyConfiguration
	Service         *corev1ac.ServiceApplyConfiguration
	HTTPRoute       *gwv1ac.HTTPRouteApplyConfiguration
	ExternalSecrets []*unstructured.Unstructured
	Databases       []*Database
}

func NewServer(v *rikamiv1.Vessel, p *rikamiv1.Profile, s *rikamiv1.VesselServer) *VesselServerResource {
	res := &VesselServerResource{
		VesselResourceSource: &VesselResourceSource{
			Profile: p,
			Vessel:  v,
		},
		Server: s,
	}
	res.Build()
	return res
}

func (r *VesselServerResource) newObject(apiVersion, kind, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": r.Vessel.Namespace,
			"labels": map[string]any{
				vesselLabel: r.Vessel.Name,
				serverLabel: r.Server.Name,
			},
			"ownerReferences": []any{map[string]any{
				"apiVersion":         rikamiv1.GroupVersion.String(),
				"kind":               "Vessel",
				"name":               r.Vessel.Name,
				"uid":                string(r.Vessel.UID),
				"controller":         true,
				"blockOwnerDeletion": true,
			}},
		},
		"spec": spec,
	}}
}

func (r *VesselServerResource) Build() *VesselServerResource {
	labels := map[string]string{vesselLabel: r.Vessel.Name, serverLabel: r.Server.Name}

	r.Deployment = appsv1ac.Deployment(r.Server.Name, r.Vessel.Namespace).
		WithOwnerReferences(metav1ac.OwnerReference().
			WithAPIVersion(rikamiv1.GroupVersion.String()).
			WithKind("Vessel").
			WithName(r.Vessel.Name).
			WithUID(r.Vessel.UID).
			WithController(true).
			WithBlockOwnerDeletion(true)).
		WithLabels(labels).
		WithSpec(appsv1ac.DeploymentSpec().
			WithSelector(metav1ac.LabelSelector().
				WithMatchLabels(labels)).
			WithTemplate(corev1ac.PodTemplateSpec().
				WithLabels(labels).
				WithSpec(corev1ac.PodSpec().
					WithContainers(corev1ac.Container().
						WithName(r.Vessel.Name).
						WithImage(r.Server.Image).
						WithImagePullPolicy(r.Profile.Spec.PullPolicy).
						WithPorts(corev1ac.ContainerPort().
							WithContainerPort(r.Server.Port))))))

	// deployments additions
	containers := r.Deployment.Spec.Template.Spec.Containers
	for _, secret := range r.Server.ExternalSecrets {
		for i := range containers {
			containers[i].WithEnvFrom(corev1ac.EnvFromSource().
				WithSecretRef(corev1ac.SecretEnvSource().
					WithName(secret.Name)))
		}
	}

	r.Service = corev1ac.Service(r.Server.Name, r.Vessel.Namespace).
		WithOwnerReferences(metav1ac.OwnerReference().
			WithAPIVersion(rikamiv1.GroupVersion.String()).
			WithKind("Vessel").
			WithName(r.Vessel.Name).
			WithUID(r.Vessel.UID).
			WithController(true).
			WithBlockOwnerDeletion(true)).
		WithLabels(labels).
		WithSpec(corev1ac.ServiceSpec().
			WithSelector(labels).
			WithType(corev1.ServiceTypeClusterIP).
			WithPorts(corev1ac.ServicePort().
				WithPort(80).
				WithTargetPort(intstr.IntOrString{Type: intstr.Int, IntVal: r.Server.Port})))

	var hostname string
	if r.Profile.Spec.NamespacedSubdomain {
		hostname = r.Vessel.Name + "." + r.Vessel.Namespace + "." + r.Profile.Spec.Domain
	} else {
		hostname = r.Vessel.Name + "." + r.Profile.Spec.Domain
	}

	r.HTTPRoute = gwv1ac.HTTPRoute(r.Server.Name, r.Vessel.Namespace).
		WithOwnerReferences(metav1ac.OwnerReference().
			WithAPIVersion(rikamiv1.GroupVersion.String()).
			WithKind("Vessel").
			WithName(r.Vessel.Name).
			WithUID(r.Vessel.UID).
			WithController(true).
			WithBlockOwnerDeletion(true)).
		WithLabels(labels).
		WithSpec(gwv1ac.HTTPRouteSpec().
			WithParentRefs(gwv1ac.ParentReference().
				WithKind(gwv1.Kind("Gateway")).
				WithName(gwv1.ObjectName(r.Profile.Spec.Gateway))).
			WithHostnames(gwv1.Hostname(hostname)).
			WithRules(gwv1ac.HTTPRouteRule().
				WithMatches(gwv1ac.HTTPRouteMatch().
					WithPath(gwv1ac.HTTPPathMatch().
						WithType(gwv1.PathMatchType("PathPrefix")).
						WithValue("/"))).
				WithBackendRefs(gwv1ac.HTTPBackendRef().
					WithName(gwv1.ObjectName(r.Server.Name)).
					WithPort(80))))

	r.ExternalSecrets = make([]*unstructured.Unstructured, len(r.Server.ExternalSecrets))

	for i, secret := range r.Server.ExternalSecrets {
		refreshPolicy := cmp.Or(secret.RefreshPolicy, r.Profile.Spec.ExternalSecretsConfig.RefreshPolicy)
		refreshInterval := cmp.Or(secret.RefreshInterval, r.Profile.Spec.ExternalSecretsConfig.RefreshInterval)
		secretStoreRef := cmp.Or(secret.SecretStoreRef, r.Profile.Spec.ExternalSecretsConfig.SecretStoreRef)

		data := make([]esv1.ExternalSecretData, len(secret.Data))

		for i, d := range secret.Data {
			data[i] = esv1.ExternalSecretData{
				SecretKey: d.SecretKey,
				RemoteRef: esv1.ExternalSecretDataRemoteRef{
					Key:      d.Key,
					Property: d.Property,
				},
			}
		}

		secretSpec := map[string]any{
			"secretStoreRef":  *secretStoreRef,
			"refreshPolicy":   *refreshPolicy,
			"refreshInterval": *refreshInterval,
			"data":            data,
		}

		newSecret := r.newObject("external-secrets.io/v1", "ExternalSecret", secret.Name, secretSpec)

		r.ExternalSecrets[i] = newSecret

	}

	r.Databases = make([]*Database, len(r.Server.Databases))

	for i, db := range r.Server.Databases {

		remoteSecretKey := strings.ReplaceAll(r.Profile.Spec.DatabaseConfig.SecretPath, "*", r.Server.Name)

		secretSpec := map[string]any{
			"secretStoreRef":  r.Profile.Spec.ExternalSecretsConfig.SecretStoreRef,
			"refreshPolicy":   r.Profile.Spec.ExternalSecretsConfig.RefreshPolicy,
			"refreshInterval": r.Profile.Spec.ExternalSecretsConfig.RefreshInterval,
			"data": []esv1.ExternalSecretData{{
				SecretKey: r.Profile.Spec.DatabaseConfig.SecretKey,
				RemoteRef: esv1.ExternalSecretDataRemoteRef{
					Key:      remoteSecretKey,
					Property: r.Profile.Spec.DatabaseConfig.SecretKey,
				},
			}},
		}

		newSecret := r.newObject("external-secrets.io/v1", "ExternalSecret", db.Name, secretSpec)

		atlasSchemaSpec := map[string]any{
			"urlFrom": atlasv1.Secret{
				SecretKeyRef: &corev1.SecretKeySelector{
					Key:                  r.Profile.Spec.DatabaseConfig.SecretKey,
					LocalObjectReference: corev1.LocalObjectReference{Name: db.Name},
				},
			},
			"schema": atlasv1.Schema{
				SQL: db.Schema,
			},
		}

		r.Databases[i] = &Database{
			AtlasSchema:    r.newObject("db.atlasgo.io/v1alpha1", "AtlasSchema", db.Name, atlasSchemaSpec),
			ExternalSecret: newSecret,
		}
	}

	return r
}
