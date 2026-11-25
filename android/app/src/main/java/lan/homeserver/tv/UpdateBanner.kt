package lan.homeserver.tv

import android.annotation.SuppressLint
import android.content.Context
import android.graphics.Color
import android.view.Gravity
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView

// "Update available" bar at the bottom of the overlay, in the same plain
// native style as ErrorView. Install takes initial focus so OK on the remote
// is enough; Later is one D-pad press away.
@SuppressLint("ViewConstructor") // only ever built in code, with its actions
class UpdateBanner(
    context: Context,
    info: UpdateInfo,
    private val onInstall: () -> Unit,
    private val onLater: () -> Unit,
) : LinearLayout(context) {
    private val message = TextView(context)
    private val install = Button(context)
    private val later = Button(context)

    init {
        orientation = HORIZONTAL
        gravity = Gravity.CENTER_VERTICAL
        setBackgroundColor(Color.argb(0xEE, 0x20, 0x20, 0x20))
        val pad = (16 * resources.displayMetrics.density).toInt()
        setPadding(pad, pad, pad, pad)
        isClickable = true
        // Being clickable makes a view focusable by default (Android 8+), and
        // a focusable bar takes the focus itself when asked for it, leaving
        // Install unfocused and OK on the remote doing nothing. The bar only
        // swallows taps; the focus goes to its buttons.
        isFocusable = false

        message.setTextColor(Color.WHITE)
        message.textSize = 18f
        message.text = context.getString(R.string.update_available, info.versionName)
        install.setText(R.string.install)
        later.setText(R.string.later)
        install.setOnClickListener { onInstall() }
        later.setOnClickListener { onLater() }
        addView(message, LayoutParams(0, LayoutParams.WRAP_CONTENT, 1f))
        addView(install, LayoutParams(LayoutParams.WRAP_CONTENT, LayoutParams.WRAP_CONTENT))
        addView(later, LayoutParams(LayoutParams.WRAP_CONTENT, LayoutParams.WRAP_CONTENT))
    }

    fun setBusy() {
        install.isEnabled = false
        message.setText(R.string.update_downloading)
        later.requestFocus()
    }

    // An error leaves the banner up so Install can be tried again.
    fun showError(text: String) {
        install.isEnabled = true
        message.text = text
        install.requestFocus()
    }
}
