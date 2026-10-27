#!/usr/bin/env bash
set -euo pipefail

# Builds the Android app: the Go engine as libhy2tun.so (one per ABI, with the
# NDK) and the APK around it (Gradle, android/).
#
#   scripts/build_android.sh [version] [out-dir]   libraries + APK into out-dir
#   scripts/build_android.sh --lib                 libraries only (for Android Studio)
#
# Needs the Android SDK with the NDK (ANDROID_HOME / ANDROID_NDK_HOME, default
# ~/Library/Android/sdk) and a JDK 17+ (default: the one in Android Studio).
# Without android/keystore.properties the APK is the debug build.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

LIB_ONLY=0
if [ "${1:-}" = "--lib" ]; then
    LIB_ONLY=1
    shift
fi
VERSION="${1:-0.8.0}"
OUT_DIR="${2:-dist}"
API=26

SDK="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}"
NDK="${ANDROID_NDK_HOME:-}"
if [ -z "${NDK}" ] && [ -d "${SDK}/ndk" ]; then
    NDK="${SDK}/ndk/$(ls "${SDK}/ndk" | sort -V | tail -1)"
fi
if [ -z "${NDK}" ] || [ ! -d "${NDK}/toolchains/llvm" ]; then
    echo "Android NDK not found: install it in Android Studio (SDK Manager > SDK Tools > NDK) or set ANDROID_NDK_HOME" >&2
    exit 1
fi
TC="$(echo "${NDK}"/toolchains/llvm/prebuilt/*/bin)"
echo "NDK: ${NDK}"

JNI_DIR="android/app/src/main/jniLibs"
LDFLAGS="-s -w -X main.appVersion=${VERSION} '-extldflags=-Wl,-soname,libhy2tun.so -Wl,-z,max-page-size=16384'"

build_lib() { # abi goarch clang [goarm]
    local abi="$1" arch="$2" cc="$3" arm="${4:-}"
    echo "Compiling libhy2tun.so for ${abi}..."
    mkdir -p "${JNI_DIR}/${abi}"
    CGO_ENABLED=1 GOOS=android GOARCH="${arch}" GOARM="${arm}" CC="${TC}/${cc}" \
        go build -buildmode=c-shared -trimpath -ldflags "${LDFLAGS}" -o "${JNI_DIR}/${abi}/libhy2tun.so" .
    rm -f "${JNI_DIR}/${abi}/libhy2tun.h"
}

build_lib arm64-v8a arm64 "aarch64-linux-android${API}-clang"
build_lib armeabi-v7a arm "armv7a-linux-androideabi${API}-clang" 7
build_lib x86_64 amd64 "x86_64-linux-android${API}-clang"

if [ "${LIB_ONLY}" = 1 ]; then
    exit 0
fi

if [ -z "${JAVA_HOME:-}" ] && [ -d "/Applications/Android Studio.app/Contents/jbr/Contents/Home" ]; then
    export JAVA_HOME="/Applications/Android Studio.app/Contents/jbr/Contents/Home"
fi
export ANDROID_HOME="${SDK}"

TASK=assembleDebug
VARIANT=debug
if [ -f android/keystore.properties ]; then
    TASK=assembleRelease
    VARIANT=release
fi
echo "Building the APK (${VARIANT})..."
(cd android && ./gradlew --no-daemon -q "${TASK}" -PversionName="${VERSION}")

mkdir -p "${OUT_DIR}"
APK="${OUT_DIR}/Hy2Tun-${VERSION}-android.apk"
[ "${VARIANT}" = debug ] && APK="${OUT_DIR}/Hy2Tun-${VERSION}-android-debug.apk"
cp "android/app/build/outputs/apk/${VARIANT}/app-${VARIANT}.apk" "${APK}"
(cd "${OUT_DIR}" && shasum -a 256 "$(basename "${APK}")" > "SHA256SUMS-android.txt")
echo "Done: ${APK}"
