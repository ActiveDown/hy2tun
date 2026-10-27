package com.activedown.hy2tun

import android.Manifest
import android.annotation.SuppressLint
import android.app.AlertDialog
import android.content.Intent
import android.content.pm.PackageManager
import android.content.res.Configuration
import android.graphics.Color
import android.net.Uri
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import android.webkit.JavascriptInterface
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import org.json.JSONObject
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * The window: the same panel as on Windows and macOS (ui.html, served by the
 * engine on 127.0.0.1) in a WebView. The parts that do not exist on Android
 * are hidden by ANDROID_JS; QR scanning and hy2:// links are handled here.
 */
class MainActivity : ComponentActivity() {

    companion object {
        @Volatile
        private var current: MainActivity? = null

        /** The window is in the foreground (system dialogs can be shown). */
        val visible get() = current != null

        /**
         * Shows the system VPN consent dialog for a connection started from the
         * panel and waits for the answer (called on an engine thread).
         */
        fun askVpnPermission(): Boolean {
            val act = current ?: return false
            val latch = CountDownLatch(1)
            var granted = false
            act.runOnUiThread {
                val ask = VpnService.prepare(act)
                if (ask == null) {
                    granted = true
                    latch.countDown()
                } else {
                    act.onVpnAnswer = { granted = it; latch.countDown() }
                    act.vpnPermission.launch(ask)
                }
            }
            latch.await(2, TimeUnit.MINUTES)
            return granted
        }
    }

    private lateinit var web: WebView
    private var onVpnAnswer: ((Boolean) -> Unit)? = null

    private val vpnPermission = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) {
        onVpnAnswer?.invoke(it.resultCode == RESULT_OK)
        onVpnAnswer = null
    }

    private val notifyPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    private val qrScan = registerForActivityResult(ScanContract()) { r ->
        r.contents?.let { importLink(it) }
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        Core.init(this)

        val dark = (resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK) == Configuration.UI_MODE_NIGHT_YES
        val root = FrameLayout(this)
        root.setBackgroundColor(Color.parseColor(if (dark) "#0e1015" else "#f2f4f8")) // the panel's --bg
        web = WebView(this)
        web.setBackgroundColor(Color.TRANSPARENT)
        root.addView(web)
        setContentView(root)
        WindowCompat.getInsetsController(window, root).isAppearanceLightStatusBars = !dark
        WindowCompat.getInsetsController(window, root).isAppearanceLightNavigationBars = !dark
        ViewCompat.setOnApplyWindowInsetsListener(root) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.ime())
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            WindowInsetsCompat.CONSUMED
        }

        web.settings.javaScriptEnabled = true
        web.settings.domStorageEnabled = true
        web.webChromeClient = WebChromeClient() // confirm() dialogs of the panel
        web.addJavascriptInterface(Bridge(), "Hy2Android")
        web.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, req: WebResourceRequest): Boolean {
                if (req.url.host == "127.0.0.1") return false
                runCatching { startActivity(Intent(Intent.ACTION_VIEW, req.url)) } // links leave the panel
                return true
            }

            override fun onPageFinished(view: WebView, url: String) {
                view.evaluateJavascript(ANDROID_JS, null)
            }
        }
        web.loadUrl(Core.ui())

        handleIntent(intent)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            notifyPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
    }

    override fun onResume() {
        super.onResume()
        current = this
    }

    override fun onPause() {
        if (current === this) current = null
        super.onPause()
    }

    override fun onDestroy() {
        web.destroy()
        super.onDestroy()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleIntent(intent)
    }

    /** A hy2:// link opened from another app: add it after a confirmation. */
    private fun handleIntent(intent: Intent?) {
        val data = intent?.dataString ?: return
        if (intent.action != Intent.ACTION_VIEW || !isLink(data)) return
        // show the server only: the part before @ is the password
        val host = Uri.parse(data).let { (it.host ?: "") + (if (it.port > 0) ":" + it.port else "") }
        AlertDialog.Builder(this)
            .setTitle(R.string.add_profile_q)
            .setMessage(host)
            .setPositiveButton(R.string.add) { _, _ -> importLink(data) }
            .setNegativeButton(R.string.cancel, null)
            .show()
    }

    private fun importLink(text: String) {
        val link = text.trim().lineSequence().map { it.trim() }.firstOrNull { isLink(it) }
        val msg = if (link == null) getString(R.string.err_not_a_link) else try {
            getString(R.string.added, Core.addServer(link))
        } catch (e: Core.Error) {
            e.message ?: ""
        }
        if (::web.isInitialized) web.evaluateJavascript("toast(${JSONObject.quote(msg)});tick()", null)
        else Toast.makeText(this, msg, Toast.LENGTH_LONG).show()
    }

    /** window.Hy2Android in the panel. */
    inner class Bridge {
        /** The system VPN settings: "Always-on VPN" and "Block connections without VPN". */
        @JavascriptInterface
        fun openVpnSettings() = runOnUiThread {
            runCatching { startActivity(Intent(Settings.ACTION_VPN_SETTINGS)) }
        }

        @JavascriptInterface
        fun scanQr() = runOnUiThread {
            qrScan.launch(
                ScanOptions()
                    .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
                    .setPrompt(getString(R.string.scan_prompt))
                    .setBeepEnabled(false)
                    .setOrientationLocked(true),
            )
        }
    }
}

