#!/bin/sh
# Builds the multi-arch (linux/amd64, linux/arm64) controller image without a container daemon: ko cross-compiles the
# controller onto gcr.io/distroless/static-debian12:nonroot (.ko.yaml) and attaches an SPDX SBOM.
#
#   IMAGE    the image repository, for example share.ankra.cloud/library/ankra-cloud-ccm
#   TAGS     space-separated immutable tags, for example "v0.1.0" or "sha-1a2b3c4"
#   VERSION  the version the binary reports (default: the first tag)
#   PUSH     true to publish; anything else builds the image and discards it
#
# Registry credentials come from the Docker config ($DOCKER_CONFIG/config.json or ~/.docker/config.json).
set -eu

: "${IMAGE:?set IMAGE}"
: "${TAGS:?set TAGS}"
PUSH="${PUSH:-false}"
first_tag="${TAGS%% *}"
VERSION="${VERSION:-${first_tag}}"
export VERSION

for tag in ${TAGS}; do
	case "${tag}" in
	latest) echo "image.sh: refusing the moving tag latest" >&2; exit 1 ;;
	esac
done

work_directory="$(mktemp -d)"
trap 'rm -rf "${work_directory}"' EXIT
platforms="linux/amd64,linux/arm64"
ko_tags="$(printf '%s' "${TAGS}" | tr ' ' ',')"

if [ "${PUSH}" = "true" ]; then
	if command -v crane >/dev/null 2>&1; then
		for tag in ${TAGS}; do
			if crane manifest "${IMAGE}:${tag}" >/dev/null 2>&1; then
				echo "image.sh: ${IMAGE}:${tag} is already published and tags are immutable" >&2
				exit 1
			fi
		done
	fi
	KO_DOCKER_REPO="${IMAGE}" \
		ko build ./cmd/ankra-cloud-ccm --bare --platform="${platforms}" --tags="${ko_tags}" --sbom=spdx \
		--image-refs "${work_directory}/image-refs"
	echo "image: $(tail -n 1 "${work_directory}/image-refs")"
else
	KO_DOCKER_REPO="ko.local" \
		ko build ./cmd/ankra-cloud-ccm --bare --platform="${platforms}" --tags="${ko_tags}" --push=false
	echo "built ${IMAGE}:${first_tag} without pushing"
fi
