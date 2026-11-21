package lan.homeserver.tv

import android.annotation.SuppressLint
import android.app.Activity
import android.content.Intent
import android.graphics.Bitmap
import android.net.http.SslError
import android.os.Build
import android.os.Bundle
import android.util.Log
import android.view.Gravity
import android.view.KeyEvent
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import android.webkit.CookieManager
import android.webkit.JavascriptInterface
import android.webkit.SslErrorHandler
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import java.io.ByteArrayInputStream
import java.net.URI
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate

// The whole app: a full-screen WebView on the media site. It never leaves the
// LAN domain, and trusts the baked-in home CA as a fallback to the network
// security config.
class MainActivity : Activity() {
    private val urls = AppUrls(BuildConfig.LAN_DOMAIN, BuildConfig.HTTPS_PORT, BuildConfig.HTTP_PORT)

    private lateinit var webView: WebView
    private lateinit var overlay: FrameLayout
    private lateinit var errorView: ErrorView
    private val homeCa: X509Certificate by lazy {
        resources.openRawResource(R.raw.home_ca).use {
            CertificateFactory.getInstance("X.509").generateCertificate(it) as X509Certificate
        }
    }

    private val updater by lazy { Updater(this, urls, homeCa) }
    private var banner: UpdateBanner? = null
    private val chrome = Chrome()
    private var failedUrl: String? = null
    private var playing = false
    private var customView: View? = null
    private var customViewCallback: WebChromeClient.CustomViewCallback? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        webView = findViewById(R.id.webview)
        overlay = findViewById(R.id.overlay)
        errorView = ErrorView(this) {
            errorView.hide()
            // loadUrl skips shouldOverrideUrlLoading, so vet the URL here.
            val retryUrl = failedUrl?.takeIf { it.startsWith("https://") && hostAllowed(hostOf(it)) }
            webView.loadUrl(retryUrl ?: urls.start)
        }
        overlay.addView(errorView, ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)

        configureWebView()
        if (savedInstanceState == null) webView.loadUrl(urls.start) else webView.restoreState(savedInstanceState)

