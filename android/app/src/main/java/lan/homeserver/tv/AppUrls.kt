package lan.homeserver.tv

// Every URL the app uses, derived from the baked-in domain and ports so no
// other code builds addresses by hand. Default ports are left out of the URL.
class AppUrls(private val lanDomain: String, private val httpsPort: Int, private val httpPort: Int) {
    private fun port(p: Int, default: Int) = if (p == default) "" else ":$p"

    val start: String get() = "https://media.$lanDomain${port(httpsPort, 443)}/"
    val updateInfo: String get() = https("/tv.json")

    // Plain HTTP on purpose: the way back in when the baked-in CA no longer
    // matches the server's, since the HTTPS update check would fail then.
    val reinstall: String get() = "http://$lanDomain${port(httpPort, 80)}/tv.apk"

    // isStart: url is the media home page (query and fragment aside), where
    // Back leaves the app instead of going back through the page history.
    fun isStart(url: String?): Boolean {
        val u = try {
            java.net.URI(url ?: return false)
        } catch (e: java.net.URISyntaxException) {
            return false
        }
        val port = if (u.port == -1) 443 else u.port
        return u.scheme == "https" && u.host.equals("media.$lanDomain", ignoreCase = true) &&
            port == httpsPort && (u.rawPath.isNullOrEmpty() || u.rawPath == "/")
    }

    fun https(path: String): String = "https://$lanDomain${port(httpsPort, 443)}$path"
}
