package lan.homeserver.tv

// Which hosts the WebView may stay on: the LAN domain and its subdomains. The
// dot in the suffix check keeps "evilhomeserver.lan" out.
object HostPolicy {
    fun allowed(host: String?, lanDomain: String): Boolean {
        if (host.isNullOrEmpty()) return false
        val h = host.lowercase()
        val d = lanDomain.lowercase()
        return h == d || h.endsWith(".$d")
    }
}
