package lan.homeserver.tv

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
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

    @Test
    fun startPageIsTheMediaHome() {
        val urls = AppUrls("homeserver.lan", 443, 80)
        assertTrue(urls.isStart("https://media.homeserver.lan/"))
        assertTrue(urls.isStart("https://media.homeserver.lan"))
        assertTrue(urls.isStart("https://media.homeserver.lan/?x=1#top"))
        assertTrue(urls.isStart("https://MEDIA.homeserver.lan/"))
        assertFalse(urls.isStart("https://media.homeserver.lan/movies"))
        assertFalse(urls.isStart("https://homeserver.lan/"))
        assertFalse(urls.isStart("https://media.homeserver.lan:8443/"))
        assertFalse(urls.isStart("http://media.homeserver.lan/"))
        assertFalse(urls.isStart(null))
        assertFalse(urls.isStart("not a url"))

        val custom = AppUrls("homeserver.lan", 8443, 8080)
        assertTrue(custom.isStart("https://media.homeserver.lan:8443/"))
        assertFalse(custom.isStart("https://media.homeserver.lan/"))
    }
}
