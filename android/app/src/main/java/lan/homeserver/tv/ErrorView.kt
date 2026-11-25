package lan.homeserver.tv

import android.annotation.SuppressLint
import android.content.Context
import android.graphics.Color
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView

// Opaque full-screen message shown over the WebView when the page can't load.
// Built in code: it is one text and one button. The button takes focus so the
// TV remote's OK key retries without any navigation first.
@SuppressLint("ViewConstructor") // only ever built in code, with its retry action
class ErrorView(context: Context, onRetry: () -> Unit) : LinearLayout(context) {
    private val message = TextView(context)
    private val retry = Button(context)

    init {
        orientation = VERTICAL
        gravity = Gravity.CENTER
        setBackgroundColor(Color.BLACK)
        val pad = (32 * resources.displayMetrics.density).toInt()
        setPadding(pad, pad, pad, pad)
        // Swallow touches so the dead page underneath can't be tapped. Not
        // focusable, though clickable views are by default: the remote's
        // focus belongs on Retry, not on the black screen around it.
        isClickable = true
        isFocusable = false
        visibility = View.GONE

        message.setTextColor(Color.WHITE)
        message.textSize = 20f
        message.gravity = Gravity.CENTER
        retry.setText(R.string.retry)
        retry.setOnClickListener { onRetry() }
        addView(message, LayoutParams(LayoutParams.MATCH_PARENT, LayoutParams.WRAP_CONTENT))
        addView(retry, LayoutParams(LayoutParams.WRAP_CONTENT, LayoutParams.WRAP_CONTENT).apply {
            topMargin = pad
        })
    }

    val showing: Boolean get() = visibility == View.VISIBLE

    // What is on screen, or null when hidden.
    var failure: LoadFailure? = null
        private set

    fun show(failure: LoadFailure, reinstallUrl: String) {
        this.failure = failure
        message.text = when (failure) {
            LoadFailure.OFFLINE -> context.getString(R.string.error_offline)
            LoadFailure.UNTRUSTED -> context.getString(R.string.error_untrusted, reinstallUrl)
            LoadFailure.OTHER -> context.getString(R.string.error_other)
        }
        visibility = View.VISIBLE
        retry.requestFocus()
    }

    fun hide() {
        failure = null
        visibility = View.GONE
    }
}
