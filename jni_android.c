// JNI side of the Android library: the bridge between Core.nativeCall in the
// app and mobileCall in Go, and the callbacks into the app (VpnService.protect,
// building the VPN interface).

#include <jni.h>
#include <stdlib.h>
#include <string.h>
#include <android/log.h>

static JavaVM *g_vm;
static jclass g_core;
static jmethodID g_protect;
static jmethodID g_establish;
static jmethodID g_tun_closing;

JNIEXPORT jint JNI_OnLoad(JavaVM *vm, void *reserved) {
	JNIEnv *env;
	g_vm = vm;
	if ((*vm)->GetEnv(vm, (void **)&env, JNI_VERSION_1_6) != JNI_OK)
		return JNI_ERR;
	// Look the class up here: threads started by Go only see the system class loader.
	jclass c = (*env)->FindClass(env, "com/activedown/hy2tun/Core");
	if (c == NULL)
		return JNI_ERR;
	g_core = (*env)->NewGlobalRef(env, c);
	g_protect = (*env)->GetStaticMethodID(env, g_core, "protect", "(I)Z");
	g_establish = (*env)->GetStaticMethodID(env, g_core, "establish", "()Ljava/lang/String;");
	g_tun_closing = (*env)->GetStaticMethodID(env, g_core, "tunClosing", "()V");
	if (g_protect == NULL || g_establish == NULL || g_tun_closing == NULL)
		return JNI_ERR;
	return JNI_VERSION_1_6;
}

// hy2_protect calls Core.protect(fd) from any thread.
int hy2_protect(int fd) {
	JNIEnv *env;
	int attached = 0;
	jint r = (*g_vm)->GetEnv(g_vm, (void **)&env, JNI_VERSION_1_6);
	if (r == JNI_EDETACHED) {
		if ((*g_vm)->AttachCurrentThread(g_vm, &env, NULL) != JNI_OK)
			return 0;
		attached = 1;
	} else if (r != JNI_OK) {
		return 0;
	}
	jboolean ok = (*env)->CallStaticBooleanMethod(env, g_core, g_protect, (jint)fd);
	if ((*env)->ExceptionCheck(env)) {
		(*env)->ExceptionClear(env);
		ok = JNI_FALSE;
	}
	if (attached)
		(*g_vm)->DetachCurrentThread(g_vm);
	return ok == JNI_TRUE;
}

// hy2_establish calls Core.establish() from any thread; the result ("fd:<n>"
// or "error:<text>") is malloc'ed.
char *hy2_establish(void) {
	JNIEnv *env;
	int attached = 0;
	char *out = NULL;
	jint r = (*g_vm)->GetEnv(g_vm, (void **)&env, JNI_VERSION_1_6);
	if (r == JNI_EDETACHED) {
		if ((*g_vm)->AttachCurrentThread(g_vm, &env, NULL) != JNI_OK)
			return NULL;
		attached = 1;
	} else if (r != JNI_OK) {
		return NULL;
	}
	jstring s = (jstring)(*env)->CallStaticObjectMethod(env, g_core, g_establish);
	if ((*env)->ExceptionCheck(env)) {
		(*env)->ExceptionClear(env);
		s = NULL;
	}
	if (s != NULL) {
		const char *c = (*env)->GetStringUTFChars(env, s, NULL);
		if (c != NULL) {
			out = strdup(c);
			(*env)->ReleaseStringUTFChars(env, s, c);
		}
		(*env)->DeleteLocalRef(env, s);
	}
	if (attached)
		(*g_vm)->DetachCurrentThread(g_vm);
	return out;
}

// hy2_tun_closing calls Core.tunClosing() from any thread.
void hy2_tun_closing(void) {
	JNIEnv *env;
	int attached = 0;
	jint r = (*g_vm)->GetEnv(g_vm, (void **)&env, JNI_VERSION_1_6);
	if (r == JNI_EDETACHED) {
		if ((*g_vm)->AttachCurrentThread(g_vm, &env, NULL) != JNI_OK)
			return;
		attached = 1;
	} else if (r != JNI_OK) {
		return;
	}
	(*env)->CallStaticVoidMethod(env, g_core, g_tun_closing);
	if ((*env)->ExceptionCheck(env))
		(*env)->ExceptionClear(env);
	if (attached)
		(*g_vm)->DetachCurrentThread(g_vm);
}

// hy2_get_string copies a Java string into a malloc'ed C string (NULL for null).
char *hy2_get_string(JNIEnv *env, jstring s) {
	if (s == NULL)
		return NULL;
	const char *c = (*env)->GetStringUTFChars(env, s, NULL);
	if (c == NULL)
		return NULL;
	char *out = strdup(c);
	(*env)->ReleaseStringUTFChars(env, s, c);
	return out;
}

// hy2_get_bytes copies a byte[] into a malloc'ed buffer; *n is its length.
char *hy2_get_bytes(JNIEnv *env, jbyteArray a, int *n) {
	*n = 0;
	if (a == NULL)
		return NULL;
	jsize len = (*env)->GetArrayLength(env, a);
	char *buf = malloc(len > 0 ? len : 1);
	if (buf == NULL)
		return NULL;
	(*env)->GetByteArrayRegion(env, a, 0, len, (jbyte *)buf);
	*n = len;
	return buf;
}

jbyteArray hy2_new_bytes(JNIEnv *env, const void *p, int n) {
	jbyteArray a = (*env)->NewByteArray(env, n);
	if (a != NULL && n > 0)
		(*env)->SetByteArrayRegion(env, a, 0, n, (const jbyte *)p);
	return a;
}

void hy2_log(const char *msg) {
	__android_log_write(ANDROID_LOG_INFO, "hy2tun", msg);
}
