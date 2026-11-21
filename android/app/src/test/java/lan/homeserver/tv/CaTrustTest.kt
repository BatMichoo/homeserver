package lan.homeserver.tv

import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import java.util.Date
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

// Test certificates in src/test/resources, generated once with OpenSSL (RSA
// 2048, ~100 years so the tests do not rot; the expired one is dated 2020).
// leaf-key.pem is committed because the updater's HTTPS test serves with it
// (OpenSSL 3+ writes keys as PKCS#8 already). ca-key.pem and the other keys
// and CSRs were thrown away.
//
//   openssl req -x509 -newkey rsa:2048 -nodes -keyout ca-key.pem -out ca.pem -days 36500 \
//     -subj "/CN=Test Home CA" -addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign,cRLSign
//   openssl req -x509 -newkey rsa:2048 -nodes -keyout oca-key.pem -out other-ca.pem -days 36500 \
//     -subj "/CN=Other Test CA" -addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign,cRLSign
//   printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\nbasicConstraints=CA:FALSE\n' > ext.cnf
//   openssl req -newkey rsa:2048 -nodes -keyout leaf-key.pem -out leaf.csr -subj "/CN=localhost"
//   openssl x509 -req -in leaf.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial -out leaf.pem -extfile ext.cnf -days 36500
//   openssl req -newkey rsa:2048 -nodes -keyout other-leaf-key.pem -out other-leaf.csr -subj "/CN=localhost"
//   openssl x509 -req -in other-leaf.csr -CA other-ca.pem -CAkey oca-key.pem -CAcreateserial -out other-leaf.pem -extfile ext.cnf -days 36500
//   openssl x509 -req -in leaf.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial -out expired-leaf.pem -extfile ext.cnf \
//     -days 365 -not_before 20200101000000Z -not_after 20210101000000Z
class CaTrustTest {
    private fun cert(name: String): X509Certificate =
        javaClass.getResourceAsStream("/$name")!!.use {
            CertificateFactory.getInstance("X.509").generateCertificate(it) as X509Certificate
        }

    private val ca = cert("ca.pem")

    @Test
    fun leafSignedByTheCaChains() {
        assertTrue(CaTrust.chainsTo(cert("leaf.pem"), ca))
    }

    @Test
    fun leafFromAnotherCaDoesNot() {
        assertFalse(CaTrust.chainsTo(cert("other-leaf.pem"), ca))
    }

    @Test
    fun leafDoesNotChainToAnotherCa() {
        assertFalse(CaTrust.chainsTo(cert("leaf.pem"), cert("other-ca.pem")))
    }

    @Test
    fun expiredLeafDoesNot() {
        assertFalse(CaTrust.chainsTo(cert("expired-leaf.pem"), ca))
    }

    @Test
    fun expiredCaDoesNot() {
        val farFuture = Date(Date().time + 200L * 365 * 24 * 3600 * 1000)
        assertFalse(CaTrust.chainsTo(cert("leaf.pem"), ca, farFuture))
    }
}
