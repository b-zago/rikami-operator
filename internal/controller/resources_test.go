package controller

import (
	"maps"
	"slices"
	"testing"
	"time"

	rikamiv1 "github.com/b-zago/rikami-operator/api/v1alpha1"
	esv1 "github.com/external-secrets/external-secrets/apis/externalsecrets/v1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/utils/ptr"
)

// These tests exercise Build() only. It is a pure function of the Vessel and
// Profile, so no cluster or envtest is needed:
//
//	go test ./internal/controller/ -run 'TestBuild|TestAppliedSet' -v

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const (
	testServerName     = "api"
	testNodeLabel      = "pool"
	testNodePool       = "apps"
	profileStartupPath = "/profile-startup"
	profileReadyPath   = "/profile-ready"
	profileHealthzPath = "/profile-healthz"
	customReadyPath    = "/custom-ready"
)

func testVessel(mutate ...func(*rikamiv1.Vessel)) *rikamiv1.Vessel {
	v := &rikamiv1.Vessel{
		ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "staging", UID: "vessel-uid"},
	}
	for _, m := range mutate {
		m(v)
	}
	return v
}

func testProfile(mutate ...func(*rikamiv1.Profile)) *rikamiv1.Profile {
	p := &rikamiv1.Profile{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "staging"},
		Spec: rikamiv1.ProfileSpec{
			Gateway:            "gw",
			PullPolicy:         corev1.PullIfNotPresent,
			Domain:             "example.com",
			DefaultServicePort: 8080, // deliberately not 80
			ExternalSecretsConfig: rikamiv1.ExternalSecretsConfig{
				RefreshPolicy:   ptr.To(esv1.ExternalSecretRefreshPolicy("CreatedOnce")),
				RefreshInterval: &metav1.Duration{Duration: time.Hour},
				SecretStoreRef:  &esv1.SecretStoreRef{Name: "profile-store", Kind: "ClusterSecretStore"},
			},
			DatabaseConfig: rikamiv1.DatabaseConfig{
				DatabaseAppConfig: rikamiv1.DatabaseAppConfig{
					UsernameKey:   "username",
					PasswordKey:   "password",
					HostKey:       "host",
					SecretAppPath: "/db/*/app",
				},
				DatabaseMigratorConfig: rikamiv1.DatabaseMigratorConfig{
					SecretMigratorPath: "/db/*/migrator",
					SecretKey:          "url",
				},
				EnvKeysPrefix: "db_",
			},
			StartupProbe:   httpProbe(profileStartupPath),
			ReadinessProbe: httpProbe(profileReadyPath),
			LivenessProbe:  httpProbe(profileHealthzPath),
			Resources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			},
		},
	}
	for _, m := range mutate {
		m(p)
	}
	return p
}

func testServer(mutate ...func(*rikamiv1.VesselServer)) *rikamiv1.VesselServer {
	s := &rikamiv1.VesselServer{
		VesselWorkload: rikamiv1.VesselWorkload{Name: testServerName, Image: "ghcr.io/example/api:1.0.0", Port: 3000},
	}
	for _, m := range mutate {
		m(s)
	}
	return s
}

func httpProbe(path string) *corev1.Probe {
	return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path}}}
}

func probePath(p *corev1ac.ProbeApplyConfiguration) string {
	if p == nil || p.HTTPGet == nil || p.HTTPGet.Path == nil {
		return ""
	}
	return *p.HTTPGet.Path
}

func container(t *testing.T, r *VesselServerResource) *corev1ac.ContainerApplyConfiguration {
	t.Helper()
	cs := r.Deployment.Spec.Template.Spec.Containers
	if len(cs) != 1 {
		t.Fatalf("expected 1 container, got %d", len(cs))
	}
	return &cs[0]
}

// ---------------------------------------------------------------------------
// Profile resolution
// ---------------------------------------------------------------------------

