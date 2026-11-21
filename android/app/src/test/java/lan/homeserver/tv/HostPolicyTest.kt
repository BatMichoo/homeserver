package lan.homeserver.tv

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class HostPolicyTest {
    private val domain = "homeserver.lan"

    @Test
    fun allowsTheDomainAndItsSubdomains() {
        assertTrue(HostPolicy.allowed("homeserver.lan", domain))
        assertTrue(HostPolicy.allowed("media.homeserver.lan", domain))
        assertTrue(HostPolicy.allowed("AUTH.HomeServer.LAN", domain))
    }

    @Test
    fun rejectsEverythingElse() {
        assertFalse(HostPolicy.allowed("evilhomeserver.lan", domain))
        assertFalse(HostPolicy.allowed("homeserver.lan.evil.com", domain))
        assertFalse(HostPolicy.allowed("192.168.1.100", domain))
        assertFalse(HostPolicy.allowed("", domain))
        assertFalse(HostPolicy.allowed(null, domain))
        assertFalse(HostPolicy.allowed("example.com", domain))
    }
}
