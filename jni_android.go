//go:build android && cgo

package main

/*
#cgo LDFLAGS: -llog
#include <jni.h>
#include <stdlib.h>

int hy2_protect(int fd);
char *hy2_establish(void);
void hy2_tun_closing(void);
char *hy2_get_string(JNIEnv *env, jstring s);
char *hy2_get_bytes(JNIEnv *env, jbyteArray a, int *n);
jbyteArray hy2_new_bytes(JNIEnv *env, const void *p, int n);
void hy2_log(const char *msg);
*/
import "C"

import "unsafe"

// Java_com_activedown_hy2tun_Core_nativeCall is Core.nativeCall(method, arg):
// a method name and a JSON argument (UTF-8 bytes) in, the JSON reply out.
//
//export Java_com_activedown_hy2tun_Core_nativeCall
func Java_com_activedown_hy2tun_Core_nativeCall(env *C.JNIEnv, cls C.jclass, method C.jstring, arg C.jbyteArray) C.jbyteArray {
	var m string
	if cm := C.hy2_get_string(env, method); cm != nil {
		m = C.GoString(cm)
		C.free(unsafe.Pointer(cm))
	}
	var n C.int
	var in []byte
	if p := C.hy2_get_bytes(env, arg, &n); p != nil {
		in = C.GoBytes(unsafe.Pointer(p), n)
		C.free(unsafe.Pointer(p))
	}
	out := mobileCall(m, in)
	var ptr unsafe.Pointer
	if len(out) > 0 {
		ptr = unsafe.Pointer(&out[0])
	}
	return C.hy2_new_bytes(env, ptr, C.int(len(out)))
}

func protectFd(fd int) bool { return C.hy2_protect(C.int(fd)) != 0 }

// establishVpn has the app build the VPN interface: "fd:<n>" or "error:<text>".
func establishVpn() string {
	c := C.hy2_establish()
	if c == nil {
		return "error:the app did not answer"
	}
	defer C.free(unsafe.Pointer(c))
	return C.GoString(c)
}

// tunClosing lets the app block the selected apps (kill switch) before the
// tunnel's interface is closed.
func tunClosing() { C.hy2_tun_closing() }

// logcatWriter mirrors the log to logcat (adb logcat -s hy2tun).
type logcatWriter struct{}

func (logcatWriter) Write(p []byte) (int, error) {
	s := C.CString(string(p))
	C.hy2_log(s)
	C.free(unsafe.Pointer(s))
	return len(p), nil
}