func TestBuildProbes(t *testing.T) {
	tests := []struct {
		name                         string
		vesselFlag                   bool
		workloadFlag                 *bool
		workloadReadiness            *corev1.Probe
		startup, readiness, liveness string
	}{
		{
			name:       "vessel flag on uses all three profile probes",
			vesselFlag: true,
			startup:    profileStartupPath, readiness: profileReadyPath, liveness: profileHealthzPath,
		},
		{
			name:              "workload probe wins over profile",
			vesselFlag:        true,
			workloadReadiness: httpProbe(customReadyPath),
			startup:           profileStartupPath, readiness: customReadyPath, liveness: profileHealthzPath,
		},
		{
			name:         "workload flag false overrides vessel flag",
			vesselFlag:   true,
			workloadFlag: ptr.To(false),
		},
		{
			name:         "workload flag true overrides vessel default",
			workloadFlag: ptr.To(true),
			startup:      profileStartupPath, readiness: profileReadyPath, liveness: profileHealthzPath,
		},
		{
			name:              "profile off uses only workload probes",
			workloadReadiness: httpProbe(customReadyPath),
			readiness:         customReadyPath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := testVessel(func(v *rikamiv1.Vessel) { v.Spec.UseProfileProbes = tt.vesselFlag })
			s := testServer(func(s *rikamiv1.VesselServer) {
				s.UseProfileProbes = tt.workloadFlag
				s.ReadinessProbe = tt.workloadReadiness
			})

			c := container(t, NewServer(v, testProfile(), s))

			if got := probePath(c.StartupProbe); got != tt.startup {
				t.Errorf("startup probe = %q, want %q", got, tt.startup)
			}
			if got := probePath(c.ReadinessProbe); got != tt.readiness {
				t.Errorf("readiness probe = %q, want %q", got, tt.readiness)
			}
			if got := probePath(c.LivenessProbe); got != tt.liveness {
				t.Errorf("liveness probe = %q, want %q", got, tt.liveness)
			}
		})
	}
}

func TestBuildResources(t *testing.T) {
	t.Run("profile resources when enabled", func(t *testing.T) {
		v := testVessel(func(v *rikamiv1.Vessel) { v.Spec.UseProfileResources = true })
		c := container(t, NewServer(v, testProfile(), testServer()))

		if c.Resources == nil || c.Resources.Requests == nil {
			t.Fatal("expected resource requests from profile")
		}
		if got := (*c.Resources.Requests)[corev1.ResourceCPU]; got.String() != "100m" {
			t.Errorf("cpu request = %s, want 100m", got.String())
		}
	})

	t.Run("no resources when disabled", func(t *testing.T) {
		c := container(t, NewServer(testVessel(), testProfile(), testServer()))
		if c.Resources != nil {
			t.Errorf("expected no resources, got %+v", c.Resources)
		}
	})
}

// ---------------------------------------------------------------------------
// Scheduling
// ---------------------------------------------------------------------------

func TestBuildScheduling(t *testing.T) {
	profile := testProfile(func(p *rikamiv1.Profile) {
		p.Spec.Scheduling = &rikamiv1.Scheduling{
			NodeSelector: map[string]string{testNodeLabel: testNodePool},
			Tolerations:  []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}},
			TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
				{MaxSkew: 1, TopologyKey: "kubernetes.io/hostname", WhenUnsatisfiable: corev1.ScheduleAnyway},
				{
					MaxSkew: 1, TopologyKey: "topology.kubernetes.io/zone", WhenUnsatisfiable: corev1.ScheduleAnyway,
					LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"custom": "spread"}},
				},
			},
		}
	})
	v := testVessel(func(v *rikamiv1.Vessel) { v.Spec.UseProfileScheduling = true })
	podSpec := NewServer(v, profile, testServer()).Deployment.Spec.Template.Spec

	if podSpec.NodeSelector[testNodeLabel] != testNodePool {
		t.Errorf("nodeSelector = %v, want pool=apps", podSpec.NodeSelector)
	}
	if len(podSpec.Tolerations) != 1 || *podSpec.Tolerations[0].Key != "dedicated" {
		t.Errorf("tolerations = %+v, want one with key dedicated", podSpec.Tolerations)
	}
	if len(podSpec.TopologySpreadConstraints) != 2 {
		t.Fatalf("expected 2 spread constraints, got %d", len(podSpec.TopologySpreadConstraints))
	}

	t.Run("missing labelSelector gets the server's labels", func(t *testing.T) {
		got := podSpec.TopologySpreadConstraints[0].LabelSelector
		want := map[string]string{vesselLabel: "shop", serverLabel: testServerName}
		if got == nil || !maps.Equal(got.MatchLabels, want) {
			t.Errorf("labelSelector = %+v, want matchLabels %v", got, want)
		}
	})

	t.Run("explicit labelSelector is left alone", func(t *testing.T) {
		got := podSpec.TopologySpreadConstraints[1].LabelSelector
		if got == nil || got.MatchLabels["custom"] != "spread" || len(got.MatchLabels) != 1 {
			t.Errorf("labelSelector = %+v, want only custom=selector", got)
		}
	})

	t.Run("profile object is not mutated", func(t *testing.T) {
		if profile.Spec.Scheduling.TopologySpreadConstraints[0].LabelSelector != nil {
			t.Error("Build() wrote a labelSelector back into the Profile")
		}
	})
}

