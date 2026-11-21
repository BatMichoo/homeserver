package lan.homeserver.tv

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
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
        // After the SSL handler cancels, WebView may also report the load as
        // a failed handshake; it must still read as an untrusted server.
        assertEquals(LoadFailure.UNTRUSTED, LoadFailure.fromWebViewError(-11)) // ERROR_FAILED_SSL_HANDSHAKE
    }

    @Test
    fun untrustedIsNotDowngradedForTheSameUrl() {
        val url = "https://media.homeserver.lan/"
        // The generic error that follows a cancelled SSL load keeps the
        // "reinstall" message on screen.
        assertFalse(LoadFailure.replaces(LoadFailure.UNTRUSTED, url, LoadFailure.OTHER, url))
        assertFalse(LoadFailure.replaces(LoadFailure.UNTRUSTED, url, LoadFailure.OFFLINE, url))
        assertTrue(LoadFailure.replaces(LoadFailure.UNTRUSTED, url, LoadFailure.UNTRUSTED, url))
        // Another page, or no error on screen, shows the new failure.
        assertTrue(LoadFailure.replaces(LoadFailure.UNTRUSTED, url, LoadFailure.OTHER, "https://media.homeserver.lan/x"))
        assertTrue(LoadFailure.replaces(null, url, LoadFailure.OTHER, url))
        assertTrue(LoadFailure.replaces(LoadFailure.OFFLINE, url, LoadFailure.OTHER, url))
    }
}