        updater.check { info -> if (info != null && info.newerThan(BuildConfig.VERSION_CODE)) showUpdateBanner(info) }
    }

    @Suppress("SetJavaScriptEnabled") // the media site is a JS app
    private fun configureWebView() {
        webView.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            mediaPlaybackRequiresUserGesture = true
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            allowFileAccess = false
            allowContentAccess = false
        }
        CookieManager.getInstance().setAcceptCookie(true)
        webView.addJavascriptInterface(HomeServerApp(), "HomeServerApp")
        webView.webViewClient = Client()
        webView.webChromeClient = chrome
    }

    private fun hostAllowed(host: String?) = HostPolicy.allowed(host, BuildConfig.LAN_DOMAIN)

    private fun hostOf(url: String?): String? = try {
        url?.let { URI(it).host }
    } catch (e: java.net.URISyntaxException) {
        null
    }

    private inner class Client : WebViewClient() {
        override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
            val url = request.url
            val stay = url.scheme == "https" && hostAllowed(url.host)
            return !stay // true = blocked; no Intent is ever fired
        }

        // shouldOverrideUrlLoading is not called for POSTs (a form or JS submit),
        // so also refuse any main-frame request that leaves the LAN domain. The
        // API can't tell iframes from subresources, so those are not covered.
        override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
            val url = request.url
            if (!request.isForMainFrame || (url.scheme == "https" && hostAllowed(url.host))) return null
            return WebResourceResponse("text/plain", "utf-8", 403, "Blocked", emptyMap(), ByteArrayInputStream(ByteArray(0)))
        }

        override fun onPageFinished(view: WebView, url: String?) {
            // Only our own pages get the bridge script.
            if (hostAllowed(hostOf(url))) view.evaluateJavascript(PLAYBACK_SCRIPT, null)
        }

        // proceed() only after the chain is verified against the home CA for our own host.
        @SuppressLint("WebViewClientOnReceivedSslError")
        override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, error: SslError) {
            val host = hostOf(error.url)
            // SslError.getCertificate().getX509Certificate() needs API 29; older
            // devices rely on the network security config alone.
            val leaf = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) error.certificate.x509Certificate else null
            if (leaf != null && hostAllowed(host) && CaTrust.chainsTo(leaf, homeCa)) {
                Log.i("HomeServer", "ssl fallback: trusted by home CA for $host")
                handler.proceed()
                return
            }
            handler.cancel()
            // The main frame's own load fails; subresource errors show nothing.
            if (error.url == view.url || error.url == pendingUrl) {
                failedUrl = error.url
                errorView.show(LoadFailure.fromSslError(), urls.reinstall)
            }
        }

        override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
            if (!request.isForMainFrame) return
            failedUrl = request.url.toString()
            errorView.show(LoadFailure.fromWebViewError(error.errorCode), urls.reinstall)
        }

        override fun onPageStarted(view: WebView, url: String?, favicon: Bitmap?) {
            pendingUrl = url
        }
    }

    // The URL of the load in progress: on a first-load SSL failure view.url is
    // still null/old, so the SSL handler compares against this too.
    private var pendingUrl: String? = null

    private inner class Chrome : WebChromeClient() {
        override fun onShowCustomView(view: View, callback: CustomViewCallback) {
            customView?.let { onHideCustomView() }
            customView = view
            customViewCallback = callback
            overlay.addView(view, ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
            setFullscreenBars(true)
            updateKeepScreenOn()
        }

        override fun onHideCustomView() {
            val view = customView ?: return
            overlay.removeView(view)
            customView = null
            customViewCallback?.onCustomViewHidden()
            customViewCallback = null
            setFullscreenBars(false)
            updateKeepScreenOn()
        }
    }

    // Called by the page's play/pause/ended listeners, on a WebView thread.
    private inner class HomeServerApp {
        @JavascriptInterface
        fun setPlaying(playing: Boolean) {
            runOnUiThread {
                // Defence in depth: only our own pages may act on the bridge.
                if (!hostAllowed(hostOf(webView.url))) return@runOnUiThread
                this@MainActivity.playing = playing
                updateKeepScreenOn()
            }
        }
    }

    private fun updateKeepScreenOn() {
        if (playing || customView != null) {
            window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        } else {
            window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        }
    }

    private fun setFullscreenBars(on: Boolean) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            val c = window.insetsController ?: return
            val bars = android.view.WindowInsets.Type.systemBars()
            if (on) {
                c.hide(bars)
                c.systemBarsBehavior = android.view.WindowInsetsController.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
            } else {
                c.show(bars)
            }
        } else {
            @Suppress("DEPRECATION")
            window.decorView.systemUiVisibility = if (on) {
                View.SYSTEM_UI_FLAG_FULLSCREEN or View.SYSTEM_UI_FLAG_HIDE_NAVIGATION or
                    View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY or View.SYSTEM_UI_FLAG_LAYOUT_STABLE
            } else {
                View.SYSTEM_UI_FLAG_VISIBLE
            }
        }
    }

    // Back: fullscreen, then the error view, then page history, then exit.
    @Deprecated("Deprecated in Java")
    override fun onBackPressed() {
        when {
            customView != null -> chrome.onHideCustomView()
            errorView.showing -> errorView.hide()
            webView.canGoBack() -> webView.goBack()
            else -> finish()
        }
    }

    // The remote's media keys go to the page (it handles them as of Task 7).
    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        if (event.keyCode in MEDIA_KEYS) return webView.dispatchKeyEvent(event)
        return super.dispatchKeyEvent(event)
    }

    override fun onPause() {
        CookieManager.getInstance().flush()
        super.onPause()
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        webView.saveState(outState)
    }

    override fun onDestroy() {
        webView.destroy()
        super.onDestroy()
    }

    private fun showUpdateBanner(info: UpdateInfo) {
        if (banner != null) return
        lateinit var b: UpdateBanner
        b = UpdateBanner(
            this, info,
            onInstall = {
                b.setBusy()
                updater.install(info) { b.showError(it) }
            },
            onLater = {
                overlay.removeView(b)
                banner = null
                webView.requestFocus()
            },
        )
        banner = b
        overlay.addView(
            b,
            FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT, Gravity.BOTTOM),
        )
        b.requestFocus()
    }

    // The installer's answer comes back here (the activity is singleTop).
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        updater.handleResult(intent) { banner?.showError(it) }
    }

    private companion object {
        val MEDIA_KEYS = setOf(
            KeyEvent.KEYCODE_MEDIA_PLAY_PAUSE, KeyEvent.KEYCODE_MEDIA_PLAY, KeyEvent.KEYCODE_MEDIA_PAUSE,
            KeyEvent.KEYCODE_MEDIA_FAST_FORWARD, KeyEvent.KEYCODE_MEDIA_REWIND,
        )

        // Capture phase, because media events don't bubble.
        const val PLAYBACK_SCRIPT = """
            (function () {
              if (window.__hsPlaybackHook) return;
              window.__hsPlaybackHook = true;
              ['play', 'pause', 'ended'].forEach(function (t) {
                document.addEventListener(t, function () {
                  if (window.HomeServerApp) HomeServerApp.setPlaying(t === 'play');
                }, true);
              });
            })();
        """
    }
}
