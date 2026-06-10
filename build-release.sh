#!/usr/bin/env bash
set -euo pipefail

VERSION="${1:-}"
if [[ -z "${VERSION}" ]]; then
  echo "usage: ./build-release.sh <version>" >&2
  exit 2
fi

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
OUT_DIR="${ROOT_DIR}/dist/${VERSION}"

rm -rf "${OUT_DIR}"
mkdir -p "${OUT_DIR}"

build_one() {
  local goos="$1"
  local goarch="$2"
  local ext="$3"
  local name="mateway_${goos}_${goarch}${ext}"
  local package_name="mateway_${goos}_${goarch}"
  local staging="${OUT_DIR}/${package_name}"
  echo "[mateway] building ${package_name}"
  mkdir -p "${staging}"
  GOOS="${goos}" GOARCH="${goarch}" go build -o "${staging}/mateway${ext}" ./cmd/mateway
  cp -R assets "${staging}/assets"
  if [[ "${goos}" == "windows" ]]; then
    (cd "${OUT_DIR}" && zip -qr "${package_name}.zip" "${package_name}")
  else
    (cd "${OUT_DIR}" && tar -czf "${package_name}.tar.gz" "${package_name}")
  fi
  rm -rf "${staging}"
}

cd "${ROOT_DIR}"

build_one linux amd64 ""
build_one linux arm64 ""
build_one darwin amd64 ""
build_one darwin arm64 ""
build_one windows amd64 ".exe"

echo "[mateway] release artifacts written to ${OUT_DIR}"
