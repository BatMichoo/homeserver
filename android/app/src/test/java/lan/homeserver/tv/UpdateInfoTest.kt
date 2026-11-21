package lan.homeserver.tv

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdateInfoTest {
    @Test
    fun parsesTheServerDocument() {
        val info = UpdateInfo.parse("""{"versionCode":7,"versionName":"7-abc1234","apk":"/tv.apk"}""")
        assertEquals(UpdateInfo(7, "7-abc1234", "/tv.apk"), info)
    }

    @Test
    fun ignoresExtraFields() {
        val info = UpdateInfo.parse("""{"versionCode":7,"versionName":"7","apk":"/tv.apk","note":"x"}""")
        assertEquals(UpdateInfo(7, "7", "/tv.apk"), info)
    }

    @Test
    fun rejectsMalformedDocuments() {
        assertNull(UpdateInfo.parse("not json"))
        assertNull(UpdateInfo.parse("{}"))
        assertNull(UpdateInfo.parse("""{"versionCode":"7"}"""))
    }

    @Test
    fun newerThanComparesVersionCodes() {
        val info = UpdateInfo(7, "7", "/tv.apk")
        assertTrue(info.newerThan(6))
        assertFalse(info.newerThan(7))
        assertFalse(info.newerThan(8))
    }
}
