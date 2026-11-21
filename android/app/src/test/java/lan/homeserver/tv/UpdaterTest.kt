package lan.homeserver.tv

import java.io.File
import java.io.IOException
import java.net.InetAddress
import java.security.KeyFactory
import java.security.KeyStore
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import java.security.spec.PKCS8EncodedKeySpec
import java.util.Base64
import javax.net.ssl.KeyManagerFactory
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLServerSocket
import kotlin.concurrent.thread
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

class UpdaterTest {
    private fun resource(name: String) = javaClass.classLoader!!.getResourceAsStream(name)!!.readBytes()
    private fun cert(name: String) =
        CertificateFactory.getInstance("X.509").generateCertificate(resource(name).inputStream()) as X509Certificate

    // A bare-bones HTTPS server on an SSLServerSocket: the Kotlin/Android test
    // classpath can't see com.sun.net.httpserver, and this is all we need.
    private class Route(val status: Int, val body: String, val headers: Map<String, String> = emptyMap())
    private lateinit var socket: SSLServerSocket
    private val routes = HashMap<String, Route>()
    private val port get() = socket.localPort

    @Before
    fun start() {
        val pem = String(resource("leaf-key.pem")).lines().filter { !it.startsWith("-----") }.joinToString("")
        val key = KeyFactory.getInstance("RSA").generatePrivate(PKCS8EncodedKeySpec(Base64.getDecoder().decode(pem)))
        val ks = KeyStore.getInstance("PKCS12").apply {
            load(null, null)
            setKeyEntry("leaf", key, "x".toCharArray(), arrayOf(cert("leaf.pem")))
        }
        val kmf = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm()).apply { init(ks, "x".toCharArray()) }
        val ctx = SSLContext.getInstance("TLS").apply { init(kmf.keyManagers, null, null) }
        socket = ctx.serverSocketFactory.createServerSocket(0, 10, InetAddress.getByName("127.0.0.1")) as SSLServerSocket
        routes["/tv.json"] = Route(200, """{"versionCode":7,"versionName":"7-abc","apk":"/tv.apk"}""")
        routes["/evil.json"] = Route(200, """{"versionCode":7,"versionName":"7","apk":"https://evil.example/x.apk"}""")
        routes["/proto.json"] = Route(200, """{"versionCode":7,"versionName":"7","apk":"//evil.example/x.apk"}""")
        routes["/redirect.json"] = Route(302, "", mapOf("Location" to "/tv.json"))
        routes["/tv.apk"] = Route(200, "0123456789")
        thread(isDaemon = true) {
            while (!socket.isClosed) {
                try {
                    socket.accept().use { c ->
                        val reader = c.getInputStream().bufferedReader()
                        val path = reader.readLine().split(" ")[1]
                        while (reader.readLine().orEmpty().isNotEmpty()) { /* skip headers */ }
                        val r = routes[path] ?: Route(404, "")
                        val head = StringBuilder("HTTP/1.1 ${r.status} X\r\nContent-Length: ${r.body.length}\r\nConnection: close\r\n")
                        r.headers.forEach { (k, v) -> head.append("$k: $v\r\n") }
                        c.getOutputStream().apply { write((head.toString() + "\r\n" + r.body).toByteArray()); flush() }
                    }
                } catch (e: IOException) {
                    // a client that rejected our certificate just hangs up
                }
            }
        }
    }

    @After
    fun stop() = socket.close()

    private fun url(path: String, host: String = "localhost") = "https://$host:$port$path"

    @Test
    fun fetchesTheUpdateInfo() {
        assertEquals(UpdateInfo(7, "7-abc", "/tv.apk"), fetchUpdateInfo(url("/tv.json"), cert("ca.pem"), "localhost"))
    }

    @Test
    fun aCertificateFromAnotherCaIsNotTrusted() {
        assertNull(fetchUpdateInfo(url("/tv.json"), cert("other-ca.pem"), "localhost"))
    }

    @Test
    fun aMissingDocumentIsNoUpdate() {
        assertNull(fetchUpdateInfo(url("/nope.json"), cert("ca.pem"), "localhost"))
    }

    @Test
    fun redirectsAreNotFollowed() {
        assertNull(fetchUpdateInfo(url("/redirect.json"), cert("ca.pem"), "localhost"))
    }

    @Test
    fun aHostOutsideTheDomainIsRefused() {
        assertNull(fetchUpdateInfo(url("/tv.json", "127.0.0.1"), cert("ca.pem"), "localhost"))
    }

    @Test
    fun anApkPathThatIsNotOnThisServerIsNoUpdate() {
        assertNull(fetchUpdateInfo(url("/evil.json"), cert("ca.pem"), "localhost"))
        assertNull(fetchUpdateInfo(url("/proto.json"), cert("ca.pem"), "localhost"))
    }

    @Test
    fun downloadsTheApk() {
        val f = File.createTempFile("tvtest", ".apk")
        try {
            assertTrue(downloadApk(url("/tv.apk"), cert("ca.pem"), "localhost", f, 100))
            assertEquals("0123456789", f.readText())
        } finally {
            f.delete()
        }
    }

    @Test
    fun anOversizedApkIsRejectedAndRemoved() {
        val f = File.createTempFile("tvtest", ".apk")
        try {
            assertFalse(downloadApk(url("/tv.apk"), cert("ca.pem"), "localhost", f, 5))
            assertFalse(f.exists())
        } finally {
            f.delete()
        }
    }
}
