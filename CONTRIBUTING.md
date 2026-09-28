# Contributing to ankra-cloud-ccm

Thank you for helping improve the Ankra Cloud cloud controller manager. Bug reports, fixes and documentation improvements are all welcome.

## Reporting a problem

Open an issue with the chart and image version (`helm list -n kube-system`), the Kubernetes version and distribution,
what you expected, what happened, and the relevant logs (`kubectl -n kube-system logs <pod> -c <container>`). Remove
API tokens and other credentials before you paste anything.

Security problems are not reported in issues: see [SECURITY.md](SECURITY.md).

## Making a change

1. Fork the repository and branch from `main`.
2. Make the change with a test. Go identifiers use full words (`requestError`, not `err`), except the idiomatic
   `ctx`, method receivers and `ok`.
3. Run the gates the pipeline runs:

   ```bash
   make vet test
   make lint        # golangci-lint v2
   make helm-lint   # when you touched the chart; run `make manifests` to re-render deploy/
   ```

4. Add a line under `Unreleased` in [CHANGELOG.md](CHANGELOG.md) for anything a user would notice.
5. Open a pull request against `main`. CI runs on Ankra Pipelines and reports one `Ankra pipeline` check.

The API client in `internal/ankraapi` is generated. Do not edit `operations_gen.go` by hand: run `make sync-client`
to regenerate it from the published Ankra Cloud OpenAPI document.

By contributing you agree that your contribution is licensed under the Apache License 2.0.

## Releases

Maintainers release by moving the `Unreleased` entries in CHANGELOG.md under the new version, setting `version` and
`appVersion` in the chart, and pushing a `v<semver>` tag. The pipeline publishes the image with that tag and the chart
to `oci://share.ankra.cloud/charts`; the Helm repository is updated by a pull request to
[ankraio/ankra-charts](https://github.com/ankraio/ankra-charts).

Documentation: <https://cloud.ankra.app/docs/kubernetes-ccm>
