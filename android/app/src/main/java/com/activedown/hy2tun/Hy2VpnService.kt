package com.activedown.hy2tun

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.net.VpnService
import android.os.Build
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import org.json.JSONObject
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlin.concurrent.thread

/**
 * The VPN side of a connection. The first socket of the engine (Core.protect)
 * starts this service in the foreground; after connecting to the server the
 * engine asks for its TUN (Core.establish -> establish below), and the service
 * builds the interface (address, routes, DNS, the per-app choice). The engine
 * owns the descriptor and closes it on disconnect, after which the service stops.
 */
class Hy2VpnService : VpnService() {

    companion object {
        private const val TAG = "hy2tun"
        const val ACTION_START = "com.activedown.hy2tun.START"
        const val ACTION_DISCONNECT = "com.activedown.hy2tun.DISCONNECT"
        private const val CHANNEL = "tunnel"
        private const val NOTE_ID = 1

        @Volatile
        private var instance: Hy2VpnService? = null

        @Volatile
        private var ready = CountDownLatch(1)

        @Volatile
        private var deniedAt = 0L

        /**
         * Makes sure the user allowed the VPN and the service runs in the
         * foreground; returns the service.
         */
        @Synchronized
        fun start(ctx: Context): Hy2VpnService {
            instance?.let { return it }
            if (VpnService.prepare(ctx) != null) {
                // one answer per attempt: protect() and establish() both get here
                if (System.currentTimeMillis() - deniedAt < 15_000 || !MainActivity.askVpnPermission()) {
                    deniedAt = System.currentTimeMillis()
                    throw Core.Error(ctx.getString(R.string.err_no_permission))
                }
            }
            ContextCompat.startForegroundService(ctx, Intent(ctx, Hy2VpnService::class.java).setAction(ACTION_START))
            if (!ready.await(10, TimeUnit.SECONDS)) throw Core.Error("VPN service did not start")
            return instance ?: throw Core.Error("VPN service did not start")
        }

        /** Builds the VPN interface for the engine; returns its detached descriptor. */
        fun establish(ctx: Context): Int {
            val svc = start(ctx)
            val pfd = svc.buildInterface().establish() ?: throw Core.Error(ctx.getString(R.string.err_no_permission))
            NetWatch.current?.let { svc.setUnderlyingNetworks(arrayOf(it)) }
            return pfd.detachFd()
        }
    }

    @Volatile
    private var running = true

    /** The per-app part of the interface that is up (mode and lists). */
    @Volatile
    private var appsSig = ""

