package controller

import (
	"cmp"
	"encoding/json"
	"strings"

	atlasv1 "github.com/ariga/atlas-operator/api/v1alpha1"
	rikamiv1 "github.com/b-zago/rikami-operator/api/v1"
	esv1 "github.com/external-secrets/external-secrets/apis/externalsecrets/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	batchv1ac "k8s.io/client-go/applyconfigurations/batch/v1"
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
	MigratorSecret *unstructured.Unstructured
	AppSecret      *unstructured.Unstructured
	SeedJob        *batchv1ac.JobApplyConfiguration
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
	res.Build(false)
	return res
}

// NewService as separate func for readability and ease of use
func NewService(v *rikamiv1.Vessel, p *rikamiv1.Profile, s *rikamiv1.VesselServer) *VesselServerResource {
	res := &VesselServerResource{
		VesselResourceSource: &VesselResourceSource{
			Profile: p,
			Vessel:  v,
		},
		Server: s,
	}
	res.Build(true)
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

func (r *VesselServerResource) Build(isService bool) *VesselServerResource {
	appDBSecretSuffix := "-app"
	migratorDBSecretSuffix := "-migrator"
	labels := map[string]string{vesselLabel: r.Vessel.Name, serverLabel: r.Server.Name}

	container := r.buildContainer(appDBSecretSuffix)

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
					WithContainers(container))))

	var svcPort int32

	if isService {
		svcPort = r.Server.Port
	} else {
		svcPort = r.Profile.Spec.DefaultServicePort
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
				WithPort(svcPort).
				WithTargetPort(intstr.IntOrString{Type: intstr.Int, IntVal: r.Server.Port})))

	if !isService {
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
	}

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

		remoteMigratorSecretKey := strings.ReplaceAll(r.Profile.Spec.DatabaseConfig.DatabaseMigratorConfig.SecretMigratorPath, "*", db.Name)
		remoteAppSecretKey := strings.ReplaceAll(r.Profile.Spec.DatabaseConfig.DatabaseAppConfig.SecretAppPath, "*", db.Name)

		migratorSecretSpec := map[string]any{
			"secretStoreRef":  r.Profile.Spec.ExternalSecretsConfig.SecretStoreRef,
			"refreshPolicy":   r.Profile.Spec.ExternalSecretsConfig.RefreshPolicy,
			"refreshInterval": r.Profile.Spec.ExternalSecretsConfig.RefreshInterval,
			"data": []esv1.ExternalSecretData{{
				SecretKey: r.Profile.Spec.DatabaseConfig.DatabaseMigratorConfig.SecretKey,
				RemoteRef: esv1.ExternalSecretDataRemoteRef{
					Key:      remoteMigratorSecretKey,
					Property: r.Profile.Spec.DatabaseConfig.DatabaseMigratorConfig.SecretKey,
				},
			}},
		}

		migratorSecretName := db.Name + migratorDBSecretSuffix
		migratorSecret := r.newObject("external-secrets.io/v1", "ExternalSecret", migratorSecretName, migratorSecretSpec)

		appSecretSpec := map[string]any{
			"secretStoreRef":  r.Profile.Spec.ExternalSecretsConfig.SecretStoreRef,
			"refreshPolicy":   r.Profile.Spec.ExternalSecretsConfig.RefreshPolicy,
			"refreshInterval": r.Profile.Spec.ExternalSecretsConfig.RefreshInterval,
			"data": []esv1.ExternalSecretData{{
				SecretKey: r.Profile.Spec.DatabaseConfig.EnvKeysPrefix + r.Profile.Spec.DatabaseConfig.DatabaseAppConfig.HostKey,
				RemoteRef: esv1.ExternalSecretDataRemoteRef{
					Key:      remoteAppSecretKey,
					Property: r.Profile.Spec.DatabaseConfig.DatabaseAppConfig.HostKey,
				},
			}, {
				SecretKey: r.Profile.Spec.DatabaseConfig.EnvKeysPrefix + r.Profile.Spec.DatabaseConfig.DatabaseAppConfig.PasswordKey,
				RemoteRef: esv1.ExternalSecretDataRemoteRef{
					Key:      remoteAppSecretKey,
					Property: r.Profile.Spec.DatabaseConfig.DatabaseAppConfig.PasswordKey,
				},
			}, {
				SecretKey: r.Profile.Spec.DatabaseConfig.EnvKeysPrefix + r.Profile.Spec.DatabaseConfig.DatabaseAppConfig.UsernameKey,
				RemoteRef: esv1.ExternalSecretDataRemoteRef{
					Key:      remoteAppSecretKey,
					Property: r.Profile.Spec.DatabaseConfig.DatabaseAppConfig.UsernameKey,
				},
			}},
		}
		appSecretName := db.Name + appDBSecretSuffix
		appSecret := r.newObject("external-secrets.io/v1", "ExternalSecret", db.Name+"-app", appSecretSpec)

		atlasSchemaSpec := map[string]any{
			"urlFrom": atlasv1.Secret{
				SecretKeyRef: &corev1.SecretKeySelector{
					Key:                  r.Profile.Spec.DatabaseConfig.DatabaseMigratorConfig.SecretKey,
					LocalObjectReference: corev1.LocalObjectReference{Name: migratorSecretName},
				},
			},
			"schema": atlasv1.Schema{
				SQL: db.Schema,
			},
		}

		r.Databases[i] = &Database{
			AtlasSchema:    r.newObject("db.atlasgo.io/v1alpha1", "AtlasSchema", db.Name, atlasSchemaSpec),
			MigratorSecret: migratorSecret,
			AppSecret:      appSecret,
		}

		// optionals

		// seed job

		if db.Seed != nil {
			seedJob := batchv1ac.Job(db.Name, r.Vessel.Namespace).
				WithOwnerReferences(metav1ac.OwnerReference().
					WithAPIVersion(rikamiv1.GroupVersion.String()).
					WithKind("Vessel").
					WithName(r.Vessel.Name).
					WithUID(r.Vessel.UID).
					WithController(true).
					WithBlockOwnerDeletion(true)).
				WithLabels(labels).
				WithSpec(batchv1ac.JobSpec().
					WithBackoffLimit(5).
					WithTemplate(corev1ac.PodTemplateSpec().
						WithSpec(corev1ac.PodSpec().
							WithRestartPolicy(corev1.RestartPolicyOnFailure).
							WithContainers(corev1ac.Container().
								WithName("seed").
								WithImage("postgres:17-alpine").
								WithEnvFrom(corev1ac.EnvFromSource().
									WithSecretRef(corev1ac.SecretEnvSource().
										WithName(appSecretName))).
								WithEnv(corev1ac.EnvVar().
									WithName("PGPASSWORD").
									WithValueFrom(corev1ac.EnvVarSource().
										WithSecretKeyRef(corev1ac.SecretKeySelector().
											WithName(appSecretName).
											WithKey(r.Profile.Spec.DatabaseConfig.EnvKeysPrefix+r.Profile.Spec.DatabaseConfig.DatabaseAppConfig.PasswordKey)))).
								WithCommand("psql").
								WithArgs(
									"-h", "$("+r.Profile.Spec.DatabaseConfig.EnvKeysPrefix+r.Profile.Spec.DatabaseConfig.DatabaseAppConfig.HostKey+")",
									"-U", "$("+r.Profile.Spec.DatabaseConfig.EnvKeysPrefix+r.Profile.Spec.DatabaseConfig.DatabaseAppConfig.UsernameKey+")",
									"-d", db.Name,
									"-v", "ON_ERROR_STOP=1",
									"-c", *db.Seed,
								)))))
			r.Databases[i].SeedJob = seedJob
		}

	}

	return r
}

