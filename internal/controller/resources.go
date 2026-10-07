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

const (
	appDBSecretSuffix      = "-app"
	migratorDBSecretSuffix = "-migrator"

	entryPortName   = "entry"
	metricsPortName = "metrics"

	seedImage        = "postgres:17-alpine"
	seedBackoffLimit = 5
)

// ---------------------------------------------------------------------------
// Applied set (used for pruning)
// ---------------------------------------------------------------------------

// appliedSet records every object applied during a reconcile, so anything
// labelled as ours but not in the set can be pruned afterwards.
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

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// VesselResourceSource holds the inputs every resource is built from.
type VesselResourceSource struct {
	*rikamiv1.Vessel
	*rikamiv1.Profile
}

// Database groups the objects created for a single database.
type Database struct {
	AtlasSchema    *unstructured.Unstructured
	MigratorSecret *unstructured.Unstructured
	AppSecret      *unstructured.Unstructured
	SeedJob        *batchv1ac.JobApplyConfiguration
}

// VesselServerResource holds every object built for one server or service.
type VesselServerResource struct {
	*VesselResourceSource
	Server     *rikamiv1.VesselWorkload
	Services   []rikamiv1.VesselService
	RootDomain bool

	Deployment      *appsv1ac.DeploymentApplyConfiguration
	HPA             *autoscalingv2ac.HorizontalPodAutoscalerApplyConfiguration
	Service         *corev1ac.ServiceApplyConfiguration
	ServiceMonitor  *unstructured.Unstructured
	HTTPRoute       *gwv1ac.HTTPRouteApplyConfiguration
	ExternalSecrets []*unstructured.Unstructured
	Databases       []*Database
}

// NewServer builds all resources for a server (exposed through an HTTPRoute).
func NewServer(v *rikamiv1.Vessel, p *rikamiv1.Profile, s *rikamiv1.VesselServer) *VesselServerResource {
	res := &VesselServerResource{
		VesselResourceSource: &VesselResourceSource{Vessel: v, Profile: p},
		Server:               &s.VesselWorkload,
		Services:             s.Services,
		RootDomain:           s.RootDomain,
	}
	return res.Build(false)
}

// NewService builds all resources for an in-cluster service (no HTTPRoute).
func NewService(v *rikamiv1.Vessel, p *rikamiv1.Profile, s *rikamiv1.VesselService) *VesselServerResource {
	res := &VesselServerResource{
		VesselResourceSource: &VesselResourceSource{Vessel: v, Profile: p},
		Server:               &s.VesselWorkload,
	}
	return res.Build(true)
}

// Build fills in every resource for this workload. It is a pure function of
// the Vessel and Profile, so it can be unit tested without a cluster.
func (r *VesselServerResource) Build(isService bool) *VesselServerResource {
	labels := r.labels()

	r.Deployment = r.buildDeployment(labels)
	r.HPA = r.buildHPA(labels)
	r.Service = r.buildService(isService, labels)
	r.ServiceMonitor = r.buildServiceMonitor(labels)
	if !isService {
		r.HTTPRoute = r.buildHTTPRoute(labels)
	}
	r.ExternalSecrets = r.buildExternalSecrets()
	r.Databases = r.buildDatabases(labels)

	return r
}

// ---------------------------------------------------------------------------
// Shared metadata
// ---------------------------------------------------------------------------

