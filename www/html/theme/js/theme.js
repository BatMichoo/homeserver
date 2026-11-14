// The theme's behaviour: light/dark switch, navigation highlight, the side
// sheet (#sheet), toasts (#toast) and the settings menu. Lifted from
// Expenser's main.js. Functions the templates call are global and start
// with "theme".

// --- Light/dark -------------------------------------------------------
// The choice is a cookie, so the server renders the right mode on the next
// page load. Its domain comes from <body data-theme-cookie-domain>; empty
// means this host only.
function themeSetMode(mode) {
  const dark = mode === "dark";
  document.body.classList.toggle("dark", dark);
  const domain = document.body.dataset.themeCookieDomain;
  document.cookie =
    "homeserver_theme=" + (dark ? "dark" : "light") +
    "; path=/; max-age=31536000; SameSite=Lax" +
    (domain ? "; domain=" + domain : "");
}

document.addEventListener("click", (event) => {
  const option = event.target.closest(".theme-option");
  if (option) {
    themeSetMode(option.dataset.theme);
  }
});

// --- Navigation highlight: the button whose data-path is the current page
function themeMarkActiveNav() {
  const path = window.location.pathname.replace(/\/$/, "");
  document.querySelectorAll(".nav-button").forEach((button) => {
    const buttonPath = button.getAttribute("data-path");
    const active =
      buttonPath === path || (buttonPath === "/" && path === "");
    button.classList.toggle("active", active);
  });
}

document.body.addEventListener("htmx:afterSwap", themeMarkActiveNav);
document.body.addEventListener("htmx:historyCacheMiss", themeMarkActiveNav);
document.body.addEventListener("htmx:historyRestore", themeMarkActiveNav);
themeMarkActiveNav();

// --- Side sheet ---------------------------------------------------------
// Anything swapped into #sheet opens it (hx-on::after-settle on #sheet).
function themeShowSheet() {
  const sheet = document.getElementById("sheet");
  const backdrop = document.getElementById("backdrop");
  if (!sheet || !backdrop) {
    return;
  }
  backdrop.classList.add("blurred-content");
  sheet.show();
}

function themeHideSheet() {
  const sheet = document.getElementById("sheet");
  const backdrop = document.getElementById("backdrop");
  if (!sheet || !backdrop) {
    return;
  }
  backdrop.classList.remove("blurred-content");
  sheet.close();
  sheet.textContent = "";
}

document.getElementById("backdrop")?.addEventListener("click", themeHideSheet);

// --- Toasts ---------------------------------------------------------------
// A toast closes itself after its duration; the progress bar counts down.
function themeStartCountdown(progress, toast, durationMs) {
  const intervalMs = 10;
  const max = (durationMs || 5000) / intervalMs;
  let value = max;
  progress.max = max;
  progress.value = max;
  if (progress.countdownTimer) {
    clearInterval(progress.countdownTimer);
  }
  const timer = setInterval(() => {
    value -= 1;
    if (value < 0) {
      clearInterval(timer);
      progress.value = 0;
      if (toast && toast.open) {
        toast.close();
        toast.textContent = "";
      }
      return;
    }
    progress.value = value;
  }, intervalMs);
  progress.countdownTimer = timer;
}

function themeShowToast(toast, durationMs) {
  toast.show();
  const close = document.getElementById("toast-close");
  if (close) {
    close.focus();
  }
  const progress = toast.querySelector("#countdown-progress");
  if (progress) {
    themeStartCountdown(progress, toast, durationMs);
  }
}

function themeHideToast() {
  const toast = document.getElementById("toast");
  if (!toast) {
    return;
  }
  const progress = toast.querySelector("#countdown-progress");
  if (progress && progress.countdownTimer) {
    clearInterval(progress.countdownTimer);
  }
  toast.close();
  toast.textContent = "";
}

// --- Settings menu: close on outside click, after an action, or Escape ----
document.addEventListener("click", (event) => {
  document.querySelectorAll("details.settings-menu[open]").forEach((menu) => {
    const inside = menu.contains(event.target);
    const action = event.target.closest(".settings-action");
    if (!inside || action) {
      menu.removeAttribute("open");
    }
  });
});

// Escape closes the open sheet first, then any open settings menu.
document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape" || event.defaultPrevented) {
    return;
  }
  const sheet = document.getElementById("sheet");
  if (sheet && sheet.open) {
    themeHideSheet();
    return;
  }
  document
    .querySelectorAll("details.settings-menu[open]")
    .forEach((menu) => menu.removeAttribute("open"));
});

// A required field folded inside a closed <details> can't be focused by the
// browser's validation, so open it when one of its fields is invalid.
document.addEventListener(
  "invalid",
  (event) => {
    const details = event.target.closest("details");
    if (details && !details.open) {
      details.open = true;
    }
  },
  true,
);