func (r *VesselServerResource) buildContainer(appDBSecretSuffix string) *corev1ac.ContainerApplyConfiguration {
	container := corev1ac.Container().
		WithName(r.Vessel.Name).
		WithImage(r.Server.Image).
		WithImagePullPolicy(r.Profile.Spec.PullPolicy).
		WithPorts(corev1ac.ContainerPort().
			WithContainerPort(r.Server.Port))

	for _, secret := range r.Server.ExternalSecrets {
		container.WithEnvFrom(corev1ac.EnvFromSource().WithSecretRef(corev1ac.SecretEnvSource().WithName(secret.Name)))
	}

	for _, db := range r.Server.Databases {
		container.WithEnvFrom(corev1ac.EnvFromSource().WithSecretRef(corev1ac.SecretEnvSource().WithName(db.Name + appDBSecretSuffix)))
	}

	for k, v := range r.Server.Envs {
		container.WithEnv(corev1ac.EnvVar().WithName(k).WithValue(v))
	}

	for _, ref := range r.Server.EnvSecretRefs {
		container.WithEnvFrom(corev1ac.EnvFromSource().WithSecretRef(corev1ac.SecretEnvSource().WithName(ref)))
	}

	useProbes := r.Vessel.Spec.UseProfileProbes
	if r.Server.UseProfileProbes != nil {
		useProbes = *r.Server.UseProfileProbes
	}

	if useProbes {
		container.
			WithStartupProbe(toApplyConfig[corev1ac.ProbeApplyConfiguration](cmp.Or(r.Server.StartupProbe, r.Profile.Spec.StartupProbe))).
			WithStartupProbe(toApplyConfig[corev1ac.ProbeApplyConfiguration](cmp.Or(r.Server.ReadinessProbe, r.Profile.Spec.ReadinessProbe))).
			WithStartupProbe(toApplyConfig[corev1ac.ProbeApplyConfiguration](cmp.Or(r.Server.LivenessProbe, r.Profile.Spec.LivenessProbe)))
	} else {
		container.
			WithStartupProbe(toApplyConfig[corev1ac.ProbeApplyConfiguration](r.Server.StartupProbe)).
			WithReadinessProbe(toApplyConfig[corev1ac.ProbeApplyConfiguration](r.Server.ReadinessProbe)).
			WithLivenessProbe(toApplyConfig[corev1ac.ProbeApplyConfiguration](r.Server.LivenessProbe))
	}

	container.WithResources(toApplyConfig[corev1ac.ResourceRequirementsApplyConfiguration](r.Server.Resources))

	return container
}

// toApplyConfig converts a core API type into its apply configuration
// equivalent via a JSON round-trip. Both types share the same JSON shape,
// and apply configurations use pointers with omitempty, so unset fields
// stay nil and won't be owned by our field manager.
// Note: explicit zero values in non-pointer, omitempty fields are dropped.
func toApplyConfig[AC any, T any](in *T) *AC {
	if in == nil {
		return nil
	}
	data, _ := json.Marshal(in)
	out := new(AC)
	_ = json.Unmarshal(data, out)
	return out
}