func (r *VesselServerResource) labels() map[string]string {
	return map[string]string{vesselLabel: r.Vessel.Name, serverLabel: r.Server.Name}
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

// newObject builds an unstructured object for third-party CRDs that have no
// typed apply configurations (ExternalSecret, AtlasSchema, ServiceMonitor).
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

// ---------------------------------------------------------------------------
// Profile resolution
//
// Each setting follows the same rule: the Vessel-wide useProfileX flag is the
// default, a per-workload useProfileX overrides it, and when the profile is in
// use the workload's own value still wins over the profile's.
// ---------------------------------------------------------------------------

func useProfile(vesselDefault bool, workloadOverride *bool) bool {
	if workloadOverride != nil {
		return *workloadOverride
	}
	return vesselDefault
}

type probes struct {
	startup, readiness, liveness *corev1.Probe
}

func (r *VesselServerResource) resolveProbes() probes {
	s := r.Server
	if !useProfile(r.Vessel.Spec.UseProfileProbes, s.UseProfileProbes) {
		return probes{s.StartupProbe, s.ReadinessProbe, s.LivenessProbe}
	}
	p := r.Profile.Spec
	return probes{
		startup:   cmp.Or(s.StartupProbe, p.StartupProbe),
		readiness: cmp.Or(s.ReadinessProbe, p.ReadinessProbe),
		liveness:  cmp.Or(s.LivenessProbe, p.LivenessProbe),
	}
}

func (r *VesselServerResource) resolveResources() *corev1.ResourceRequirements {
	if useProfile(r.Vessel.Spec.UseProfileResources, r.Server.UseProfileResources) {
		return cmp.Or(r.Server.Resources, r.Profile.Spec.Resources)
	}
	return r.Server.Resources
}

func (r *VesselServerResource) resolveScheduling() *rikamiv1.Scheduling {
	if useProfile(r.Vessel.Spec.UseProfileScheduling, r.Server.UseProfileScheduling) {
		return cmp.Or(r.Server.Scheduling, r.Profile.Spec.Scheduling)
	}
	return r.Server.Scheduling
}

func (r *VesselServerResource) resolveAutoscaling() *rikamiv1.Autoscaling {
	if useProfile(r.Vessel.Spec.UseProfileAutoscaling, r.Server.UseProfileAutoscaling) {
		return cmp.Or(r.Server.Autoscaling, r.Profile.Spec.Autoscaling)
	}
	return r.Server.Autoscaling
}

// resolveMetrics has no Vessel-wide flag: the workload chooses between the
// profile's metrics and its own.
func (r *VesselServerResource) resolveMetrics() *rikamiv1.Metrics {
	if r.Server.UseProfileMetrics {
		return r.Profile.Spec.Metrics
	}
	return r.Server.Metrics
}

// ---------------------------------------------------------------------------
// Deployment
// ---------------------------------------------------------------------------

// buildDeployment never sets spec.replicas: when autoscaling is enabled the
// HPA owns that field, and setting it here would fight the HPA on every
// reconcile because we apply with ForceOwnership.
func (r *VesselServerResource) buildDeployment(labels map[string]string) *appsv1ac.DeploymentApplyConfiguration {
	podSpec := corev1ac.PodSpec().WithContainers(r.buildContainer())

	if r.Profile.Spec.PullSecret != nil {
		podSpec.WithImagePullSecrets(corev1ac.LocalObjectReference().WithName(*r.Profile.Spec.PullSecret))
	}
	r.applyScheduling(podSpec, labels)

	return appsv1ac.Deployment(r.Server.Name, r.Vessel.Namespace).
		WithOwnerReferences(r.ownerRef()).
		WithLabels(labels).
		WithSpec(appsv1ac.DeploymentSpec().
			WithSelector(metav1ac.LabelSelector().WithMatchLabels(labels)).
			WithTemplate(corev1ac.PodTemplateSpec().
				WithLabels(labels).
				WithSpec(podSpec)))
}

func (r *VesselServerResource) buildContainer() *corev1ac.ContainerApplyConfiguration {
	container := corev1ac.Container().
		WithName(r.Vessel.Name).
		WithImage(r.Server.Image).
		WithImagePullPolicy(r.Profile.Spec.PullPolicy).
		WithPorts(corev1ac.ContainerPort().
			WithName(entryPortName).
			WithContainerPort(r.Server.Port))

	if m := r.resolveMetrics(); m != nil {
		container.WithPorts(corev1ac.ContainerPort().
			WithName(metricsPortName).
			WithContainerPort(m.Port))
	}

	for _, secret := range r.Server.ExternalSecrets {
		container.WithEnvFrom(secretEnvSource(secret.Name))
	}
	for _, db := range r.Server.Databases {
		container.WithEnvFrom(secretEnvSource(db.Name + appDBSecretSuffix))
	}
	for _, ref := range r.Server.EnvSecretRefs {
		container.WithEnvFrom(secretEnvSource(ref))
	}
	for _, k := range slices.Sorted(maps.Keys(r.Server.Envs)) {
		container.WithEnv(corev1ac.EnvVar().WithName(k).WithValue(r.Server.Envs[k]))
	}

	p := r.resolveProbes()
	container.
		WithStartupProbe(toApplyConfig[corev1ac.ProbeApplyConfiguration](p.startup)).
		WithReadinessProbe(toApplyConfig[corev1ac.ProbeApplyConfiguration](p.readiness)).
		WithLivenessProbe(toApplyConfig[corev1ac.ProbeApplyConfiguration](p.liveness)).
		WithResources(toApplyConfig[corev1ac.ResourceRequirementsApplyConfiguration](r.resolveResources()))

	return container
}

// applyScheduling copies the resolved scheduling settings onto the pod spec.
// Topology spread constraints without a labelSelector get one that matches
// this server's pods, so profiles can stay generic.
func (r *VesselServerResource) applyScheduling(podSpec *corev1ac.PodSpecApplyConfiguration, labels map[string]string) {
	s := r.resolveScheduling()
	if s == nil {
		return
	}

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

// ---------------------------------------------------------------------------
// HorizontalPodAutoscaler
// ---------------------------------------------------------------------------

func (r *VesselServerResource) buildHPA(labels map[string]string) *autoscalingv2ac.HorizontalPodAutoscalerApplyConfiguration {
	a := r.resolveAutoscaling()
	if a == nil {
		return nil
	}

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

	return autoscalingv2ac.HorizontalPodAutoscaler(r.Server.Name, r.Vessel.Namespace).
		WithOwnerReferences(r.ownerRef()).
		WithLabels(labels).
		WithSpec(spec)
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

// ---------------------------------------------------------------------------
// Service, ServiceMonitor and HTTPRoute
// ---------------------------------------------------------------------------

// servicePort is the port the Service exposes: servers use the profile's
// default, in-cluster services keep their own port.
func (r *VesselServerResource) servicePort(isService bool) int32 {
	if isService {
		return r.Server.Port
	}
	return r.Profile.Spec.DefaultServicePort
}

func (r *VesselServerResource) buildService(isService bool, labels map[string]string) *corev1ac.ServiceApplyConfiguration {
	spec := corev1ac.ServiceSpec().
		WithSelector(labels).
		WithType(corev1.ServiceTypeClusterIP).
		WithPorts(corev1ac.ServicePort().
			WithName(entryPortName). // ports must be named once there is more than one
			WithPort(r.servicePort(isService)).
			WithTargetPort(intstr.FromString(entryPortName)))

	if m := r.resolveMetrics(); m != nil {
		spec.WithPorts(corev1ac.ServicePort().
			WithName(metricsPortName).
			WithPort(m.Port).
			WithTargetPort(intstr.FromString(metricsPortName)))
	}

	return corev1ac.Service(r.Server.Name, r.Vessel.Namespace).
		WithOwnerReferences(r.ownerRef()).
		WithLabels(labels).
		WithSpec(spec)
}

func (r *VesselServerResource) buildServiceMonitor(labels map[string]string) *unstructured.Unstructured {
	m := r.resolveMetrics()
	if m == nil {
		return nil
	}

	spec := map[string]any{
		"selector": metav1.LabelSelector{MatchLabels: labels},
		"namespaceSelector": monitoringv1.NamespaceSelector{
			MatchNames: []string{r.Vessel.Namespace},
		},
		"endpoints": []monitoringv1.Endpoint{{
			Port:     metricsPortName,
			Path:     m.Endpoint,
			Interval: monitoringv1.Duration(m.Interval),
		}},
	}
	return r.newObject("monitoring.coreos.com/v1", "ServiceMonitor", r.Server.Name, spec)
}

func (r *VesselServerResource) hostname() string {
	p := r.Profile.Spec
	switch {
	case r.RootDomain:
		return p.Domain
	case p.NamespacedSubdomain:
		return r.Vessel.Name + "." + r.Vessel.Namespace + "." + p.Domain
	default:
		return r.Vessel.Name + "." + p.Domain
	}
}

// buildHTTPRoute is only called for servers, so the backend port is always
// the profile's default service port.
func (r *VesselServerResource) buildHTTPRoute(labels map[string]string) *gwv1ac.HTTPRouteApplyConfiguration {
	return gwv1ac.HTTPRoute(r.Server.Name, r.Vessel.Namespace).
		WithOwnerReferences(r.ownerRef()).
		WithLabels(labels).
		WithSpec(gwv1ac.HTTPRouteSpec().
			WithParentRefs(gwv1ac.ParentReference().
				WithKind(gwv1.Kind("Gateway")).
				WithName(gwv1.ObjectName(r.Profile.Spec.Gateway))).
			WithHostnames(gwv1.Hostname(r.hostname())).
			WithRules(gwv1ac.HTTPRouteRule().
				WithMatches(gwv1ac.HTTPRouteMatch().
					WithPath(gwv1ac.HTTPPathMatch().
						WithType(gwv1.PathMatchPathPrefix).
						WithValue("/"))).
				WithBackendRefs(gwv1ac.HTTPBackendRef().
					WithName(gwv1.ObjectName(r.Server.Name)).
					WithPort(gwv1.PortNumber(r.servicePort(false))))))
}

// ---------------------------------------------------------------------------
// ExternalSecrets
// ---------------------------------------------------------------------------

func (r *VesselServerResource) buildExternalSecrets() []*unstructured.Unstructured {
	defaults := r.Profile.Spec.ExternalSecretsConfig
	secrets := make([]*unstructured.Unstructured, 0, len(r.Server.ExternalSecrets))

	for _, secret := range r.Server.ExternalSecrets {
		spec := externalSecretSpec(
			cmp.Or(secret.SecretStoreRef, defaults.SecretStoreRef),
			cmp.Or(secret.RefreshPolicy, defaults.RefreshPolicy),
			cmp.Or(secret.RefreshInterval, defaults.RefreshInterval),
		)

		// CRD validation guarantees exactly one of data or extract is set.
		if secret.Extract != nil {
			spec["dataFrom"] = []esv1.ExternalSecretDataFromRemoteRef{{
				Extract: &esv1.ExternalSecretDataRemoteRef{Key: *secret.Extract},
			}}
		} else {
			data := make([]esv1.ExternalSecretData, len(secret.Data))
			for i, d := range secret.Data {
				data[i] = remoteSecretData(d.SecretKey, d.Key, d.Property)
			}
			spec["data"] = data
		}

		secrets = append(secrets, r.newObject("external-secrets.io/v1", "ExternalSecret", secret.Name, spec))
	}
	return secrets
}

func externalSecretSpec(store *esv1.SecretStoreRef, policy *esv1.ExternalSecretRefreshPolicy, interval *metav1.Duration) map[string]any {
	return map[string]any{
		"secretStoreRef":  store,
		"refreshPolicy":   policy,
		"refreshInterval": interval,
	}
}

func remoteSecretData(secretKey, key, property string) esv1.ExternalSecretData {
	return esv1.ExternalSecretData{
		SecretKey: secretKey,
		RemoteRef: esv1.ExternalSecretDataRemoteRef{Key: key, Property: property},
	}
}

func secretEnvSource(name string) *corev1ac.EnvFromSourceApplyConfiguration {
	return corev1ac.EnvFromSource().WithSecretRef(corev1ac.SecretEnvSource().WithName(name))
}

// ---------------------------------------------------------------------------
// Databases
// ---------------------------------------------------------------------------

func (r *VesselServerResource) buildDatabases(labels map[string]string) []*Database {
	dbs := make([]*Database, 0, len(r.Server.Databases))
	for _, db := range r.Server.Databases {
		dbs = append(dbs, r.buildDatabase(db, labels))
	}
	return dbs
}

// buildDatabase creates two ExternalSecrets (migrator credentials for Atlas,
// app credentials injected into the pod), the AtlasSchema and, optionally, a
// seed Job.
func (r *VesselServerResource) buildDatabase(db rikamiv1.Database, labels map[string]string) *Database {
	esc := r.Profile.Spec.ExternalSecretsConfig
	app := r.Profile.Spec.DatabaseConfig.DatabaseAppConfig
	migrator := r.Profile.Spec.DatabaseConfig.DatabaseMigratorConfig

	migratorSecretName := db.Name + migratorDBSecretSuffix
	appSecretName := db.Name + appDBSecretSuffix
	migratorRemoteKey := strings.ReplaceAll(migrator.SecretMigratorPath, "*", db.Name)
	appRemoteKey := strings.ReplaceAll(app.SecretAppPath, "*", db.Name)

	migratorSpec := externalSecretSpec(esc.SecretStoreRef, esc.RefreshPolicy, esc.RefreshInterval)
	migratorSpec["data"] = []esv1.ExternalSecretData{
		remoteSecretData(migrator.SecretKey, migratorRemoteKey, migrator.SecretKey),
	}

	appSpec := externalSecretSpec(esc.SecretStoreRef, esc.RefreshPolicy, esc.RefreshInterval)
	appSpec["data"] = []esv1.ExternalSecretData{
		remoteSecretData(r.dbEnvKey(app.HostKey), appRemoteKey, app.HostKey),
		remoteSecretData(r.dbEnvKey(app.PasswordKey), appRemoteKey, app.PasswordKey),
		remoteSecretData(r.dbEnvKey(app.UsernameKey), appRemoteKey, app.UsernameKey),
	}

	atlasSchemaSpec := map[string]any{
		"urlFrom": atlasv1.Secret{
			SecretKeyRef: &corev1.SecretKeySelector{
				Key:                  migrator.SecretKey,
				LocalObjectReference: corev1.LocalObjectReference{Name: migratorSecretName},
			},
		},
		"schema": atlasv1.Schema{SQL: db.Schema},
	}

	out := &Database{
		MigratorSecret: r.newObject("external-secrets.io/v1", "ExternalSecret", migratorSecretName, migratorSpec),
		AppSecret:      r.newObject("external-secrets.io/v1", "ExternalSecret", appSecretName, appSpec),
		AtlasSchema:    r.newObject("db.atlasgo.io/v1alpha1", "AtlasSchema", db.Name, atlasSchemaSpec),
	}
	if db.Seed != nil {
		out.SeedJob = r.buildSeedJob(db, appSecretName, labels)
	}
	return out
}

// dbEnvKey is the env var name an app credential is exposed under.
func (r *VesselServerResource) dbEnvKey(key string) string {
	return r.Profile.Spec.DatabaseConfig.EnvKeysPrefix + key
}

func (r *VesselServerResource) buildSeedJob(db rikamiv1.Database, appSecretName string, labels map[string]string) *batchv1ac.JobApplyConfiguration {
	app := r.Profile.Spec.DatabaseConfig.DatabaseAppConfig

	container := corev1ac.Container().
		WithName("seed").
		WithImage(seedImage).
		WithEnvFrom(secretEnvSource(appSecretName)).
		WithEnv(corev1ac.EnvVar().
			WithName("PGPASSWORD").
			WithValueFrom(corev1ac.EnvVarSource().
				WithSecretKeyRef(corev1ac.SecretKeySelector().
					WithName(appSecretName).
					WithKey(r.dbEnvKey(app.PasswordKey))))).
		WithCommand("psql").
		WithArgs(
			"-h", "$("+r.dbEnvKey(app.HostKey)+")",
			"-U", "$("+r.dbEnvKey(app.UsernameKey)+")",
			"-d", db.Name,
			"-v", "ON_ERROR_STOP=1",
			"-c", *db.Seed,
		)

	return batchv1ac.Job(db.Name, r.Vessel.Namespace).
		WithOwnerReferences(r.ownerRef()).
		WithLabels(labels).
		WithSpec(batchv1ac.JobSpec().
			WithBackoffLimit(seedBackoffLimit).
			WithTemplate(corev1ac.PodTemplateSpec().
				WithSpec(corev1ac.PodSpec().
					WithRestartPolicy(corev1.RestartPolicyOnFailure).
					WithContainers(container))))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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