func TestBuildSchedulingWorkloadOverride(t *testing.T) {
	profile := testProfile(func(p *rikamiv1.Profile) {
		p.Spec.Scheduling = &rikamiv1.Scheduling{NodeSelector: map[string]string{testNodeLabel: "profile"}}
	})
	v := testVessel(func(v *rikamiv1.Vessel) { v.Spec.UseProfileScheduling = true })
	s := testServer(func(s *rikamiv1.VesselServer) {
		s.Scheduling = &rikamiv1.Scheduling{NodeSelector: map[string]string{testNodeLabel: "workload"}}
	})

	podSpec := NewServer(v, profile, s).Deployment.Spec.Template.Spec
	if podSpec.NodeSelector[testNodeLabel] != "workload" {
		t.Errorf("nodeSelector = %v, want the workload's block to replace the profile's", podSpec.NodeSelector)
	}
}

// ---------------------------------------------------------------------------
// Autoscaling
// ---------------------------------------------------------------------------

func TestBuildAutoscaling(t *testing.T) {
	t.Run("no HPA when autoscaling is not configured", func(t *testing.T) {
		r := NewServer(testVessel(), testProfile(), testServer())
		if r.HPA != nil {
			t.Error("expected no HPA")
		}
	})

	t.Run("HPA from profile", func(t *testing.T) {
		p := testProfile(func(p *rikamiv1.Profile) {
			p.Spec.Autoscaling = &rikamiv1.Autoscaling{
				MinReplicas:          ptr.To[int32](2),
				MaxReplicas:          4,
				TargetCPUUtilization: ptr.To[int32](75),
			}
		})
		v := testVessel(func(v *rikamiv1.Vessel) { v.Spec.UseProfileAutoscaling = true })
		r := NewServer(v, p, testServer())

		if r.HPA == nil {
			t.Fatal("expected an HPA")
		}
		spec := r.HPA.Spec
		if *spec.MinReplicas != 2 || *spec.MaxReplicas != 4 {
			t.Errorf("replicas = %d..%d, want 2..4", *spec.MinReplicas, *spec.MaxReplicas)
		}
		if *spec.ScaleTargetRef.Name != testServerName || *spec.ScaleTargetRef.Kind != "Deployment" {
			t.Errorf("scaleTargetRef = %+v, want Deployment api", spec.ScaleTargetRef)
		}
		if len(spec.Metrics) != 1 {
			t.Fatalf("expected 1 metric, got %d", len(spec.Metrics))
		}
		m := spec.Metrics[0].Resource
		if *m.Name != corev1.ResourceCPU || *m.Target.AverageUtilization != 75 {
			t.Errorf("metric = %s at %d%%, want cpu at 75%%", *m.Name, *m.Target.AverageUtilization)
		}
		if len(r.HPA.OwnerReferences) != 1 {
			t.Error("HPA is missing its ownerReference")
		}
	})

	t.Run("deployment never sets replicas", func(t *testing.T) {
		s := testServer(func(s *rikamiv1.VesselServer) {
			s.Autoscaling = &rikamiv1.Autoscaling{MaxReplicas: 3}
		})
		r := NewServer(testVessel(), testProfile(), s)
		if r.Deployment.Spec.Replicas != nil {
			t.Error("Deployment sets spec.replicas, which would fight the HPA")
		}
	})
}

// ---------------------------------------------------------------------------
// Networking: Service, HTTPRoute, ServiceMonitor
// ---------------------------------------------------------------------------

func TestBuildServer(t *testing.T) {
	r := NewServer(testVessel(), testProfile(), testServer())

	t.Run("service has an ownerReference", func(t *testing.T) {
		refs := r.Service.OwnerReferences
		if len(refs) != 1 || *refs[0].Kind != "Vessel" || string(*refs[0].UID) != "vessel-uid" {
			t.Errorf("service ownerReferences = %+v, want the Vessel", refs)
		}
	})

	t.Run("service exposes the profile's default port", func(t *testing.T) {
		if got := *r.Service.Spec.Ports[0].Port; got != 8080 {
			t.Errorf("service port = %d, want 8080", got)
		}
	})

	t.Run("HTTPRoute targets the service port", func(t *testing.T) {
		if r.HTTPRoute == nil {
			t.Fatal("expected an HTTPRoute for a server")
		}
		port := r.HTTPRoute.Spec.Rules[0].BackendRefs[0].Port
		if port == nil || *port != 8080 {
			t.Errorf("backend port = %v, want 8080", port)
		}
	})
}

