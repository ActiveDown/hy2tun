#!/usr/bin/env bash
# Cross-build unsigned Windows releases from macOS/Linux. For Authenticode
# signing on Windows, use build.ps1 with -Pfx or -Thumbprint instead.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"
VERSION="${1:?Usage: scripts/build_windows.sh VERSION [OUT_DIR] [WINTUN_DLL]}"
OUT_DIR="${2:-dist}"
WINTUN_DLL="${3:-${OUT_DIR}/wintun.dll}"
if [[ ! "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]]; then
    echo 'Invalid release version' >&2
    exit 1
fi
mkdir -p "${OUT_DIR}"
TMP_STAGE="$(mktemp -d "${TMPDIR:-/tmp}/hy2tun_windows.XXXXXX")"
trap 'rm -rf "${TMP_STAGE}"' EXIT

# Keep this DLL hash in sync with scripts/build.ps1.
WINTUN_SHA='e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce'
if [[ ! -f "${WINTUN_DLL}" ]]; then
    curl --proto '=https' --tlsv1.2 -fsSL --retry 2 --connect-timeout 15 --max-time 120 \
        https://www.wintun.net/builds/wintun-0.14.1.zip -o "${TMP_STAGE}/wintun.zip"
    python3 - "${TMP_STAGE}/wintun.zip" "${WINTUN_DLL}" "${WINTUN_SHA}" <<'PY'
import hashlib, pathlib, sys, zipfile
with zipfile.ZipFile(sys.argv[1]) as archive:
    dll = archive.read('wintun/bin/amd64/wintun.dll')
if hashlib.sha256(dll).hexdigest() != sys.argv[3]:
    raise SystemExit('Downloaded wintun.dll hash mismatch')
path = pathlib.Path(sys.argv[2])
path.parent.mkdir(parents=True, exist_ok=True)
path.write_bytes(dll)
PY
fi
python3 - "${WINTUN_DLL}" "${WINTUN_SHA}" <<'PY'
import hashlib, pathlib, sys
if hashlib.sha256(pathlib.Path(sys.argv[1]).read_bytes()).hexdigest() != sys.argv[2]:
    raise SystemExit('wintun.dll hash mismatch')
print('Verified pinned Wintun 0.14.1 amd64 DLL')
PY

export GOOS=windows GOARCH=amd64 CGO_ENABLED=0
LDFLAGS="-s -w -X main.appVersion=${VERSION}"
echo "Building unsigned Windows amd64 release ${VERSION}..."
go build -trimpath -ldflags "${LDFLAGS} -H=windowsgui" -o "${OUT_DIR}/Hy2Tun.exe" .
go build -trimpath -ldflags "${LDFLAGS}" -o "${OUT_DIR}/hy2tun-console.exe" .
go build -trimpath -ldflags "${LDFLAGS} -H=windowsgui" -o "${OUT_DIR}/Uninstall.exe" ./cmd/uninstall

mkdir -p cmd/setup/payload
cp "${OUT_DIR}/Hy2Tun.exe" cmd/setup/payload/Hy2Tun.exe
cp "${OUT_DIR}/Uninstall.exe" cmd/setup/payload/Uninstall.exe
cp "${WINTUN_DLL}" cmd/setup/payload/wintun.dll
SETUP="${OUT_DIR}/Hy2Tun-${VERSION}-setup-windows-amd64.exe"
go build -trimpath -ldflags "${LDFLAGS} -H=windowsgui" -o "${SETUP}" ./cmd/setup

# PE machine/subsystem, build target and release version are checked before packaging.
python3 - "${OUT_DIR}" "${SETUP}" "${VERSION}" <<'PY'
import pathlib, struct, subprocess, sys
out = pathlib.Path(sys.argv[1])
for path, expected in [(out/'Hy2Tun.exe', 2), (out/'hy2tun-console.exe', 3),
                       (out/'Uninstall.exe', 2), (pathlib.Path(sys.argv[2]), 2)]:
    data = path.read_bytes()
    pe, = struct.unpack_from('<I', data, 0x3c)
    if data[:2] != b'MZ' or data[pe:pe+4] != b'PE\0\0':
        raise SystemExit(f'{path.name}: invalid PE executable')
    machine, = struct.unpack_from('<H', data, pe+4)
    subsystem, = struct.unpack_from('<H', data, pe+24+68)
    if machine != 0x8664 or subsystem != expected:
        raise SystemExit(f'{path.name}: wrong architecture or subsystem')
    info = subprocess.check_output(['go', 'version', '-m', str(path)], text=True)
    if 'GOOS=windows' not in info or 'GOARCH=amd64' not in info:
        raise SystemExit(f'{path.name}: unexpected Go build target')
    # Go with -trimpath may omit -ldflags from build info. Verify the release
    # version string is present as well as supplying -X to every build above.
    if sys.argv[3].encode() not in data:
        raise SystemExit(f'{path.name}: missing release version string')
    print(f'{path.name}: Windows amd64, subsystem {subsystem}, version {sys.argv[3]}')
PY

ZIP="${OUT_DIR}/Hy2Tun-${VERSION}-portable-windows-amd64.zip"
python3 - "${ROOT_DIR}" "${OUT_DIR}" "${WINTUN_DLL}" "${ZIP}" "${SETUP}" <<'PY'
import hashlib, pathlib, sys, zipfile
root, out, dll, archive, setup = map(pathlib.Path, sys.argv[1:])
with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    for path in sorted((root/'packaging/portable').iterdir()):
        if path.is_file():
            z.write(path, path.name)
    z.write(out/'hy2tun-console.exe', 'hy2tun.exe')
    z.write(dll, 'wintun.dll')
with zipfile.ZipFile(archive) as z:
    required = {'hy2tun.exe', 'wintun.dll', 'README.txt',
                '1-run-as-admin.bat', '9-emergency-cleanup.bat'}
    if set(z.namelist()) != required or z.testzip() is not None:
        raise SystemExit('Portable archive contents failed validation')
lines = [f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}' for p in [setup, archive]]
(out/'SHA256SUMS-windows.txt').write_text('\n'.join(lines)+'\n', encoding='utf-8')
print('\n'.join(lines))
PY

echo "Done: Windows installer and portable zip in ${OUT_DIR} (unsigned)."
