# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).

## Unreleased

## v0.1.1

### Fixed

- Services of type LoadBalancer no longer stay pending with "get_zone_capabilities: decode the answer: json: cannot
  unmarshal number into Go struct field zoneCapabilitiesDocument.stage of type string": the zone's `stage` is read
  as the number the API sends, and the compute node count comes from `compute_nodes`.
- A load balancer created without `load-balancer.ankra.cloud/ipv4` no longer gets the priced IPv4 address: the
  controller now always sends `public_ipv4`, which the API defaults to true.
- Load balancers past the first page of `list_load_balancers` are found again: the controller follows `next_cursor`.
- Labels are sent with `create_load_balancer`, so a new load balancer carries them from the start.

### Changed

- `api/openapi.yaml` and the generated client are synced with the current Ankra Cloud OpenAPI document;
  `update_load_balancer`, `replace_load_balancer_members` and `get_zone_capabilities` go through the generated client.

## v0.1.0

First public release.

### Added

- The cloud controller manager for provider `ankracloud`: InstancesV2 maps nodes to servers
  (`ankracloud://<zone>/<server-id>`) and sets their addresses (IPv6 first), instance type, zone and region.
- Services of type LoadBalancer: one Ankra load balancer per Service, or the Service's members on a combined edge
  (`load-balancer.ankra.cloud/edge`), with TCP ports, member swaps on node changes, health-check annotations, opt-in
  IPv4, and single-VM load balancers on zones with one compute node.
- The Helm chart `ankra-cloud-ccm`, published to https://ankraio.github.io/ankra-charts and
  `oci://share.ankra.cloud/charts`, and plain manifests in `deploy/`.
- Multi-arch images (linux/amd64, linux/arm64) at `share.ankra.cloud/library/ankra-cloud-ccm`, distroless and
  non-root, built without a daemon with ko, with an SPDX SBOM.
