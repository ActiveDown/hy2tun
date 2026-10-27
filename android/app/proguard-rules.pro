# Called from native code (jni_android.c / jni_android.go)
-keep class com.activedown.hy2tun.Core {
    public static boolean protect(int);
    public static java.lang.String establish();
    public static native byte[] nativeCall(java.lang.String, byte[]);
}
