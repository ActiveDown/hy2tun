#!/usr/bin/env bash
set -euo pipefail

# Scripts directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

VERSION="${1:-0.6.0}"
OUT_DIR="${2:-dist}"
mkdir -p "${OUT_DIR}"

echo "Building Hy2Tun ${VERSION} for macOS (Universal: arm64 + x86_64)..."

LDFLAGS="-s -w -X main.appVersion=${VERSION}"

TMP_ARM64="$(mktemp /tmp/hy2tun_arm64.XXXXXX)"
TMP_AMD64="$(mktemp /tmp/hy2tun_amd64.XXXXXX)"
trap 'rm -f "${TMP_ARM64}" "${TMP_AMD64}"' EXIT

echo "Compiling darwin/arm64..."
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "${LDFLAGS}" -o "${TMP_ARM64}" .

echo "Compiling darwin/amd64..."
CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "${LDFLAGS}" -o "${TMP_AMD64}" .

UNIVERSAL_BIN="${OUT_DIR}/hy2tun"
echo "Creating universal binary..."
lipo -create -output "${UNIVERSAL_BIN}" "${TMP_ARM64}" "${TMP_AMD64}"
chmod +x "${UNIVERSAL_BIN}"

TARBALL="${OUT_DIR}/Hy2Tun-${VERSION}-macos-universal.tar.gz"
echo "Packaging ${TARBALL}..."
tar -C "${OUT_DIR}" -czf "${TARBALL}" hy2tun

echo "Calculating SHA256..."
if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "${TARBALL}" > "${TARBALL}.sha256"
fi

echo "Done! Artifacts created in ${OUT_DIR}/:"
ls -lh "${OUT_DIR}/"
