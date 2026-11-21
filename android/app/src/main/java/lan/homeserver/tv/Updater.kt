package lan.homeserver.tv

import android.app.PendingIntent
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import java.io.File
import java.io.IOException
import java.net.URL
import java.security.KeyStore
import java.security.SecureRandom
import java.security.cert.X509Certificate
import java.util.concurrent.atomic.AtomicBoolean
import javax.net.ssl.HttpsURLConnection
import javax.net.ssl.SSLContext
import javax.net.ssl.TrustManagerFactory
import kotlin.concurrent.thread

private const val TIMEOUT_MS = 10_000
private const val MAX_INFO_BYTES = 64 * 1024
const val MAX_APK_BYTES = 200L * 1024 * 1024

// An HTTPS connection that trusts ONLY the home CA (not the system store), so
// the update channel doesn't depend on the WebView or on any public CA. The
// host must be on the LAN domain, and redirects are never followed: the URL we
// vetted is the only one we talk to. Hostname matching against the certificate
// is left to the platform's default verifier (a custom one would replace it).
private fun openHome(url: String, homeCa: X509Certificate, allowedDomain: String): HttpsURLConnection {
    val u = URL(url)
    if (u.protocol != "https" || !HostPolicy.allowed(u.host, allowedDomain)) throw IOException("host not allowed")
    val store = KeyStore.getInstance(KeyStore.getDefaultType()).apply {
        load(null, null)
        setCertificateEntry("home-ca", homeCa)
    }
    val tmf = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm()).apply { init(store) }
    val ctx = SSLContext.getInstance("TLS").apply { init(null, tmf.trustManagers, null) }
    return (u.openConnection() as HttpsURLConnection).apply {
        sslSocketFactory = ctx.socketFactory
        instanceFollowRedirects = false
        connectTimeout = TIMEOUT_MS
        readTimeout = TIMEOUT_MS
    }
}

// InputStream.readNBytes needs API 33; the document is tiny, so read by hand.
private fun readCapped(input: java.io.InputStream, limit: Int): ByteArray {
    val out = java.io.ByteArrayOutputStream()
    val buf = ByteArray(4096)
    while (out.size() < limit) {
        val n = input.read(buf, 0, minOf(buf.size, limit - out.size()))
        if (n < 0) break
        out.write(buf, 0, n)
    }
    return out.toByteArray()
}

// Pure (no Android APIs) so a JVM test can run it. Null on any failure.
fun fetchUpdateInfo(url: String, homeCa: X509Certificate, allowedDomain: String): UpdateInfo? = try {
    val c = openHome(url, homeCa, allowedDomain)
    try {
        if (c.responseCode != 200) {
            null
        } else {
            val text = c.inputStream.use { readCapped(it, MAX_INFO_BYTES + 1) }
            if (text.size > MAX_INFO_BYTES) null else UpdateInfo.parse(String(text, Charsets.UTF_8))
                // The path comes from the server: only a plain absolute path on
                // this same server is accepted, never "//host" or a full URL.
                ?.takeIf { it.apkPath.startsWith("/") && !it.apkPath.startsWith("//") }
        }
    } finally {
        c.disconnect()
    }
} catch (e: IOException) {
    null
} catch (e: java.security.GeneralSecurityException) {
    null
}

// Downloads to `dest`, refusing anything over maxBytes. On any failure the
// partial file is removed.
fun downloadApk(url: String, homeCa: X509Certificate, allowedDomain: String, dest: File, maxBytes: Long): Boolean {
    var ok = false
    try {
        val c = openHome(url, homeCa, allowedDomain)
        try {
            if (c.responseCode == 200 && c.contentLengthLong <= maxBytes) {
                c.inputStream.use { input ->
                    dest.outputStream().use { out ->
                        val buf = ByteArray(64 * 1024)
                        var total = 0L
                        while (true) {
                            val n = input.read(buf)
                            if (n < 0) break
                            total += n
                            if (total > maxBytes) throw IOException("apk too large")
                            out.write(buf, 0, n)
                        }
                    }
                }
                ok = true
            }
        } finally {
            c.disconnect()
        }
    } catch (e: IOException) {
        ok = false
    } catch (e: java.security.GeneralSecurityException) {
        ok = false
    }
    if (!ok) dest.delete()
    return ok
}

