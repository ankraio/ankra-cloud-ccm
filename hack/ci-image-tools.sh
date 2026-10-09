#!/bin/sh
# Installs pinned, checksum-verified ko and crane release binaries into .ci-tools/image for the image stages, which
# run as an unprivileged user with a read-only root file system. Prints the directory to put on PATH.
#
# Building them with `go install` instead pulled their whole module tree (cosign, the cloud SDKs) into the module
# cache and compiled it into the build cache, in the workspace every stage shares: a run whose cache keys all missed
# ran out of space there (ankra-t9c7w.36.14).
set -eu

ko_version="0.19.1"
crane_version="0.22.1"
case "$(uname -m)" in
x86_64)
	architecture="x86_64"
	ko_sha256="635ac6ea3fd376c935fee597fbb29ab2c2449f49ef1655085fe3aa9c25fed7a5"
	crane_sha256="0ab7a1d6932a213aed964ce97666c3077fe691c8606413674a8b3e0b9ec4cda0"
	;;
aarch64 | arm64)
	architecture="arm64"
	ko_sha256="4099b2d1170d3b8a70e049237462efc2dd14d5fa30e9d2e5e108fb4f778cdd3f"
	crane_sha256="898c0cff975f898a33e8c4580bdafb0e7c02c7faa33374e946762f97c4ab7110"
	;;
*) echo "ci-image-tools.sh: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
directory="$(pwd)/.ci-tools/image"

install_tool() {
	name="$1"
	url="$2"
	sha256="$3"
	if [ -x "${directory}/${name}" ]; then
		return
	fi
	download_directory="$(mktemp -d)"
	archive="${download_directory}/${name}.tar.gz"
	curl -fsSL -o "${archive}" "${url}"
	printf '%s  %s\n' "${sha256}" "${archive}" | sha256sum -c - >&2
	mkdir -p "${directory}"
	tar -xzf "${archive}" -C "${directory}" "${name}"
	rm -rf "${download_directory}"
}

install_tool ko \
	"https://github.com/ko-build/ko/releases/download/v${ko_version}/ko_${ko_version}_Linux_${architecture}.tar.gz" \
	"${ko_sha256}"
install_tool crane \
	"https://github.com/google/go-containerregistry/releases/download/v${crane_version}/go-containerregistry_Linux_${architecture}.tar.gz" \
	"${crane_sha256}"
echo "${directory}"
