//go:build android && !cgo

package main

// Without cgo there is no JNI bridge; this only lets the Android code be
// type-checked without the NDK (GOOS=android CGO_ENABLED=0 go vet).

func protectFd(fd int) bool { return false }

func establishVpn() string { return "error:no JNI bridge" }

type logcatWriter struct{}

func (logcatWriter) Write(p []byte) (int, error) { return len(p), nil }
