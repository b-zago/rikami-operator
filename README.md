# rikami-operator

A Kubernetes operator that turns a short application description into everything the app needs to run: Deployment, Service, HTTPRoute, autoscaling, secrets, database schema and monitoring.

Platform defaults live in a **Profile**. Applications are described in a **Vessel**. Change the Profile once and every Vessel that uses it is updated.

```yaml
apiVersion: rikami.zagoapps.com/v1alpha1
kind: Vessel
metadata:
  name: shop
spec:
  useProfileProbes: true
  useProfileResources: true
  useProfileScheduling: true
  useProfileAutoscaling: true
  servers:
    - name: shop-api
      image: ghcr.io/example/shop-api:1.4.0
      port: 8000
      databases:
        - name: shop
          schema: CREATE TABLE IF NOT EXISTS orders (id SERIAL PRIMARY KEY);
```

From these few lines the operator creates a Deployment with probes, resources and scheduling rules from the Profile, a Service, an HTTPRoute at `shop.<domain>`, an HPA, and the database credentials and schema migration.

## Why

Helm renders templates only when you install or upgrade a release. An operator keeps reconciling, which allows two things a chart can't do well:

- **Environment policy in one place.** Staging and production each get a Profile with their own domain, gateway, secret store, resource defaults, probes, scheduling and autoscaling policy. Changing the Profile rolls the change out to every Vessel that uses it, with no re-rendering of individual releases.
- **Clear ownership.** The platform side owns Profiles; application developers write Vessels and only override what is specific to their app.

## Features

- **Profile defaults with overrides:** probes, resources, scheduling and autoscaling can come from the Profile, per Vessel or per workload. The workload's own values always win.
- **Routing** via Gateway API HTTPRoutes, with root-domain and namespaced-subdomain modes.
- **Autoscaling** via HorizontalPodAutoscaler. The operator never sets `spec.replicas` while autoscaling is on, so it doesn't fight the HPA.
- **Scheduling:** node selectors, affinity, tolerations and topology spread. Spread constraints without a `labelSelector` automatically get one that matches the workload's own pods, so Profiles stay generic.
- **Secrets** via External Secrets Operator, injected into the container as environment variables.
- **Databases:** credentials via External Secrets, schema migrations via Atlas Operator, and an optional seed Job.
- **Monitoring:** metrics port and Prometheus ServiceMonitor.
- **In-cluster services:** sidecar-like workloads next to a server, without public routing.
- **Pruning:** removing something from a Vessel deletes the resources it created.
- **Status conditions** (`Available`, `Progressing`, `Degraded`) based on the health of all child resources.

## How it works

