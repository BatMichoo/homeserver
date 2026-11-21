package lan.homeserver.tv

import java.security.GeneralSecurityException
import java.security.cert.X509Certificate
import java.util.Date

// Whether the server's certificate still belongs to the CA baked into this
// app. When the server's CA is regenerated this turns false and the app tells
// the user to reinstall instead of failing with a blank page.
object CaTrust {
    fun chainsTo(leaf: X509Certificate, ca: X509Certificate, now: Date = Date()): Boolean = try {
        leaf.checkValidity(now)
        ca.checkValidity(now)
        leaf.verify(ca.publicKey)
        true
    } catch (e: GeneralSecurityException) {
        false
    }
}
