package com.activedown.hy2tun

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.util.Log
import java.net.Inet6Address

/**
 * Tracks the underlying network and reports it to the engine: its DNS servers
 * for the NXDOMAIN fallback and whether it has IPv6; a change (Wi-Fi <-> mobile)
 * makes the engine re-establish the connection. The default-network callback is
 * useless here: for the VPN app it reports its own tun0 and then stays silent,
 * so the best non-VPN network is tracked instead (it is the system default one).
 */
object NetWatch {
    @Volatile
    var current: Network? = null
        private set

    private var started = false

    @Synchronized
    fun start(ctx: Context) {
        if (started) return
        started = true
        val cm = ctx.getSystemService(ConnectivityManager::class.java)
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onLinkPropertiesChanged(network: Network, lp: LinkProperties) {
                if (cm.getNetworkCapabilities(network)?.hasTransport(NetworkCapabilities.TRANSPORT_VPN) == true) return
                val dns = lp.dnsServers.mapNotNull { it.hostAddress?.substringBefore('%') }
                val v6 = lp.linkAddresses.any {
                    val a = it.address
                    a is Inet6Address && !a.isLinkLocalAddress && !a.isSiteLocalAddress && !a.isLoopbackAddress &&
                        (a.address[0].toInt() and 0xfe) != 0xfc // not ULA
                }
                Log.i("hy2tun", "underlying network ${lp.interfaceName}, dns $dns, ipv6 $v6")
                current = network
                runCatching { Core.network(lp.interfaceName ?: network.toString(), dns, v6) }
                Core.vpn?.setUnderlyingNetworks(arrayOf(network))
            }

            override fun onLost(network: Network) {
                if (current == network) {
                    current = null
                    Core.vpn?.setUnderlyingNetworks(null)
                }
            }
        }
        val req = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_RESTRICTED)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .build()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            cm.registerBestMatchingNetworkCallback(req, cb, Handler(Looper.getMainLooper()))
        } else {
            // the same request as the system default one: satisfied by the default network
            cm.requestNetwork(req, cb)
        }
    }
}
