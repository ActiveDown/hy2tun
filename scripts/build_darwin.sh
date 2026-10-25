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

# Ensure AppIcon.icns exists
if [ ! -f "assets/AppIcon.icns" ]; then
    echo "Generating icons..."
    go run ./tools/genicon
fi

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

# Build macOS .app Bundle
APP_BUNDLE="${OUT_DIR}/Hy2Tun.app"
echo "Creating ${APP_BUNDLE}..."
rm -rf "${APP_BUNDLE}"
mkdir -p "${APP_BUNDLE}/Contents/MacOS"
mkdir -p "${APP_BUNDLE}/Contents/Resources"
cp "${UNIVERSAL_BIN}" "${APP_BUNDLE}/Contents/MacOS/hy2tun"
if [ -f "assets/AppIcon.icns" ]; then
    cp "assets/AppIcon.icns" "${APP_BUNDLE}/Contents/Resources/AppIcon.icns"
fi

cat << 'PLIST' > "${APP_BUNDLE}/Contents/Info.plist"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>Hy2Tun</string>
    <key>CFBundleDisplayName</key>
    <string>Hy2Tun</string>
    <key>CFBundleIdentifier</key>
    <string>com.activedown.hy2tun</string>
    <key>CFBundleVersion</key>
    <string>VERSION</string>
    <key>CFBundleShortVersionString</key>
    <string>VERSION</string>
    <key>CFBundleExecutable</key>
    <string>hy2tun</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleIconFile</key>
    <string>AppIcon</string>
    <key>LSMinimumSystemVersion</key>
    <string>11.0</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSRequiresAquaSystemAppearance</key>
    <false/>
    <key>NSAppTransportSecurity</key>
    <dict>
        <key>NSAllowsArbitraryLoads</key>
        <true/>
        <key>NSAllowsLocalNetworking</key>
        <true/>
    </dict>
</dict>
</plist>
PLIST
sed -i '' "s/VERSION/${VERSION}/g" "${APP_BUNDLE}/Contents/Info.plist"

# Copy Hy2Tun.app to repository root for easy double-clicking in Finder
rm -rf "${ROOT_DIR}/Hy2Tun.app"
cp -R "${APP_BUNDLE}" "${ROOT_DIR}/Hy2Tun.app"
cp "${UNIVERSAL_BIN}" "${ROOT_DIR}/hy2tun"

# Package artifacts
TARBALL="${OUT_DIR}/Hy2Tun-${VERSION}-macos-universal.tar.gz"
echo "Packaging ${TARBALL}..."
tar -C "${OUT_DIR}" -czf "${TARBALL}" hy2tun

ZIP_APP="${OUT_DIR}/Hy2Tun-${VERSION}-macos-app.zip"
echo "Packaging ${ZIP_APP}..."
(cd "${OUT_DIR}" && zip -r -q "Hy2Tun-${VERSION}-macos-app.zip" "Hy2Tun.app")

echo "Calculating SHA256..."
if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "${TARBALL}" > "${TARBALL}.sha256"
    shasum -a 256 "${ZIP_APP}" > "${ZIP_APP}.sha256"
fi

echo "Done! Artifacts created in ${OUT_DIR}/ and root directory:"
ls -lh "${OUT_DIR}/"
