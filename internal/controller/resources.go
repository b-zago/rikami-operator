package controller

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	atlasv1 "github.com/ariga/atlas-operator/api/v1alpha1"
	rikamiv1 "github.com/b-zago/rikami-operator/api/v1alpha1"
	esv1 "github.com/external-secrets/external-secrets/apis/externalsecrets/v1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	autoscalingv2ac "k8s.io/client-go/applyconfigurations/autoscaling/v2"
	batchv1ac "k8s.io/client-go/applyconfigurations/batch/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1ac "sigs.k8s.io/gateway-api/applyconfiguration/apis/v1"
)

// needed for pruning
type appliedSet map[string]struct{}

func appliedKey(gk schema.GroupKind, name string) string {
	return gk.String() + "/" + name
}

func (s appliedSet) add(apiVersion, kind, name string) {
	gv, _ := schema.ParseGroupVersion(apiVersion)
	s[appliedKey(gv.WithKind(kind).GroupKind(), name)] = struct{}{}
}

func (s appliedSet) has(gk schema.GroupKind, name string) bool {
	_, ok := s[appliedKey(gk, name)]
	return ok
}

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
	Server          *rikamiv1.VesselWorkload
	Deployment      *appsv1ac.DeploymentApplyConfiguration
	Service         *corev1ac.ServiceApplyConfiguration
	HPA             *autoscalingv2ac.HorizontalPodAutoscalerApplyConfiguration
	ServiceMonitor  *unstructured.Unstructured
	HTTPRoute       *gwv1ac.HTTPRouteApplyConfiguration
	ExternalSecrets []*unstructured.Unstructured
	Databases       []*Database
	Services        []rikamiv1.VesselService
	RootDomain      bool
}

func NewServer(v *rikamiv1.Vessel, p *rikamiv1.Profile, s *rikamiv1.VesselServer) *VesselServerResource {
	res := &VesselServerResource{
		VesselResourceSource: &VesselResourceSource{
			Profile: p,
			Vessel:  v,
		},
		Server:     &s.VesselWorkload,
		Services:   s.Services,
		RootDomain: s.RootDomain,
	}
	res.Build(false)
	return res
}

