package lan.homeserver.tv

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdateScheduleTest {
    private val hour = 60 * 60 * 1000L

    @Test
    fun theFirstCheckIsAlwaysDue() {
        assertTrue(UpdateSchedule(6 * hour).due(now = 5))
    }

    @Test
    fun nextCheckOnlyAfterSixHours() {
        val s = UpdateSchedule(6 * hour)
        assertTrue(s.due(now = 0))
        assertFalse(s.due(now = 1 * hour))
        assertFalse(s.due(now = 6 * hour - 1))
        assertTrue(s.due(now = 6 * hour))
        assertFalse(s.due(now = 7 * hour))
        assertTrue(s.due(now = 12 * hour))
    }
}
