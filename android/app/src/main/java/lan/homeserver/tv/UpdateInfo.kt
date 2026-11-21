package lan.homeserver.tv

import org.json.JSONObject

// The server's tv.json. Anything malformed parses to null so a bad document
// just means "no update" instead of a crash.
data class UpdateInfo(val versionCode: Int, val versionName: String, val apkPath: String) {
    fun newerThan(installed: Int): Boolean = versionCode > installed

    companion object {
        fun parse(json: String): UpdateInfo? = try {
            val o = JSONObject(json)
            // optInt/optString would coerce "7" to 7; require the real types.
            val code = o.get("versionCode") as? Int
            val name = o.get("versionName") as? String
            val apk = o.get("apk") as? String
            if (code == null || name == null || apk == null) null else UpdateInfo(code, name, apk)
        } catch (e: org.json.JSONException) {
            null
        }
    }
}
