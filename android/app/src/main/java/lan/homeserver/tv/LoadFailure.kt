package lan.homeserver.tv

// Why a page failed to load, which decides the message shown. The codes are
// WebViewClient's ERROR_* values, written out so this stays plain Kotlin.
enum class LoadFailure {
    OFFLINE, UNTRUSTED, OTHER;

    companion object {
        private const val ERROR_HOST_LOOKUP = -2
        private const val ERROR_CONNECT = -6
        private const val ERROR_TIMEOUT = -8
        private const val ERROR_FAILED_SSL_HANDSHAKE = -11

        fun fromWebViewError(errorCode: Int): LoadFailure = when (errorCode) {
            ERROR_HOST_LOOKUP, ERROR_CONNECT, ERROR_TIMEOUT -> OFFLINE
            ERROR_FAILED_SSL_HANDSHAKE -> UNTRUSTED
            else -> OTHER
        }

        fun fromSslError(): LoadFailure = UNTRUSTED

        // Whether a new failure for nextUrl may replace the one on screen.
        // After the SSL handler cancels a load, WebView can report the same
        // load again as a generic error; that must not hide the "CA changed,
        // reinstall" message, which is the one that tells the user what to do.
        fun replaces(shown: LoadFailure?, shownUrl: String?, next: LoadFailure, nextUrl: String?): Boolean =
            !(shown == UNTRUSTED && next != UNTRUSTED && shownUrl == nextUrl)
    }
}
