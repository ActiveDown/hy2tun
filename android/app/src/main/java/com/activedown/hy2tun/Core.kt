package com.activedown.hy2tun

import android.content.Context
import android.content.Intent
import android.net.VpnService
import org.json.JSONArray
import org.json.JSONObject
import java.util.Locale
import kotlin.concurrent.thread

/**
 * The Go engine (libhy2tun.so, built from the repository root by
 * scripts/build_android.sh). Every call goes through one JNI entry point: a
 * method name and a JSON argument in, {"ok":..,"error":..,"data":..} out
 * (mobile_android.go). The engine also serves the desktop panel (ui.html) on
 * 127.0.0.1, which MainActivity shows.
 */
object Core {
    init {
        System.loadLibrary("hy2tun")
    }

    /** The running VPN service; Go asks it to protect its sockets. */
    @Volatile
    var vpn: VpnService? = null

    lateinit var app: Context
        private set

    @JvmStatic
    external fun nativeCall(method: String, arg: ByteArray): ByteArray

    /**
     * Called from native code for every socket of the engine (the connection to
     * the server, direct flows), so they never loop back into the tunnel. The
     * first one of a connection asks for the VPN permission and starts the service.
     */
    @JvmStatic
    fun protect(fd: Int): Boolean {
        // the server connection comes before the TUN: start the service for it
        val v = vpn ?: runCatching { Hy2VpnService.start(app) }.getOrNull() ?: return false
        return v.protect(fd)
    }

    /**
     * Called from native code when a connection needs its TUN: "fd:<n>" with the
     * VPN interface (the engine owns it from then on) or "error:<text>".
     */
    @JvmStatic
    fun establish(): String = try {
        "fd:" + Hy2VpnService.establish(app)
    } catch (e: Exception) {
        "error:" + (e.message ?: e.toString())
    }

    /** Called from native code right before the tunnel's interface is closed (kill switch). */
    @JvmStatic
    fun tunClosing() {
        runCatching { Hy2VpnService.tunClosing() }
    }

    class Error(message: String) : Exception(message)

    private fun call(method: String, arg: JSONObject = JSONObject()): Any? {
        val raw = nativeCall(method, arg.toString().toByteArray(Charsets.UTF_8))
        val out = JSONObject(String(raw, Charsets.UTF_8))
        if (!out.optBoolean("ok")) throw Error(out.optString("error", "error"))
        return if (out.isNull("data")) null else out.get("data")
    }

    @Volatile
    private var ready = false

    @Synchronized
    fun init(ctx: Context) {
        if (ready) return
        app = ctx.applicationContext
        call("init", JSONObject().put("dir", app.filesDir.absolutePath).put("lang", Locale.getDefault().language))
        ready = true
        NetWatch.start(app)
        thread(name = "apps") { sendInstalledApps() }
        thread(name = "kill-switch-start") {
            // the kill switch also holds while the app was closed or the phone restarted:
            // bring it up again (and when it is switched on with the tunnel down)
            while (true) {
                runCatching { Hy2VpnService.startKillSwitch(app) }
                Thread.sleep(2000)
            }
        }
    }

    /** The panel URL (with the access token). */
    fun ui(): String = call("ui") as String

    fun status(): Status = Status(call("status") as JSONObject)

    /** The routing policy (Config in config.go). */
    fun config(): JSONObject = call("config") as JSONObject

    fun tunInfo(): JSONObject = call("tun") as JSONObject

    /** Adds a hy2:// / hysteria2:// link; returns the profile name. */
    fun addServer(link: String): String =
        (call("addServer", JSONObject().put("link", link.trim())) as JSONObject).optString("name")

    fun network(name: String, dns: List<String>, v6: Boolean) {
        call("network", JSONObject().put("name", name).put("dns", JSONArray(dns)).put("v6", v6))
    }

    fun connect(id: String = "") {
        call("connect", JSONObject().put("id", id))
    }

    fun disconnect() {
        call("disconnect")
    }

    fun reconnect() {
        call("reconnect")
    }

    /** The installed apps for the panel's "Choose from installed apps" list. */
    private fun sendInstalledApps() {
        val pm = app.packageManager
        val launcher = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
        val list = pm.queryIntentActivities(launcher, 0)
            .map { it.activityInfo.applicationInfo }
            .distinctBy { it.packageName }
            .filter { it.packageName != app.packageName }
            .map { ai ->
                JSONObject().put("label", pm.getApplicationLabel(ai).toString())
                    .put("name", ai.packageName).put("path", ai.packageName)
            }
            .sortedBy { it.getString("label").lowercase() }
        runCatching { call("apps", JSONObject().put("apps", JSONArray(list))) }
    }
}

data class Status(val state: String, val error: String, val server: String, val upBps: Double, val downBps: Double) {
    constructor(o: JSONObject) : this(
        o.optString("state"), o.optString("error"), o.optString("server"),
        o.optDouble("up_bps", 0.0), o.optDouble("down_bps", 0.0),
    )

    val active get() = state == "connected" || state == "connecting"
}

fun JSONArray?.strings(): List<String> =
    if (this == null) emptyList() else (0 until length()).map { getString(it) }

fun isLink(s: String): Boolean {
    val l = s.trim().lowercase()
    return l.startsWith("hy2://") || l.startsWith("hysteria2://")
}
