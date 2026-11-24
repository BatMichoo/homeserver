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
        // restoreState returns null when the saved state can't be used (e.g.
        // a WebView update in between); the app then starts afresh.
        if (savedInstanceState == null || webView.restoreState(savedInstanceState) == null) webView.loadUrl(urls.start)

        updater.check { info -> if (info != null && info.newerThan(BuildConfig.VERSION_CODE)) showUpdateBanner(info) }
    }

    @Suppress("SetJavaScriptEnabled") // the media site is a JS app
    private fun configureWebView() {
        webView.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            // The player starts the video itself after a page load, and the
            // remote's OK is not a gesture WebView counts. Navigation never
            // leaves the LAN domain, so only our own pages can autoplay.
            mediaPlaybackRequiresUserGesture = false
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
            if (error.url == view.url || error.url == pendingUrl) showFailure(LoadFailure.fromSslError(), error.url)
        }

        override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
            if (!request.isForMainFrame) return
            showFailure(LoadFailure.fromWebViewError(error.errorCode), request.url.toString())
        }

        // A new page has nothing playing yet; its video reports itself again.
        override fun onPageStarted(view: WebView, url: String?, favicon: Bitmap?) {
            pendingUrl = url
            setPlaying(false)
        }

        // htmx moves between pages without a page load (pushState), and the
        // video it swapped away sends no pause. Forget the old state and let
        // the page say whether anything still plays.
        override fun doUpdateVisitedHistory(view: WebView, url: String?, isReload: Boolean) {
            setPlaying(false)
            if (hostAllowed(hostOf(url))) view.evaluateJavascript(PLAYBACK_RECHECK, null)
        }
    }

    private fun showFailure(failure: LoadFailure, url: String?) {
        if (!LoadFailure.replaces(errorView.failure, failedUrl, failure, url)) return
        failedUrl = url
        errorView.show(failure, urls.reinstall)
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
            // The remote's keys go to the focused view: the video, not the
            // WebView hidden underneath.
            view.requestFocus()
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
                this@MainActivity.setPlaying(playing)
            }
        }
    }

    private fun setPlaying(on: Boolean) {
        playing = on
        updateKeepScreenOn()
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
    // On the media home page it exits straight away: the history behind it
    // is only pages already left, and walking back through them never ends.
    @Deprecated("Deprecated in Java")
    override fun onBackPressed() {
        when {
            customView != null -> chrome.onHideCustomView()
            errorView.showing -> errorView.hide()
            webView.canGoBack() && !urls.isStart(webView.url) -> webView.goBack()
            else -> finish()
        }
    }

    // The remote's media keys go to the page's player. In fullscreen the
    // video is WebView's custom view (the WebView itself is left behind), so
    // they go there; whatever neither takes gets the normal dispatch.
    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        if (event.keyCode in MEDIA_KEYS && (customView ?: webView).dispatchKeyEvent(event)) return true
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
                if (!updater.ensureInstallPermission { b.showError(it) }) return@UpdateBanner
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

        // Reports whether any video on the page is playing (neither paused
        // nor ended), so one video stopping doesn't clear another's state. It
        // runs on the media events (capture phase: they don't bubble), after
        // htmx swaps a page in or restores one (a swapped-out video sends no
        // pause), and the app calls it after each history change. Leaving the
        // page stops whatever played.
        const val PLAYBACK_SCRIPT = """
            (function () {
              if (window.__hsPlaybackHook) return;
              function report() {
                if (!window.HomeServerApp) return;
                var playing = Array.prototype.some.call(document.querySelectorAll('video'), function (v) {
                  return !v.paused && !v.ended;
                });
                HomeServerApp.setPlaying(playing);
              }
              window.__hsPlaybackHook = report;
              ['play', 'pause', 'ended'].forEach(function (t) { document.addEventListener(t, report, true); });
              ['htmx:afterSwap', 'htmx:historyRestore'].forEach(function (t) { document.addEventListener(t, report); });
              window.addEventListener('pagehide', function () {
                if (window.HomeServerApp) HomeServerApp.setPlaying(false);
              });
            })();
        """

        const val PLAYBACK_RECHECK = "window.__hsPlaybackHook && window.__hsPlaybackHook();"
    }
}