- Every child resource is built as a pure function of the Vessel and its Profile, then applied with **server-side apply** under a single field manager.
- Each reconcile records what it applied. Anything labelled as belonging to the Vessel but not applied this time is **pruned**.
- All children carry an **ownerReference** to the Vessel, so deleting the Vessel garbage-collects everything.
- The controller watches Profiles and re-reconciles every Vessel that references a changed Profile.
- Child health is computed with [kstatus](https://github.com/kubernetes-sigs/cli-utils/tree/master/pkg/kstatus) and summarised into the Vessel's conditions.

Built with Go and [Kubebuilder](https://book.kubebuilder.io). CI runs lint, unit tests and envtest integration tests, then publishes the controller image and a Helm chart to GHCR with semantic versioning (`-dev` prereleases from the `dev` branch).

## Installation

**Prerequisites** in the cluster:

- [Gateway API](https://gateway-api.sigs.k8s.io) CRDs and a Gateway
- [External Secrets Operator](https://external-secrets.io) with a SecretStore or ClusterSecretStore
- [Atlas Operator](https://atlasgo.io/integrations/kubernetes/operator)
- [Prometheus Operator](https://prometheus-operator.dev) CRDs (for ServiceMonitor)
- [metrics-server](https://github.com/kubernetes-sigs/metrics-server) (for autoscaling)

**Install with Helm:**

```sh
helm install rikami oci://ghcr.io/b-zago/charts/rikami --version <version> \
  --namespace rikami-system --create-namespace
```

Available versions are listed under the repository's [packages](https://github.com/b-zago?tab=packages&repo_name=rikami-operator).

**Try the samples** (adjust the domain, gateway and secret store first):

```sh
kubectl apply -k config/samples/
kubectl get vessels
```

## Roadmap

- **Ordered rollout:** apply resources in dependency order (secrets and schema before the Deployment) and wait for each step to be healthy.
- **Better status reporting and Kubernetes Events,** so `kubectl describe vessel` explains exactly which resource is unhealthy and why.
- **Automatic restarts on secret changes:** when an ExternalSecret's data changes, roll the Deployment so pods pick up the new values. Likewise, re-run the seed Job when the seed changes.
- **Consistent metrics toggle:** `useProfileMetrics` currently exists only per workload. It will get a Vessel-wide flag like the other settings.

### Known limitations

- Every server in a Vessel gets the same hostname (derived from the Vessel name). In practice, use one public server per Vessel and put the rest under `services`.
- The container inside each Deployment is named after the Vessel rather than the server.

---

## Reference

### Profile

A Profile is namespaced. A Vessel uses the Profile with the name in `spec.profile` (default: `default`) from its own namespace.

| Field                                             | Required | Description                                                                                                                 |
| ------------------------------------------------- | -------- | --------------------------------------------------------------------------------------------------------------------------- |
| `gateway`                                         | yes      | Name of the Gateway that HTTPRoutes attach to.                                                                              |
| `domain`                                          | yes      | Base domain for hostnames.                                                                                                  |
| `namespacedSubdomain`                             | no       | `true` gives `<vessel>.<namespace>.<domain>` instead of `<vessel>.<domain>`. Default `false`.                               |
| `defaultServicePort`                              | no       | Port the Service of a server exposes. Default `80`.                                                                         |
| `pullPolicy`                                      | yes      | `Always`, `IfNotPresent` or `Never`.                                                                                        |
| `pullSecret`                                      | no       | Image pull secret added to every pod.                                                                                       |
| `externalSecretsConfig`                           | yes      | Defaults for ExternalSecrets: `secretStoreRef`, `refreshPolicy` (`CreatedOnce`, `Periodic`, `OnChange`), `refreshInterval`. |
| `databaseConfig`                                  | yes      | Where database credentials live. See [Databases](#databases).                                                               |
| `startupProbe`, `readinessProbe`, `livenessProbe` | no       | Standard Kubernetes probes, used by workloads with `useProfileProbes`.                                                      |
| `resources`                                       | no       | Standard resource requests/limits, used with `useProfileResources`.                                                         |
| `scheduling`                                      | no       | See [Scheduling](#scheduling). Used with `useProfileScheduling`.                                                            |
| `autoscaling`                                     | no       | See [Autoscaling](#autoscaling). Used with `useProfileAutoscaling`.                                                         |
| `metrics`                                         | no       | See [Metrics](#metrics). Used with `useProfileMetrics`.                                                                     |

### Vessel

| Field                                                                                      | Required | Description                                                      |
| ------------------------------------------------------------------------------------------ | -------- | ---------------------------------------------------------------- |
| `profile`                                                                                  | no       | Profile name in the same namespace. Default `default`.           |
| `servers`                                                                                  | no       | Up to 16 servers. Each gets a Deployment, Service and HTTPRoute. |
| `useProfileProbes`, `useProfileResources`, `useProfileScheduling`, `useProfileAutoscaling` | no       | Vessel-wide defaults for the profile toggles. Default `false`.   |

### Servers and services

A **server** is a publicly routed workload. A server can have up to 16 **services**: workloads with the same capabilities but no HTTPRoute, for in-cluster use. Service names must differ from their server's name.

| Field                                                                                      | Required | Description                                                                             |
| ------------------------------------------------------------------------------------------ | -------- | --------------------------------------------------------------------------------------- |
| `name`                                                                                     | yes      | Name of the Deployment, Service, HPA and HTTPRoute. 1–63 characters.                    |
| `image`                                                                                    | yes      | Container image.                                                                        |
| `port`                                                                                     | yes      | Container port, exposed as the named port `entry`. For services, also the Service port. |
| `rootDomain`                                                                               | no       | Servers only. Route the bare `domain` instead of a subdomain. Default `false`.          |
| `services`                                                                                 | no       | Servers only. In-cluster workloads, same fields as a server.                            |
| `startupProbe`, `readinessProbe`, `livenessProbe`                                          | no       | Standard Kubernetes probes.                                                             |
| `resources`                                                                                | no       | Standard resource requests/limits.                                                      |
| `scheduling`                                                                               | no       | See [Scheduling](#scheduling).                                                          |
| `autoscaling`                                                                              | no       | See [Autoscaling](#autoscaling).                                                        |
| `metrics`                                                                                  | no       | See [Metrics](#metrics).                                                                |
| `envs`                                                                                     | no       | Map of plain environment variables.                                                     |
| `envSecretRefs`                                                                            | no       | Existing Secrets to load as environment variables.                                      |
| `externalSecrets`                                                                          | no       | See [External secrets](#external-secrets).                                              |
| `databases`                                                                                | no       | See [Databases](#databases).                                                            |
| `useProfileProbes`, `useProfileResources`, `useProfileScheduling`, `useProfileAutoscaling` | no       | Override the Vessel-wide toggle for this workload.                                      |
| `useProfileMetrics`                                                                        | no       | Use the Profile's metrics instead of this workload's. Default `false`.                  |

### How Profile values are resolved

For each setting, the workload's `useProfileX` wins; if it's unset, the Vessel's `useProfileX` applies.

When the Profile is in use:

- **Probes** are resolved one by one: each probe set on the workload wins, the others come from the Profile.
- **Resources, scheduling and autoscaling** are resolved as a whole block: if the workload defines the block, it replaces the Profile's block entirely.

When the Profile is not in use, only the workload's own values apply.

### Scheduling

| Field                       | Description                                                                                           |
| --------------------------- | ----------------------------------------------------------------------------------------------------- |
| `nodeSelector`              | Map of node labels.                                                                                   |
| `affinity`                  | Standard Kubernetes affinity.                                                                         |
| `tolerations`               | Standard Kubernetes tolerations.                                                                      |
| `topologySpreadConstraints` | Standard constraints. If `labelSelector` is omitted, the operator fills in the workload's own labels. |

### Autoscaling

Creates a HorizontalPodAutoscaler targeting the workload's Deployment. Utilization targets are percentages of the container's **requests**, so set resources when using them.

| Field                     | Required | Description                                                                                                 |
| ------------------------- | -------- | ----------------------------------------------------------------------------------------------------------- |
| `minReplicas`             | no       | Minimum replicas, at least 1. Must not exceed `maxReplicas`.                                                |
| `maxReplicas`             | yes      | Maximum replicas.                                                                                           |
| `targetCPUUtilization`    | no       | Target average CPU, in percent of requests. Defaults to 80% CPU if no target is set.                        |
| `targetMemoryUtilization` | no       | Target average memory, in percent of requests. Only useful for apps whose memory drops when load is spread. |

### Metrics

Adds a container and Service port named `metrics` and a ServiceMonitor that scrapes it.

| Field      | Required | Description                          |
| ---------- | -------- | ------------------------------------ |
| `endpoint` | yes      | Path, e.g. `/metrics`.               |
| `port`     | yes      | Container port serving metrics.      |
| `interval` | yes      | Scrape interval, e.g. `30s` or `1m`. |

### External secrets

Each entry creates an ExternalSecret (and therefore a Secret) with the given name and loads it into the container as environment variables. Set exactly one of `data` or `extract`.

| Field                                                | Required | Description                                                                         |
| ---------------------------------------------------- | -------- | ----------------------------------------------------------------------------------- |
| `name`                                               | yes      | Name of the ExternalSecret and the resulting Secret.                                |
| `data`                                               | one of   | List of `{secretKey, key, property}`: env var name, remote key, property within it. |
| `extract`                                            | one of   | Remote key whose properties all become env vars.                                    |
| `secretStoreRef`, `refreshPolicy`, `refreshInterval` | no       | Override the Profile's `externalSecretsConfig`.                                     |

### Databases

Each database entry creates:

- an ExternalSecret `<name>-app` with the app's credentials, loaded into the container as `<envKeysPrefix><key>` env vars (e.g. `db_host`, `db_username`, `db_password`),
- an ExternalSecret `<name>-migrator` with the connection URL used for migrations,
- an Atlas `AtlasSchema` `<name>` that applies `schema`,
- if `seed` is set, a Job `<name>` that runs the seed SQL with `psql`.

| Field    | Required | Description                                                |
| -------- | -------- | ---------------------------------------------------------- |
| `name`   | yes      | Database name. Replaces `*` in the Profile's secret paths. |
| `schema` | yes      | Desired schema as SQL.                                     |
| `seed`   | no       | SQL to run once after creation.                            |

The Profile's `databaseConfig` says where credentials live in the secret store:

| Field                                             | Description                                                                                  |
| ------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| `appConfig.secretAppPath`                         | Remote path of app credentials, with `*` for the database name, e.g. `/pgsql/staging/*/app`. |
| `appConfig.hostKey`, `usernameKey`, `passwordKey` | Property names within that path.                                                             |
| `migratorConfig.secretMigratorPath`               | Remote path of the migrator's connection URL, with `*` for the database name.                |
| `migratorConfig.secretKey`                        | Property holding the URL.                                                                    |
| `envKeysPrefix`                                   | Prefix for the app's database env vars. Default `db_`.                                       |

### Status

`kubectl get vessel <name> -o yaml` shows three conditions:

| Condition     | Meaning                                                                              |
| ------------- | ------------------------------------------------------------------------------------ |
| `Available`   | All child resources are healthy.                                                     |
| `Progressing` | Some child resources are still rolling out. The operator re-checks every 10 seconds. |
| `Degraded`    | A resource failed to apply, a child is unhealthy, or the Profile is missing.         |

## Development

```sh
make test     # unit + envtest integration tests
make lint     # golangci-lint
make run      # run the controller against the current kubeconfig
```

Requires Go 1.26 and access to a cluster with the prerequisites above for `make run`.