func TestBuildService(t *testing.T) {
	svc := &rikamiv1.VesselService{VesselWorkload: rikamiv1.VesselWorkload{Name: "worker", Image: "x", Port: 9090}}
	r := NewService(testVessel(), testProfile(), svc)

	if r.HTTPRoute != nil {
		t.Error("in-cluster services must not get an HTTPRoute")
	}
	if got := *r.Service.Spec.Ports[0].Port; got != 9090 {
		t.Errorf("service port = %d, want the service's own port 9090", got)
	}
}

func TestBuildHostname(t *testing.T) {
	tests := []struct {
		name       string
		rootDomain bool
		namespaced bool
		want       string
	}{
		{name: "default", want: "shop.example.com"},
		{name: "namespaced subdomain", namespaced: true, want: "shop.staging.example.com"},
		{name: "root domain wins", rootDomain: true, namespaced: true, want: "example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testProfile(func(p *rikamiv1.Profile) { p.Spec.NamespacedSubdomain = tt.namespaced })
			s := testServer(func(s *rikamiv1.VesselServer) { s.RootDomain = tt.rootDomain })

			got := NewServer(testVessel(), p, s).HTTPRoute.Spec.Hostnames
			if len(got) != 1 || string(got[0]) != tt.want {
				t.Errorf("hostnames = %v, want [%s]", got, tt.want)
			}
		})
	}
}

func TestBuildMetrics(t *testing.T) {
	t.Run("no metrics means no ServiceMonitor or metrics port", func(t *testing.T) {
		r := NewServer(testVessel(), testProfile(), testServer())
		if r.ServiceMonitor != nil {
			t.Error("expected no ServiceMonitor")
		}
		if len(r.Service.Spec.Ports) != 1 {
			t.Errorf("expected only the entry port, got %d ports", len(r.Service.Spec.Ports))
		}
	})

	t.Run("profile metrics add a port and a ServiceMonitor", func(t *testing.T) {
		p := testProfile(func(p *rikamiv1.Profile) {
			p.Spec.Metrics = &rikamiv1.Metrics{Endpoint: "/metrics", Port: 9000, Interval: "30s"}
		})
		s := testServer(func(s *rikamiv1.VesselServer) { s.UseProfileMetrics = true })
		r := NewServer(testVessel(), p, s)

		if r.ServiceMonitor == nil {
			t.Fatal("expected a ServiceMonitor")
		}
		spec := r.ServiceMonitor.Object["spec"].(map[string]any)
		ep := spec["endpoints"].([]monitoringv1.Endpoint)[0]
		if ep.Path != "/metrics" || ep.Port != metricsPortName {
			t.Errorf("endpoint = %+v, want /metrics on port %q", ep, metricsPortName)
		}

		ports := r.Service.Spec.Ports
		if len(ports) != 2 || *ports[1].Port != 9000 {
			t.Errorf("service ports = %+v, want entry + metrics:9000", ports)
		}
		if !slices.ContainsFunc(container(t, r).Ports, func(p corev1ac.ContainerPortApplyConfiguration) bool {
			return *p.Name == metricsPortName && *p.ContainerPort == 9000
		}) {
			t.Error("container is missing the metrics port")
		}
	})
}

// ---------------------------------------------------------------------------
// ExternalSecrets and databases
// ---------------------------------------------------------------------------

func TestBuildExternalSecrets(t *testing.T) {
	s := testServer(func(s *rikamiv1.VesselServer) {
		s.ExternalSecrets = []rikamiv1.ExternalSecret{
			{Name: "keys", Data: []rikamiv1.ExternalSecretData{{SecretKey: "API_KEY", Key: "/app", Property: "api_key"}}},
			{
				Name:           "everything",
				Extract:        ptr.To("/app/all"),
				SecretStoreRef: &esv1.SecretStoreRef{Name: "own-store", Kind: "SecretStore"},
			},
		}
	})
	r := NewServer(testVessel(), testProfile(), s)

	if len(r.ExternalSecrets) != 2 {
		t.Fatalf("expected 2 ExternalSecrets, got %d", len(r.ExternalSecrets))
	}

	t.Run("data secret uses profile defaults", func(t *testing.T) {
		spec := r.ExternalSecrets[0].Object["spec"].(map[string]any)
		if got := spec["secretStoreRef"].(*esv1.SecretStoreRef).Name; got != "profile-store" {
			t.Errorf("secretStoreRef = %s, want profile-store", got)
		}
		data := spec["data"].([]esv1.ExternalSecretData)
		if len(data) != 1 || data[0].SecretKey != "API_KEY" || data[0].RemoteRef.Property != "api_key" {
			t.Errorf("data = %+v", data)
		}
		if _, ok := spec["dataFrom"]; ok {
			t.Error("data secret must not set dataFrom")
		}
	})

	t.Run("extract secret uses dataFrom and its own store", func(t *testing.T) {
		spec := r.ExternalSecrets[1].Object["spec"].(map[string]any)
		if got := spec["secretStoreRef"].(*esv1.SecretStoreRef).Name; got != "own-store" {
			t.Errorf("secretStoreRef = %s, want own-store", got)
		}
		from := spec["dataFrom"].([]esv1.ExternalSecretDataFromRemoteRef)
		if len(from) != 1 || from[0].Extract.Key != "/app/all" {
			t.Errorf("dataFrom = %+v", from)
		}
	})

	t.Run("container loads both secrets", func(t *testing.T) {
		envFrom := container(t, r).EnvFrom
		names := make([]string, 0, len(envFrom))
		for _, e := range envFrom {
			names = append(names, *e.SecretRef.Name)
		}
		if !slices.Contains(names, "keys") || !slices.Contains(names, "everything") {
			t.Errorf("envFrom = %v, want keys and everything", names)
		}
	})
}

