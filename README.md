# ankra-cloud-ccm

The Kubernetes cloud controller manager for [Ankra Cloud](https://cloud.ankra.app): an out-of-tree
`k8s.io/cloud-provider` with the provider name `ankracloud`. It initialises nodes (providerID, addresses, instance
type, zone and region) and gives every Service of type LoadBalancer an Ankra Cloud load balancer. It talks only to the
public Ankra Cloud API, with an API token.

The controller is in preview. Full documentation: <https://cloud.ankra.app/docs/kubernetes-ccm>.

## Quick start

Every kubelet must run with `--cloud-provider=external` (on k3s: `--disable-cloud-controller` on the servers and
`--kubelet-arg=cloud-provider=external` on every node). New nodes keep the
`node.cloudprovider.kubernetes.io/uninitialized` taint until the controller has initialised them.

```bash
helm repo add ankra https://ankraio.github.io/ankra-charts
helm repo update
helm install ankra-cloud-ccm ankra/ankra-cloud-ccm -n kube-system --set api.token=<token>
```

`<token>` is an Ankra Cloud API token with operate permission (**Settings → API tokens** in the console). For
production, keep the token out of Helm values, name the cluster (it labels every load balancer and must be unique per
account), and put load balancers on your private network:

```bash
kubectl -n kube-system create secret generic ankra-cloud-ccm --from-literal=token=<token>
helm install ankra-cloud-ccm ankra/ankra-cloud-ccm -n kube-system \
  --set api.existingSecret=ankra-cloud-ccm --set clusterName=production --set loadBalancer.networkID=<network-id>
```

The chart is also published as an OCI artifact:

```bash
helm install ankra-cloud-ccm oci://share.ankra.cloud/charts/ankra-cloud-ccm --version 0.1.0 -n kube-system \
  --set api.existingSecret=ankra-cloud-ccm
```

Without Helm, create the Secret above and apply the rendered manifests:

```bash
kubectl apply -f https://raw.githubusercontent.com/ankraio/ankra-cloud-ccm/main/deploy/ankra-cloud-ccm.yaml
```

The chart runs a Deployment in `kube-system` with leader election (`--leader-elect`), the standard cloud controller
manager RBAC, host networking (the controller initialises nodes before any CNI is ready), and tolerations for the
uninitialized, control-plane and not-ready taints.

| Interface | Behaviour |
| --- | --- |
| InstancesV2 | Maps a node to a server by providerID `ankracloud://<zone>/<server-id>`, or by hostname (`list_servers?hostname=`) while the providerID is empty. |
| LoadBalancer | One Ankra load balancer per Service of type LoadBalancer, or the Service's members on a combined edge. |
| Routes, Zones, Clusters, Instances | Not implemented. The CNI routes pod traffic; InstancesV2 reports zone and region. |

## Nodes

- `InstanceExists` is false once the server is gone (404) or `deleting`; `InstanceShutdown` is true while it is
  `stopping` or `stopped`.
- `InstanceMetadata`:
  - Addresses, IPv6 first: `ExternalIP` public IPv6, `ExternalIP` public IPv4 (only with the add-on),
    `InternalIP` for each private leg's ULA address, then for its RFC1918 address, and `Hostname`.
  - `node.kubernetes.io/instance-type` is the plan.
  - `topology.kubernetes.io/zone` is the Ankra zone.
  - `topology.kubernetes.io/region` is the zone's region from `list_zones`.

## Services of type LoadBalancer

- Each load balancer is named `k8s-<cluster>-<namespace>-<name>` and labelled `ccm.ankra.cloud/service=<namespace>/<name>`
  and `ccm.ankra.cloud/cluster=<cluster>`. The labels are how it is found again. The controller also adopts a load
  balancer that has its name but no labels, so a crash between create and label does not leak one.
- Each TCP port becomes a `tcp-<port>` backend and frontend. Other protocols are skipped with an
  `UnsupportedPort` event.
- The members are the nodes' addresses at the port's NodePort, one per node and Service IP family (`spec.ipFamilies`),
  IPv6 first. On a private network the private address (ULA, RFC1918) is used; without one, the public IPv6 is used.
- When nodes change, the member list is swapped in one `replace_load_balancer_members` call per backend.
- Deleting the Service deletes the load balancer. A second delete, or one whose load balancer is already gone,
  succeeds.
- The status ingress lists the IPv6 address, then the IPv4 address if the load balancer has one.
- On a zone with a single compute node, `get_zone_capabilities` reports `load_balancer_ha: false`. The load balancer
  is then created with `high_availability: false`, and the controller logs it and records a `SingleNodeLoadBalancer`
  event on the Service.

| Annotation | Meaning |
| --- | --- |
| `load-balancer.ankra.cloud/ipv4: "true"` | Adds an IPv4 frontend address (the priced IPv4 add-on). Off by default; IPv6 is always on. It is set when the load balancer is created. |
| `load-balancer.ankra.cloud/network-id` | Puts the load balancer on this private network. The default is `loadBalancer.networkID`. It is set when the load balancer is created. |
| `load-balancer.ankra.cloud/edge: "<edge-id>"` | Uses a combined edge's `load_balancer` role instead of a dedicated load balancer. Members are written with `update_edge` and named `k<hash>-…`; members of other Services and members added by hand are kept. |
| `load-balancer.ankra.cloud/zone` | The zone of a new load balancer. The default is `loadBalancer.zone`, then the nodes' zone. |
| `load-balancer.ankra.cloud/health-check-type` | `tcp` (default), `http` or `none`. |
| `load-balancer.ankra.cloud/health-check-path` | Path of the `http` check (default `/`). |
| `load-balancer.ankra.cloud/health-check-expected-status` | Status for `http`, such as `200` or `200-399` (default). |
| `load-balancer.ankra.cloud/health-check-interval` | Seconds between checks, 1-300 (default 2). |
| `load-balancer.ankra.cloud/health-check-rise` / `-fall` | Checks to bring a member back or take it out, 1-10 (defaults 2 and 3). |

## Values

| Value | Default | Description |
| --- | --- | --- |
| `api.url` | `https://cloud.ankra.app` | The Ankra Cloud API endpoint. |
| `api.existingSecret` | `""` | A Secret with the key `token` and optionally `ca.crt`. The chart creates one from `api.token` when empty. |
| `api.token` | `""` | Used only when `api.existingSecret` is empty. Prefer an existing Secret. |
| `api.caBundle` | `""` | PEM certificates to trust in addition to the system roots; used only when `api.existingSecret` is empty. |
| `api.existingSecretHasCABundle` | `false` | Set when `api.existingSecret` also holds `ca.crt`. |
| `clusterName` | `kubernetes` | Written on every load balancer as the `ccm.ankra.cloud/cluster` label; unique per account. |
| `loadBalancer.networkID` | `""` | Default private network for load balancers; the `load-balancer.ankra.cloud/network-id` annotation overrides it. |
| `loadBalancer.zone` | `""` | Default zone for load balancers; otherwise the nodes' zone. |
| `loadBalancer.highAvailability` | `auto` | `auto` asks the zone's capabilities; `true` or `false` forces it. |
| `image.repository` | `share.ankra.cloud/library/ankra-cloud-ccm` | The controller image (linux/amd64, linux/arm64). |
| `image.tag` | the chart's `appVersion` | An immutable tag: `v<semver>` or `sha-<commit>`. |
| `image.pullPolicy` | `IfNotPresent` | |
| `imagePullSecrets` | `[]` | |
| `replicas` | `1` | Two or more run as a leader-elected standby pair. |
| `extraArgs` | `[]` | Additional flags for the controller manager. |
| `logVerbosity` | `2` | klog verbosity. |
| `resources` | see `values.yaml` | |
| `hostNetwork` | `true` | The controller runs before any CNI is ready. |
| `priorityClassName` | `system-cluster-critical` | |
| `nodeSelector`, `affinity`, `tolerations` | see `values.yaml` | Prefers control-plane nodes and tolerates the uninitialized taint. |
| `podSecurityContext`, `securityContext` | non-root, read-only root file system | |

## Configuration

| Environment | From |
| --- | --- |
| `ANKRA_CLOUD_TOKEN` | Secret key `token`: an API token with operate permission. |
| `ANKRA_CLOUD_API_URL` | `api.url` (default `https://cloud.ankra.app`). |
| `ANKRA_CLOUD_CA_BUNDLE` | Path of the Secret key `ca.crt` when set, for an API behind a private CA. |
| `ANKRA_CLOUD_NETWORK_ID`, `ANKRA_CLOUD_ZONE` | Load balancer defaults. |
| `ANKRA_CLOUD_LOAD_BALANCER_HIGH_AVAILABILITY` | `auto` (default: ask the zone), `true` or `false`. |

## API calls

The provider depends on the narrow interface `internal/cloudapi.API`. The implementation calls the generated client
`internal/ankraapi` for these operations:

- `get_server`
- `list_server_interfaces`
- `list_zones`
- `list_load_balancers` and `get_load_balancer`, through `Call`, so the IPv6-first fields `public_ipv6`, `public_ipv4`,
  `labels` and `high_availability` decode next to today's `address`
- `create_load_balancer`
- `delete_load_balancer`
- the backend and frontend operations
- `get_edge` and `update_edge`

`list_servers?hostname=` goes through `Call`, which passes any query parameter. The client-side exact match stays in
place as well.

Some operations are not in the published OpenAPI document yet:

- `update_load_balancer`: `PATCH /v1/load-balancers/{id}` with `{name?, labels?}`
- `replace_load_balancer_members`: `PUT /v1/load-balancers/{id}/backends/{backend}/members` with `{members: [{name, address, port}]}`
- `get_zone_capabilities`: `GET /v1/zones/{zone}/capabilities`. A 404, 405, 401 or 403 answer means "assume HA".

Until they are there, these calls are thin HTTP requests with the same token. Once `make sync-client` generates their
operationIds, they go through the generated client's `Call` automatically. `create_load_balancer` sends
`high_availability` only when it is false and `public_ipv4` only when it is true, so the default body stays the one
today's API accepts.

## Images and releases

Images are multi-arch (linux/amd64, linux/arm64), distroless and non-root, and published to the public registry
`share.ankra.cloud`, pullable without credentials: `share.ankra.cloud/library/ankra-cloud-ccm:v<semver>` for each
release tag and `:sha-<commit>` for each commit on main. Tags are immutable; there is no `latest`. Each image carries
an SPDX SBOM in the registry. The chart is published to the Helm repository `https://ankraio.github.io/ankra-charts`
and to `oci://share.ankra.cloud/charts`.

CI runs on [Ankra Pipelines](.ankra/pipeline.yaml): go vet, the unit tests, golangci-lint, govulncheck and the chart
gates on every push and pull request; the image build on every pull request; publishing on main and on `v*` tags. See
[CHANGELOG.md](CHANGELOG.md) for what each release changed.

## Develop

```bash
make vet test        # go vet, unit tests (fake API), a controller test on client-go fakes, and the API client check
make lint            # golangci-lint v2
make helm-lint       # helm lint, helm template, and a check that deploy/ matches the chart
make manifests       # re-render deploy/
make sync-client     # refresh api/openapi.yaml from https://cloud.ankra.app/docs/openapi.yaml and regenerate the client
make image           # build the multi-arch image without pushing (needs ko)
```

`make sync-client OPENAPI_SPEC=<path>` regenerates from a local copy of the OpenAPI document instead of the published
one. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
