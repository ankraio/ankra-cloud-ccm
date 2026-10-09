# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).

## Unreleased

### Fixed

- `--version` prints `ankra-cloud-ccm <release>` and `--version=raw` adds the embedded k8s.io/cloud-provider version,
  instead of the Kubernetes placeholder `v0.0.0-master+$Format:%H$`.

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