// Checks the server for a newer build and installs it through PackageInstaller.
// Each home server signs its own APKs, so the install is an in-place update.
class Updater(private val context: Context, private val urls: AppUrls, private val homeCa: X509Certificate) {
    private val main = Handler(Looper.getMainLooper())
    private val busy = AtomicBoolean(false)

    // The activity is exported (it's the launcher), so the installer's result
    // intent is only believed if it carries this per-run secret.
    private val token = java.math.BigInteger(128, SecureRandom()).toString(16)

    fun check(callback: (UpdateInfo?) -> Unit) {
        thread(isDaemon = true) {
            val info = fetchUpdateInfo(urls.updateInfo, homeCa, BuildConfig.LAN_DOMAIN)
            main.post { callback(info) }
        }
    }

    // Call before showing any "busy" state: when the permission is missing this
    // opens Settings and returns false, and the caller stays idle so the user
    // can press Install again on coming back. onError gets the hint, or the
    // manual instructions if this TV has no such Settings screen.
    fun ensureInstallPermission(onError: (String) -> Unit): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O || context.packageManager.canRequestPackageInstalls()) return true
        onError(context.getString(R.string.update_allow_install))
        askForInstallPermission(onError)
        return false
    }

    fun install(info: UpdateInfo, onError: (String) -> Unit) {
        if (!busy.compareAndSet(false, true)) return
        thread(isDaemon = true) {
            val apk = File(context.cacheDir, APK_NAME)
            try {
                val ok = downloadApk(urls.https(info.apkPath), homeCa, BuildConfig.LAN_DOMAIN, apk, MAX_APK_BYTES)
                if (!ok) {
                    fail(context.getString(R.string.update_download_failed), onError)
                } else {
                    commit(apk)
                    busy.set(false)
                }
            } catch (e: IOException) {
                fail(context.getString(R.string.update_install_failed, e.message ?: ""), onError)
            } catch (e: SecurityException) {
                fail(context.getString(R.string.update_install_failed, e.message ?: ""), onError)
            } finally {
                apk.delete()
            }
        }
    }

    private fun fail(message: String, onError: (String) -> Unit) {
        busy.set(false)
        main.post { onError(message) }
    }

    private fun commit(apk: File) {
        val installer = context.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
        params.setSize(apk.length())
        val id = installer.createSession(params)
        installer.openSession(id).use { session ->
            apk.inputStream().use { input ->
                session.openWrite("tv.apk", 0, apk.length()).use { out ->
                    input.copyTo(out)
                    session.fsync(out)
                }
            }
            // Explicit (our own activity) so no other app can claim the result.
            // PackageInstaller fills in the status extras, so it must be mutable.
            val result = Intent(context, MainActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP)
                .putExtra(EXTRA_TOKEN, token)
            val flags = PendingIntent.FLAG_UPDATE_CURRENT or
                (if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) PendingIntent.FLAG_MUTABLE else 0)
            session.commit(PendingIntent.getActivity(context, id, result, flags).intentSender)
        }
    }

    // Called from MainActivity.onNewIntent. Returns true if the intent was the
    // installer's answer to our session.
    fun handleResult(intent: Intent, onError: (String) -> Unit): Boolean {
        if (intent.getStringExtra(EXTRA_TOKEN) != token) return false
        when (val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                @Suppress("DEPRECATION")
                val confirm = intent.getParcelableExtra<Intent>(Intent.EXTRA_INTENT)
                if (confirm != null) context.startActivity(confirm)
            }
            PackageInstaller.STATUS_SUCCESS -> {}
            else -> {
                val msg = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE) ?: "status $status"
                onError(context.getString(R.string.update_install_failed, msg))
            }
        }
        return true
    }

    @android.annotation.TargetApi(Build.VERSION_CODES.O)
    private fun askForInstallPermission(onError: (String) -> Unit) {
        try {
            context.startActivity(
                Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${context.packageName}")),
            )
        } catch (e: ActivityNotFoundException) {
            // Some TVs ship no such settings screen.
            onError(context.getString(R.string.update_unknown_sources))
        }
    }

    private companion object {
        const val APK_NAME = "tv-update.apk"
        const val EXTRA_TOKEN = "lan.homeserver.tv.UPDATE_TOKEN"
    }
}
