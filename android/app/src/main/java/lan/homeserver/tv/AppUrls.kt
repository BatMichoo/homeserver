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

    fun https(path: String): String = "https://$lanDomain${port(httpsPort, 443)}$path"
}
