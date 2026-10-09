# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).

## Unreleased

## v0.1.1

### Security

- The controller is built with Go 1.26.9 and `golang.org/x/net` v0.60.0, which fix the `net/http` and HTTP/2
  vulnerabilities govulncheck found linked into v0.1.0: GO-2026-6599, GO-2026-6600, GO-2026-6603, GO-2026-6605,
  GO-2026-6607, GO-2026-6608, GO-2026-6610, GO-2026-6611, GO-2026-6612, GO-2026-6613 and GO-2026-6617.
- The OpenTelemetry modules move to v1.45.0, which fixes GO-2026-6505 in the SDK and the OTLP trace exporters.

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
