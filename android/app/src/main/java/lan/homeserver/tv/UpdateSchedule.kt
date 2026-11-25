package lan.homeserver.tv

// When to ask the server for a newer app. The app checks on start and each
// time it comes back to the front, but at most once per interval: a TV keeps
// the app alive in the background for days (Home, standby), so a check only
// on start would rarely run, and one on every return would fetch tv.json each
// time someone switches inputs. `now` is a monotonic clock
// (SystemClock.elapsedRealtime), so changing the TV's time doesn't matter.
class UpdateSchedule(private val intervalMs: Long) {
    private var last: Long? = null

    // due says whether a check should run now, and if so counts it as run.
    fun due(now: Long): Boolean {
        val l = last
        if (l != null && now - l < intervalMs) return false
        last = now
        return true
    }
}
