package lan.homeserver.tv

import org.junit.Assert.assertEquals
import org.junit.Test

class AppUrlsTest {
    @Test
    fun defaultPortsAreOmitted() {
        val urls = AppUrls("homeserver.lan", 443, 80)
        assertEquals("https://media.homeserver.lan/", urls.start)
        assertEquals("https://homeserver.lan/tv.json", urls.updateInfo)
        assertEquals("http://homeserver.lan/tv.apk", urls.reinstall)
        assertEquals("https://homeserver.lan/tv.apk", urls.https("/tv.apk"))
    }

    @Test
    fun customPortsAreIncluded() {
        val urls = AppUrls("homeserver.lan", 8443, 8080)
        assertEquals("https://media.homeserver.lan:8443/", urls.start)
        assertEquals("https://homeserver.lan:8443/tv.json", urls.updateInfo)
        assertEquals("http://homeserver.lan:8080/tv.apk", urls.reinstall)
        assertEquals("https://homeserver.lan:8443/x", urls.https("/x"))
    }
}
