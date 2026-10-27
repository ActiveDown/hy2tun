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
import android.os.ParcelFileDescriptor
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import org.json.JSONObject
import java.io.FileInputStream
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlin.concurrent.thread

/**
 * The VPN side of a connection. The first socket of the engine (Core.protect)
 * starts this service in the foreground; after connecting to the server the
 * engine asks for its TUN (Core.establish -> establish below), and the service
 * builds the interface (address, routes, DNS, the per-app choice). The engine
 * owns the descriptor and closes it on disconnect, after which the service stops.
 *
 * Kill switch (as on Windows: mode "only selected apps"): while the tunnel is
 * down the service keeps an interface for the selected apps that leads nowhere,
 * so they have no internet; the other apps are not affected.
 */
class Hy2VpnService : VpnService() {

    companion object {
        private const val TAG = "hy2tun"
        const val ACTION_START = "com.activedown.hy2tun.START"
        const val ACTION_CONNECT = "com.activedown.hy2tun.CONNECT"
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
            svc.unblock() // the tunnel took over from the kill switch interface
            NetWatch.current?.let { svc.setUnderlyingNetworks(arrayOf(it)) }
            return pfd.detachFd()
        }

        /** The engine is about to close the tunnel: block first if the kill switch is on. */
        fun tunClosing() {
            val svc = instance ?: return
            killSwitchApps(Core.config())?.let { svc.block(it) }
        }

        /**
         * Starts the kill switch when it is on, the tunnel is down and the service
         * is not running (at app start, or right after it was switched on).
         */
        fun startKillSwitch(ctx: Context) {
            if (instance != null) return
            if (killSwitchApps(Core.config()) == null || Core.status().active) return
            if (VpnService.prepare(ctx) != null) {
                // without the VPN permission there is nothing to block with: ask once,
                // while the window is open
                if (killAsked || !MainActivity.visible) return
                killAsked = true
            }
            runCatching { start(ctx) }
        }

        @Volatile
        private var killAsked = false