// NewService as separate func for readability and ease of use
func NewService(v *rikamiv1.Vessel, p *rikamiv1.Profile, s *rikamiv1.VesselService) *VesselServerResource {
	res := &VesselServerResource{
		VesselResourceSource: &VesselResourceSource{
			Profile: p,
			Vessel:  v,
		},
		Server: &s.VesselWorkload,
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

func (r *VesselServerResource) ownerRef() *metav1ac.OwnerReferenceApplyConfiguration {
	return metav1ac.OwnerReference().
		WithAPIVersion(rikamiv1.GroupVersion.String()).
		WithKind("Vessel").
		WithName(r.Vessel.Name).
		WithUID(r.Vessel.UID).
		WithController(true).
		WithBlockOwnerDeletion(true)
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

	if r.Profile.Spec.PullSecret != nil {
		r.Deployment.Spec.Template.Spec.WithImagePullSecrets(corev1ac.LocalObjectReference().WithName(*r.Profile.Spec.PullSecret))
	}

	if s := r.resolveScheduling(); s != nil {
		podSpec := r.Deployment.Spec.Template.Spec
		if len(s.NodeSelector) > 0 {
			podSpec.WithNodeSelector(s.NodeSelector)
		}
		if s.Affinity != nil {
			podSpec.WithAffinity(toApplyConfig[corev1ac.AffinityApplyConfiguration](s.Affinity))
		}
		for i := range s.Tolerations {
			podSpec.WithTolerations(toApplyConfig[corev1ac.TolerationApplyConfiguration](&s.Tolerations[i]))
		}
		for i := range s.TopologySpreadConstraints {
			tsc := toApplyConfig[corev1ac.TopologySpreadConstraintApplyConfiguration](&s.TopologySpreadConstraints[i])
			if tsc.LabelSelector == nil {
				tsc.WithLabelSelector(metav1ac.LabelSelector().WithMatchLabels(labels))
			}
			podSpec.WithTopologySpreadConstraints(tsc)
		}
	}

	if a := r.resolveAutoscaling(); a != nil {
		spec := autoscalingv2ac.HorizontalPodAutoscalerSpec().
			WithScaleTargetRef(autoscalingv2ac.CrossVersionObjectReference().
				WithAPIVersion("apps/v1").
				WithKind("Deployment").
				WithName(r.Server.Name)).
			WithMaxReplicas(a.MaxReplicas)
		if a.MinReplicas != nil {
			spec.WithMinReplicas(*a.MinReplicas)
		}
		if a.TargetCPUUtilization != nil {
			spec.WithMetrics(resourceMetric(corev1.ResourceCPU, *a.TargetCPUUtilization))
		}
		if a.TargetMemoryUtilization != nil {
			spec.WithMetrics(resourceMetric(corev1.ResourceMemory, *a.TargetMemoryUtilization))
		}

		r.HPA = autoscalingv2ac.HorizontalPodAutoscaler(r.Server.Name, r.Vessel.Namespace).
			WithOwnerReferences(r.ownerRef()).
			WithLabels(labels).
			WithSpec(spec)
	}

	var svcPort int32

	if isService {
		svcPort = r.Server.Port
	} else {
		svcPort = r.Profile.Spec.DefaultServicePort
	}

	r.Service = corev1ac.Service(r.Server.Name, r.Vessel.Namespace).
		WithOwnerReferences( /* ... */ ).
		WithLabels(labels).
		WithSpec(corev1ac.ServiceSpec().
			WithSelector(labels).
			WithType(corev1.ServiceTypeClusterIP).
			WithPorts(corev1ac.ServicePort().
				WithName("entry"). // required once the Service has more than one port
				WithPort(svcPort).
				WithTargetPort(intstr.FromString("entry"))))

	if r.Server.UseProfileMetrics {
		if m := r.Profile.Spec.Metrics; m != nil {
			r.Service.Spec.WithPorts(corev1ac.ServicePort().
				WithName("metrics").
				WithPort(m.Port).
				WithTargetPort(intstr.FromString("metrics")))

			monitorSpec := r.buildServiceMonitorSpec(r.Profile.Spec.Metrics.Endpoint, r.Profile.Spec.Metrics.Interval, labels)
			r.ServiceMonitor = r.newObject("monitoring.coreos.com/v1", "ServiceMonitor", r.Server.Name, monitorSpec)
		}
	} else {
		if m := r.Server.Metrics; m != nil {
			r.Service.Spec.WithPorts(corev1ac.ServicePort().
				WithName("metrics").
				WithPort(m.Port).
				WithTargetPort(intstr.FromString("metrics")))

			monitorSpec := r.buildServiceMonitorSpec(r.Server.Metrics.Endpoint, r.Server.Metrics.Interval, labels)
			r.ServiceMonitor = r.newObject("monitoring.coreos.com/v1", "ServiceMonitor", r.Server.Name, monitorSpec)
		}
	}

	if !isService {
		var hostname string
		if r.RootDomain {
			hostname = r.Profile.Spec.Domain
		} else if r.Profile.Spec.NamespacedSubdomain {
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
		isDataFrom := false

		if secret.Data != nil {
			for i, d := range secret.Data {
				data[i] = esv1.ExternalSecretData{
					SecretKey: d.SecretKey,
					RemoteRef: esv1.ExternalSecretDataRemoteRef{
						Key:      d.Key,
						Property: d.Property,
					},
				}
			}
		} else {
			isDataFrom = true
		}

		secretSpec := map[string]any{
			"secretStoreRef":  *secretStoreRef,
			"refreshPolicy":   *refreshPolicy,
			"refreshInterval": *refreshInterval,
		}

		if isDataFrom {
			secretSpec["dataFrom"] = []esv1.ExternalSecretDataFromRemoteRef{{
				Extract: &esv1.ExternalSecretDataRemoteRef{
					Key: *secret.Extract,
				},
			}}
		} else {
			secretSpec["data"] = data
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
			WithContainerPort(r.Server.Port).
			WithName("entry"))

	if r.Server.UseProfileMetrics {
		if r.Profile.Spec.Metrics != nil {
			container.WithPorts(corev1ac.ContainerPort().WithName("metrics").WithContainerPort(r.Profile.Spec.Metrics.Port))
		}
	} else {
		if r.Server.Metrics != nil {
			container.WithPorts(corev1ac.ContainerPort().WithName("metrics").WithContainerPort(r.Server.Metrics.Port))
		}
	}

	for _, secret := range r.Server.ExternalSecrets {
		container.WithEnvFrom(corev1ac.EnvFromSource().WithSecretRef(corev1ac.SecretEnvSource().WithName(secret.Name)))
	}

	for _, db := range r.Server.Databases {
		container.WithEnvFrom(corev1ac.EnvFromSource().WithSecretRef(corev1ac.SecretEnvSource().WithName(db.Name + appDBSecretSuffix)))
	}

	for _, k := range slices.Sorted(maps.Keys(r.Server.Envs)) {
		container.WithEnv(corev1ac.EnvVar().WithName(k).WithValue(r.Server.Envs[k]))
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

	useResources := r.Vessel.Spec.UseProfileResources
	if r.Server.UseProfileResources != nil {
		useResources = *r.Server.UseProfileResources
	}

	if useResources {
		container.WithResources(toApplyConfig[corev1ac.ResourceRequirementsApplyConfiguration](cmp.Or(r.Server.Resources, r.Profile.Spec.Resources)))
	} else {
		container.WithResources(toApplyConfig[corev1ac.ResourceRequirementsApplyConfiguration](r.Server.Resources))
	}

	return container
}

func (r *VesselResourceSource) buildServiceMonitorSpec(endpoint, interval string, labels map[string]string) map[string]any {
	spec := map[string]any{
		"selector": metav1.LabelSelector{
			MatchLabels: labels,
		},
		"namespaceSelector": monitoringv1.NamespaceSelector{
			MatchNames: []string{r.Vessel.Namespace},
		},
		"endpoints": []monitoringv1.Endpoint{{
			Port:     "metrics",
			Path:     endpoint,
			Interval: monitoringv1.Duration(interval),
		}},
	}
	return spec
}

func (r *VesselServerResource) resolveScheduling() *rikamiv1.Scheduling {
	use := r.Vessel.Spec.UseProfileScheduling
	if r.Server.UseProfileScheduling != nil {
		use = *r.Server.UseProfileScheduling
	}
	if use {
		return cmp.Or(r.Server.Scheduling, r.Profile.Spec.Scheduling)
	}
	return r.Server.Scheduling
}

func resourceMetric(name corev1.ResourceName, target int32) *autoscalingv2ac.MetricSpecApplyConfiguration {
	return autoscalingv2ac.MetricSpec().
		WithType(autoscalingv2.ResourceMetricSourceType).
		WithResource(autoscalingv2ac.ResourceMetricSource().
			WithName(name).
			WithTarget(autoscalingv2ac.MetricTarget().
				WithType(autoscalingv2.UtilizationMetricType).
				WithAverageUtilization(target)))
}

func (r *VesselServerResource) resolveAutoscaling() *rikamiv1.Autoscaling {
	use := r.Vessel.Spec.UseProfileAutoscaling
	if r.Server.UseProfileAutoscaling != nil {
		use = *r.Server.UseProfileAutoscaling
	}
	if use {
		return cmp.Or(r.Server.Autoscaling, r.Profile.Spec.Autoscaling)
	}
	return r.Server.Autoscaling
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
