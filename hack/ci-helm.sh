#!/bin/sh
# Installs a pinned, checksum-verified helm into .ci-tools/helm for pipeline steps, which run as an unprivileged user
# with a read-only root file system and cannot use a package manager. Prints the directory to put on PATH.
set -eu

version="v3.19.0"
case "$(uname -m)" in
x86_64) architecture="amd64" ;;
aarch64 | arm64) architecture="arm64" ;;
*) echo "ci-helm.sh: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
directory="$(pwd)/.ci-tools/helm"
if [ ! -x "${directory}/helm" ]; then
	archive="helm-${version}-linux-${architecture}.tar.gz"
	download_directory="$(mktemp -d)"
	curl -fsSL -o "${download_directory}/${archive}" "https://get.helm.sh/${archive}"
	curl -fsSL -o "${download_directory}/${archive}.sha256sum" "https://get.helm.sh/${archive}.sha256sum"
	(cd "${download_directory}" && sha256sum -c "${archive}.sha256sum" >&2)
	mkdir -p "${directory}"
	tar -xzf "${download_directory}/${archive}" -C "${directory}" --strip-components=1 "linux-${architecture}/helm"
	rm -rf "${download_directory}"
fi
echo "${directory}"
