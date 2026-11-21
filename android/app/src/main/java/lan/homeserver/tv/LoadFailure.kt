package lan.homeserver.tv

// Why a page failed to load, which decides the message shown. The codes are
// WebViewClient's ERROR_* values, written out so this stays plain Kotlin.
enum class LoadFailure {
    OFFLINE, UNTRUSTED, OTHER;

    companion object {
        private const val ERROR_HOST_LOOKUP = -2
        private const val ERROR_CONNECT = -6
        private const val ERROR_TIMEOUT = -8

        fun fromWebViewError(errorCode: Int): LoadFailure = when (errorCode) {
            ERROR_HOST_LOOKUP, ERROR_CONNECT, ERROR_TIMEOUT -> OFFLINE
            else -> OTHER
        }

        fun fromSslError(): LoadFailure = UNTRUSTED
    }
}
