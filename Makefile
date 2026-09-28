# ankra-cloud-ccm. `make check` runs every gate the pipeline (.ankra/pipeline.yaml) runs, except the image build.

CHART := charts/ankra-cloud-ccm
MANIFESTS := deploy/ankra-cloud-ccm.yaml
RENDER := helm template ankra-cloud-ccm $(CHART) --namespace kube-system --set api.existingSecret=ankra-cloud-ccm
IMAGE ?= share.ankra.cloud/library/ankra-cloud-ccm
TAGS ?= sha-$(shell git rev-parse --short=7 HEAD 2>/dev/null || echo dev)
GOLANGCI_LINT ?= golangci-lint

# Where `make sync-client` reads the Ankra Cloud OpenAPI document from. OPENAPI_SPEC, a local file (for example a
# checkout of the Ankra Cloud monorepo's docs/openapi.yaml), wins over OPENAPI_URL.
OPENAPI_URL ?= https://cloud.ankra.app/docs/openapi.yaml
OPENAPI_SPEC ?=

.PHONY: build test vet lint helm-lint render manifests client client-check sync-client image image-push check

build:
	CGO_ENABLED=0 go build -trimpath -o bin/ankra-cloud-ccm ./cmd/ankra-cloud-ccm

# Unit tests against a fake API, a controller test on client-go fakes, and the check that the generated client
# matches api/openapi.yaml.
test:
	go test ./... -count=1
	cd tools/openapigen && go test ./... -count=1

vet:
	go vet ./...
	cd tools/openapigen && go vet ./...

lint:
	$(GOLANGCI_LINT) run ./...

# helm lint, a render, and a check that deploy/ matches the chart.
helm-lint:
	helm lint --strict $(CHART) --set api.token=lint-placeholder
	helm lint --strict $(CHART) --set api.existingSecret=ankra-cloud-ccm
	@rendered="$$(mktemp)"; \
	{ $(MAKE) --no-print-directory -s render; } > "$$rendered"; \
	if ! diff -uB $(MANIFESTS) "$$rendered" >/dev/null; then \
		echo "helm-lint: $(MANIFESTS) is stale; run make manifests" >&2; diff -uB $(MANIFESTS) "$$rendered" >&2; rm -f "$$rendered"; exit 1; \
	fi; \
	rm -f "$$rendered"

# Plain manifests for clusters without Helm: the chart rendered for kube-system with the token in an existing Secret.
render:
	@echo "# Rendered from charts/ankra-cloud-ccm by make manifests; do not edit. Create the Secret kube-system/ankra-cloud-ccm"
	@echo "# with the key token (an Ankra Cloud API token) first. Kubelets must run with --cloud-provider=external."
	@$(RENDER)

manifests:
	mkdir -p deploy
	$(MAKE) --no-print-directory -s render > $(MANIFESTS)

# Regenerates internal/ankraapi/operations_gen.go from api/openapi.yaml.
client:
	cd tools/openapigen && go run . -specification ../../api/openapi.yaml -package ankraapi -output ../../internal/ankraapi/operations_gen.go

client-check: client
	@git diff --exit-code -- internal/ankraapi || { echo "client-check: run make client and commit the result" >&2; exit 1; }

# Refreshes api/openapi.yaml from the published Ankra Cloud OpenAPI document and regenerates the client.
sync-client:
	@if [ -n "$(OPENAPI_SPEC)" ]; then cp "$(OPENAPI_SPEC)" api/openapi.yaml; \
	else curl -fsSL "$(OPENAPI_URL)" -o api/openapi.yaml.download && mv api/openapi.yaml.download api/openapi.yaml; fi
	$(MAKE) client

# Multi-arch image without a container daemon (needs ko; see hack/image.sh). image builds and discards, image-push
# publishes with the credentials in your Docker config.
image:
	IMAGE=$(IMAGE) TAGS="$(TAGS)" PUSH=false ./hack/image.sh

image-push:
	IMAGE=$(IMAGE) TAGS="$(TAGS)" PUSH=true ./hack/image.sh

check: vet test lint helm-lint
