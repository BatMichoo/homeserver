package lan.homeserver.tv

import android.app.Activity
import android.os.Bundle
import android.webkit.WebView

// Placeholder: just opens the media site. The real WebView activity replaces it.
class MainActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val webView = WebView(this)
        webView.settings.javaScriptEnabled = true
        setContentView(webView)
        val port = if (BuildConfig.HTTPS_PORT == 443) "" else ":${BuildConfig.HTTPS_PORT}"
        webView.loadUrl("https://media.${BuildConfig.LAN_DOMAIN}$port/")
    }
}