func TestBuildDatabases(t *testing.T) {
	s := testServer(func(s *rikamiv1.VesselServer) {
		s.Databases = []rikamiv1.Database{
			{Name: "orders", Schema: "CREATE TABLE t (id int);"},
			{Name: "users", Schema: "CREATE TABLE u (id int);", Seed: ptr.To("INSERT INTO u VALUES (1);")},
		}
	})
	r := NewServer(testVessel(), testProfile(), s)

	if len(r.Databases) != 2 {
		t.Fatalf("expected 2 databases, got %d", len(r.Databases))
	}
	orders := r.Databases[0]

	t.Run("secret and schema names", func(t *testing.T) {
		if got := orders.AppSecret.GetName(); got != "orders-app" {
			t.Errorf("app secret = %s, want orders-app", got)
		}
		if got := orders.MigratorSecret.GetName(); got != "orders-migrator" {
			t.Errorf("migrator secret = %s, want orders-migrator", got)
		}
		if got := orders.AtlasSchema.GetName(); got != "orders" {
			t.Errorf("atlas schema = %s, want orders", got)
		}
	})

	t.Run("app secret maps prefixed env keys to the remote path", func(t *testing.T) {
		data := orders.AppSecret.Object["spec"].(map[string]any)["data"].([]esv1.ExternalSecretData)
		for _, d := range data {
			if d.RemoteRef.Key != "/db/orders/app" {
				t.Errorf("remote key = %s, want /db/orders/app", d.RemoteRef.Key)
			}
			if d.SecretKey != "db_"+d.RemoteRef.Property {
				t.Errorf("secretKey = %s, want db_%s", d.SecretKey, d.RemoteRef.Property)
			}
		}
	})

	t.Run("seed job only when seed is set", func(t *testing.T) {
		if orders.SeedJob != nil {
			t.Error("orders has no seed but got a seed job")
		}
		job := r.Databases[1].SeedJob
		if job == nil {
			t.Fatal("users has a seed but no seed job")
		}
		args := job.Spec.Template.Spec.Containers[0].Args
		if i := slices.Index(args, "-d"); i < 0 || args[i+1] != "users" {
			t.Errorf("psql args = %v, want -d users", args)
		}
		if len(job.OwnerReferences) != 1 {
			t.Error("seed job is missing its ownerReference")
		}
	})

	t.Run("container loads the app secrets", func(t *testing.T) {
		envFrom := container(t, r).EnvFrom
		names := make([]string, 0, len(envFrom))
		for _, e := range envFrom {
			names = append(names, *e.SecretRef.Name)
		}
		if !slices.Contains(names, "orders-app") || !slices.Contains(names, "users-app") {
			t.Errorf("envFrom = %v, want orders-app and users-app", names)
		}
	})
}

// ---------------------------------------------------------------------------
// Pruning bookkeeping
// ---------------------------------------------------------------------------

func TestAppliedSet(t *testing.T) {
	s := appliedSet{}
	s.add("apps/v1", "Deployment", testServerName)
	s.add("v1", "Service", testServerName)

	deploy := schema.GroupKind{Group: "apps", Kind: "Deployment"}
	svc := schema.GroupKind{Group: "", Kind: "Service"}

	if !s.has(deploy, testServerName) || !s.has(svc, testServerName) {
		t.Error("expected both applied objects to be found")
	}
	if s.has(deploy, "other") {
		t.Error("unexpected match on a different name")
	}
	if s.has(schema.GroupKind{Group: "batch", Kind: "Job"}, testServerName) {
		t.Error("unexpected match on a different kind with the same name")
	}
}
