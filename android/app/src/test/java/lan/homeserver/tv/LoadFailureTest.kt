package lan.homeserver.tv

import org.junit.Assert.assertEquals
import org.junit.Test

// The numeric codes are WebViewClient.ERROR_* values; they are spelled out so
// this runs as a plain JVM test without the Android framework.
class LoadFailureTest {
    @Test
    fun networkErrorsMeanOffline() {
        assertEquals(LoadFailure.OFFLINE, LoadFailure.fromWebViewError(-2)) // ERROR_HOST_LOOKUP
        assertEquals(LoadFailure.OFFLINE, LoadFailure.fromWebViewError(-6)) // ERROR_CONNECT
        assertEquals(LoadFailure.OFFLINE, LoadFailure.fromWebViewError(-8)) // ERROR_TIMEOUT
    }

    @Test
    fun otherErrorsAreOther() {
        assertEquals(LoadFailure.OTHER, LoadFailure.fromWebViewError(-1)) // ERROR_UNKNOWN
        assertEquals(LoadFailure.OTHER, LoadFailure.fromWebViewError(0))
    }

    @Test
    fun sslErrorsAreUntrusted() {
        assertEquals(LoadFailure.UNTRUSTED, LoadFailure.fromSslError())
    }
}