    override fun onCreate() {
        super.onCreate()
        Core.init(this)
        Core.vpn = this
        createChannel()
        foreground(getString(R.string.state_connecting), "")
        instance = this
        ready.countDown()
        thread(name = "vpn-monitor") { monitor() }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_START -> {}
            ACTION_DISCONNECT -> runCatching { Core.disconnect() }
            // android.net.VpnService: the system starts the always-on VPN
            else -> runCatching { if (!Core.status().active) Core.connect() }
        }
        return START_NOT_STICKY
    }

    override fun onRevoke() {
        // another VPN app took over, or the user revoked the permission
        runCatching { Core.disconnect() }
    }

    override fun onDestroy() {
        running = false
        instance = null
        ready = CountDownLatch(1)
        // after a failed attempt the engine is already down: keep its error for the panel
        runCatching { if (Core.status().active) Core.disconnect() }
        Core.vpn = null
        super.onDestroy()
    }

    fun buildInterface(): Builder {
        val t = Core.tunInfo()
        val b = Builder()
            .setSession(getString(R.string.app_name))
            .setMtu(t.getInt("mtu"))
            .addAddress(t.getString("addr4"), t.getInt("prefix4"))
            .addAddress(t.getString("addr6"), t.getInt("prefix6"))
            .addRoute("0.0.0.0", 0)
            .addRoute("::", 0)
            .setConfigureIntent(openAppIntent())
        t.optJSONArray("dns").strings().forEach { b.addDnsServer(it) }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) b.setMetered(false)
        applyApps(b)
        return b
    }

    /**
     * The per-app split: "selected" lets only the chosen apps into the tunnel,
     * otherwise everything goes in except the excluded apps. The app itself never
     * enters its own tunnel (on top of protect()).
     */
    private fun appsSignature(cfg: JSONObject) =
        listOf("mode", "apps", "direct_apps").joinToString("|") { cfg.opt(it)?.toString() ?: "" }

    private fun applyApps(b: Builder) {
        val cfg = Core.config()
        appsSig = appsSignature(cfg)
        val apps = cfg.optJSONArray("apps").strings().filter { it != packageName }
        val directApps = cfg.optJSONArray("direct_apps").strings().toSet()
        fun ok(f: () -> Unit) = try {
            f(); true
        } catch (_: PackageManager.NameNotFoundException) {
            false // not an installed package (uninstalled, or a desktop process name)
        }
        if (cfg.optString("mode") == "selected") {
            val allowed = apps.filter { it !in directApps && ok { b.addAllowedApplication(it) } }
            if (allowed.isEmpty()) throw Core.Error(getString(R.string.err_no_apps))
        } else {
            b.addDisallowedApplication(packageName)
            val excluded = if (cfg.optString("mode") == "except") apps + directApps else directApps
            excluded.filter { it != packageName }.forEach { ok { b.addDisallowedApplication(it) } }
        }
    }

    /** Mirrors the engine in the notification; stops the service once the tunnel is gone. */
    private fun monitor() {
        var idle = 0
        var lastSig = ""
        var stable = 0
        while (running) {
            val st = runCatching { Core.status() }.getOrNull()
            if (st?.state == "connected") {
                idle = 0
                // reconnect once the user stopped editing the list (unchanged for 2 s)
                // ("selected" with an empty list would bring the tunnel down: wait for an app)
                val sig = runCatching {
                    val cfg = Core.config()
                    val empty = cfg.optString("mode") == "selected" && cfg.optJSONArray("apps").strings().isEmpty()
                    if (empty) appsSig else appsSignature(cfg)
                }.getOrDefault(appsSig)
                if (sig != appsSig && sig == lastSig && ++stable >= 2) {
                    Log.i(TAG, "the app list changed: reconnecting to rebuild the VPN interface")
                    runCatching { Core.reconnect() }
                } else if (sig != lastSig) {
                    stable = 0
                }
                lastSig = sig
                val speed = getString(R.string.speed_line, formatBytes(st.downBps.toLong()) + "/s", formatBytes(st.upBps.toLong()) + "/s")
                foreground(getString(R.string.state_connected_to, st.server), speed)
            } else if (st?.state == "connecting") {
                idle = 0
            } else if (++idle >= 3) {
                Log.i(TAG, "tunnel is down (${st?.state}), stopping the VPN service")
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return
            }
            Thread.sleep(1000)
        }
    }

    private fun createChannel() {
        val ch = NotificationChannel(CHANNEL, getString(R.string.channel_name), NotificationManager.IMPORTANCE_LOW)
        ch.setShowBadge(false)
        getSystemService(NotificationManager::class.java).createNotificationChannel(ch)
    }

    private fun openAppIntent(): PendingIntent =
        PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )

    private fun foreground(title: String, text: String) {
        val stop = PendingIntent.getService(
            this, 1, Intent(this, Hy2VpnService::class.java).setAction(ACTION_DISCONNECT),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val n: Notification = NotificationCompat.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(title)
            .setContentText(text)
            .setContentIntent(openAppIntent())
            .setOnlyAlertOnce(true)
            .setSilent(true)
            .setOngoing(true)
            .addAction(0, getString(R.string.action_disconnect), stop)
            .build()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(NOTE_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(NOTE_ID, n)
        }
    }
}

fun formatBytes(n: Long): String = when {
    n >= 1L shl 30 -> String.format("%.1f GB", n / (1L shl 30).toDouble())
    n >= 1L shl 20 -> String.format("%.1f MB", n / (1L shl 20).toDouble())
    n >= 1L shl 10 -> String.format("%.0f KB", n / 1024.0)
    else -> "$n B"
}