        /** The apps the kill switch blocks while the tunnel is down, or null when it is off. */
        fun killSwitchApps(cfg: JSONObject): List<String>? {
            if (!cfg.optBoolean("kill_switch") || cfg.optString("mode") != "selected") return null
            val direct = cfg.optJSONArray("direct_apps").strings().toSet()
            return cfg.optJSONArray("apps").strings()
                .filter { it != Core.app.packageName && it !in direct }
                .ifEmpty { null }
        }
    }

    @Volatile
    private var running = true

    /** The per-app part of the interface that is up (mode and lists). */
    @Volatile
    private var appsSig = ""

    /** The kill switch interface and the apps it was built for. */
    private var blackhole: ParcelFileDescriptor? = null
    private var blockedApps: List<String>? = null

    override fun onCreate() {
        super.onCreate()
        Core.init(this)
        Core.vpn = this
        createChannel()
        notifyState(getString(R.string.state_connecting), "", connect = false)
        instance = this
        ready.countDown()
        thread(name = "vpn-monitor") { monitor() }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_START -> {}
            ACTION_DISCONNECT -> runCatching { Core.disconnect() }
            ACTION_CONNECT -> runCatching { if (!Core.status().active) Core.connect() }
            // android.net.VpnService: the system starts the always-on VPN
            else -> runCatching { if (!Core.status().active) Core.connect() }
        }
        return START_NOT_STICKY
    }

    override fun onRevoke() {
        // another VPN app took over, or the user revoked the permission: let go
        running = false
        runCatching { Core.disconnect() }
        unblock()
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    override fun onDestroy() {
        running = false
        instance = null
        ready = CountDownLatch(1)
        unblock()
        // after a failed attempt the engine is already down: keep its error for the panel
        runCatching { if (Core.status().active) Core.disconnect() }
        Core.vpn = null
        super.onDestroy()
    }

    private fun baseBuilder(session: String): Builder {
        val t = Core.tunInfo()
        val b = Builder()
            .setSession(session)
            .setMtu(t.getInt("mtu"))
            .addAddress(t.getString("addr4"), t.getInt("prefix4"))
            .addAddress(t.getString("addr6"), t.getInt("prefix6"))
            .addRoute("0.0.0.0", 0)
            .addRoute("::", 0)
            .setConfigureIntent(openAppIntent())
        t.optJSONArray("dns").strings().forEach { b.addDnsServer(it) }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) b.setMetered(false)
        return b
    }

    fun buildInterface(): Builder {
        val b = baseBuilder(getString(R.string.app_name))
        applyApps(b)
        return b
    }

    private fun installed(f: () -> Unit) = try {
        f(); true
    } catch (_: PackageManager.NameNotFoundException) {
        false // not an installed package (uninstalled, or a desktop process name)
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
        if (cfg.optString("mode") == "selected") {
            val allowed = apps.filter { it !in directApps && installed { b.addAllowedApplication(it) } }
            if (allowed.isEmpty()) throw Core.Error(getString(R.string.err_no_apps))
        } else {
            b.addDisallowedApplication(packageName)
            val excluded = if (cfg.optString("mode") == "except") apps + directApps else directApps
            excluded.filter { it != packageName }.forEach { installed { b.addDisallowedApplication(it) } }
        }
    }

    /** Puts up (or rebuilds) the kill switch interface for apps; nothing reads it. */
    @Synchronized
    fun block(apps: List<String>) {
        if (blackhole != null && blockedApps == apps) return
        val b = baseBuilder(getString(R.string.app_name) + " · kill switch")
        if (apps.none { installed { b.addAllowedApplication(it) } }) return
        val pfd = runCatching { b.establish() }.getOrNull() ?: return
        val old = blackhole
        blackhole = pfd
        blockedApps = apps
        old?.close()
        Log.i(TAG, "kill switch: ${apps.size} app(s) blocked while the tunnel is down")
        thread(name = "kill-switch") {
            // drain the packets so the apps fail fast instead of waiting on a full queue
            val buf = ByteArray(32 * 1024)
            runCatching { FileInputStream(pfd.fileDescriptor).use { while (it.read(buf) >= 0) Unit } }
        }
    }

    @Synchronized
    fun unblock() {
        blackhole?.let {
            runCatching { it.close() }
            Log.i(TAG, "kill switch: released")
        }
        blackhole = null
        blockedApps = null
    }

    /**
     * Mirrors the engine in the notification, keeps the kill switch in line with
     * the settings and stops the service once there is nothing left to do.
     */
    private fun monitor() {
        var idle = 0
        var lastSig = ""
        var stable = 0
        while (running) {
            val st = runCatching { Core.status() }.getOrNull()
            val cfg = runCatching { Core.config() }.getOrNull()
            if (st?.state == "connected") {
                idle = 0
                val speed = getString(R.string.speed_line, formatBytes(st.downBps.toLong()) + "/s", formatBytes(st.upBps.toLong()) + "/s")
                notifyState(getString(R.string.state_connected_to, st.server), speed, connect = false)
                // reconnect once the user stopped editing the list (unchanged for 2 s);
                // "selected" with an empty list would bring the tunnel down: wait for an app
                val sig = cfg?.let {
                    val empty = it.optString("mode") == "selected" && it.optJSONArray("apps").strings().isEmpty()
                    if (empty) appsSig else appsSignature(it)
                } ?: appsSig
                if (sig != appsSig && sig == lastSig && ++stable >= 4) {
                    Log.i(TAG, "the app list changed: reconnecting to rebuild the VPN interface")
                    runCatching { Core.reconnect() }
                } else if (sig != lastSig) {
                    stable = 0
                }
                lastSig = sig
            } else if (st?.state == "connecting") {
                idle = 0
            } else {
                val kill = cfg?.let { killSwitchApps(it) }
                if (kill != null) {
                    idle = 0
                    block(kill)
                    notifyState(getString(R.string.kill_title), getString(R.string.kill_text), connect = true)
                } else {
                    unblock()
                    if (++idle >= 6) {
                        Log.i(TAG, "tunnel is down (${st?.state}), stopping the VPN service")
                        stopForeground(STOP_FOREGROUND_REMOVE)
                        stopSelf()
                        return
                    }
                }
            }
            Thread.sleep(500)
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

    private fun serviceIntent(code: Int, action: String): PendingIntent =
        PendingIntent.getService(
            this, code, Intent(this, Hy2VpnService::class.java).setAction(action),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )

    /** The foreground notification; connect: offer Connect instead of Disconnect. */
    private fun notifyState(title: String, text: String, connect: Boolean) {
        val action = if (connect) {
            NotificationCompat.Action(0, getString(R.string.action_connect), serviceIntent(2, ACTION_CONNECT))
        } else {
            NotificationCompat.Action(0, getString(R.string.action_disconnect), serviceIntent(1, ACTION_DISCONNECT))
        }
        val n: Notification = NotificationCompat.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(title)
            .setContentText(text)
            .setContentIntent(openAppIntent())
            .setOnlyAlertOnce(true)
            .setSilent(true)
            .setOngoing(true)
            .addAction(action)
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