/**
 * Adapts the desktop panel to Android after it loaded: hides what the system
 * does itself or what has no meaning here (DNS leak protection, other VPN
 * clients, child processes, start with the computer, process names, the
 * diagnostics file), words the app list and the kill switch for Android and
 * adds QR scanning next to the link field.
 */
private val ANDROID_JS = """
(function(){
  if(document.body.classList.contains('android'))return;
  document.body.classList.add('android');
  const hide=e=>{if(e)e.style.display='none'};
  hide(${'$'}('kids').closest('label'));
  hide(${'$'}('appIn').closest('.add'));
  hide(${'$'}('vpnc').closest('.card'));
  hide(${'$'}('dnsleak').closest('.card'));
  hide(${'$'}('dApps').closest('.card'));
  hide(${'$'}('auto').closest('.card'));
  ${'$'}('recent').closest('.card').style.marginTop='12px';
  hide(${'$'}('diagBtn'));hide(${'$'}('diagDone'));
  hide(document.querySelector('[data-i18n="diag.desc"]'));
  Object.assign(L.en,{'apps.hint':'Pick the apps from the installed ones.','qr.scan':'Scan QR code',
    'kill.desc':'While the VPN is off, the selected apps have no internet (only in the "Only selected apps" mode). To protect all apps, turn on "Always-on VPN" and "Block connections without VPN" for Hy2Tun in the Android settings.',
    'kill.sys':'Open VPN settings',
    'diag.debugd':'Logs every connection (sites and apps). Takes effect on the next connect.'});
  Object.assign(L.ru,{'apps.hint':'Выберите приложения из установленных.','qr.scan':'Сканировать QR-код',
    'kill.desc':'Пока VPN выключен, у выбранных приложений нет интернета (только в режиме «Только выбранные»). Чтобы защитить все приложения, включите для Hy2Tun «Постоянная VPN» и «Блокировать соединения без VPN» в настройках Android.',
    'kill.sys':'Открыть настройки VPN',
    'diag.debugd':'Записывает каждое соединение (сайты и приложения). Включается при следующем подключении.'});
  const b=document.createElement('button');
  b.className='btn';b.style.marginTop='8px';b.dataset.i18n='qr.scan';
  b.onclick=()=>Hy2Android.scanQr();
  ${'$'}('linkAdd').closest('.add').after(b);
  const k=document.createElement('button');
  k.className='btn sm';k.style.marginTop='8px';k.dataset.i18n='kill.sys';
  k.onclick=()=>Hy2Android.openVpnSettings();
  ${'$'}('kill').closest('.row').after(k);
  applyLang();
})();
"""
