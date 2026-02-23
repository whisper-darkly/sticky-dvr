// theme.js — theme + dark-mode management
//
// Cookies:   sdvr_theme  = "" | "fiesta" | "twilight" | "erotic"
//            sdvr_mode   = "light" | "dark" | "system"  (default: "system")
//
// Classes on <html>:
//   html                         → default / light
//   html.dark                    → default / dark
//   html.theme-fiesta            → fiesta / light
//   html.theme-fiesta.dark       → fiesta / dark
//   (same pattern for twilight, erotic)

const THEME_COOKIE = 'sdvr_theme';
const MODE_COOKIE  = 'sdvr_mode';
const THEMES = ['', 'fiesta', 'twilight', 'erotic'];
const MODES  = ['light', 'system', 'dark'];
const COOKIE_MAX_AGE = 60 * 60 * 24 * 365; // 1 year

// ---- Cookie helpers --------------------------------------------------------

function getCookie(name) {
  const m = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
  return m ? decodeURIComponent(m[1]) : '';
}

function setCookie(name, value) {
  document.cookie = `${name}=${encodeURIComponent(value)}; path=/; max-age=${COOKIE_MAX_AGE}; SameSite=Lax`;
}

// ---- State -----------------------------------------------------------------

function getCurrentTheme() {
  const v = getCookie(THEME_COOKIE);
  return THEMES.includes(v) ? v : '';
}

function getCurrentMode() {
  const v = getCookie(MODE_COOKIE);
  return MODES.includes(v) ? v : 'system';
}

function isDarkActive(mode) {
  if (mode === 'dark')   return true;
  if (mode === 'light')  return false;
  return window.matchMedia('(prefers-color-scheme: dark)').matches;
}

// ---- Apply to DOM ----------------------------------------------------------

export function applyTheme(theme, mode) {
  const root = document.documentElement;

  // remove old theme classes
  root.classList.remove('theme-fiesta', 'theme-twilight', 'theme-erotic');
  if (theme) root.classList.add('theme-' + theme);

  // apply/remove dark class
  root.classList.toggle('dark', isDarkActive(mode));
}

export function setTheme(theme) {
  setCookie(THEME_COOKIE, theme);
  applyTheme(theme, getCurrentMode());
  refreshPickerState();
}

export function setMode(mode) {
  setCookie(MODE_COOKIE, mode);
  applyTheme(getCurrentTheme(), mode);
  refreshPickerState();
}

// ---- System preference listener -------------------------------------------

let _mediaQuery = null;

function attachSystemListener() {
  if (_mediaQuery) _mediaQuery.removeEventListener('change', onSystemChange);
  _mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
  _mediaQuery.addEventListener('change', onSystemChange);
}

function onSystemChange() {
  if (getCurrentMode() === 'system') {
    applyTheme(getCurrentTheme(), 'system');
  }
}

// ---- Initial apply (called from inline script + on module load) ------------

export function initTheme() {
  applyTheme(getCurrentTheme(), getCurrentMode());
  attachSystemListener();
}

// ---- Theme picker UI -------------------------------------------------------

function refreshPickerState() {
  const theme = getCurrentTheme();
  const mode  = getCurrentMode();

  document.querySelectorAll('.tp-swatch').forEach(btn => {
    btn.classList.toggle('active', btn.dataset.theme === theme);
  });
  document.querySelectorAll('.tp-mode').forEach(btn => {
    btn.classList.toggle('active', btn.dataset.mode === mode);
  });
}

export function initThemePicker() {
  const picker  = document.getElementById('theme-picker');
  const toggleBtn = document.getElementById('tp-toggle');
  const panel   = document.getElementById('tp-panel');
  if (!picker || !toggleBtn || !panel) return;

  // toggle open/close
  toggleBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    const isOpen = panel.classList.contains('open');
    panel.classList.toggle('open', !isOpen);
    toggleBtn.classList.toggle('open', !isOpen);
  });

  // close on outside click
  document.addEventListener('click', onDocClick);

  // theme swatches
  picker.querySelectorAll('.tp-swatch').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      setTheme(btn.dataset.theme);
    });
  });

  // mode buttons
  picker.querySelectorAll('.tp-mode').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      setMode(btn.dataset.mode);
    });
  });

  refreshPickerState();
}

function onDocClick(e) {
  const panel   = document.getElementById('tp-panel');
  const picker  = document.getElementById('theme-picker');
  if (!panel || !picker) {
    document.removeEventListener('click', onDocClick);
    return;
  }
  if (!picker.contains(e.target)) {
    panel.classList.remove('open');
    const toggleBtn = document.getElementById('tp-toggle');
    if (toggleBtn) toggleBtn.classList.remove('open');
  }
}

// ---- Picker HTML -----------------------------------------------------------

export function themPickerHTML() {
  return `
  <div class="theme-picker" id="theme-picker">
    <button class="tp-btn" id="tp-toggle" aria-label="Appearance" title="Appearance">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="12" cy="12" r="4"/>
        <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/>
      </svg>
    </button>
    <div class="tp-panel" id="tp-panel">
      <div class="tp-section">
        <div class="tp-sct-label">Theme</div>
        <div class="tp-swatches">
          <button class="tp-swatch" data-theme="" title="Default">
            <span class="tp-dot"></span><span>Default</span>
          </button>
          <button class="tp-swatch" data-theme="fiesta" title="Fiesta">
            <span class="tp-dot tp-dot-fiesta"></span><span>Fiesta</span>
          </button>
          <button class="tp-swatch" data-theme="twilight" title="Twilight">
            <span class="tp-dot tp-dot-twilight"></span><span>Twilight</span>
          </button>
          <button class="tp-swatch" data-theme="erotic" title="Erotic">
            <span class="tp-dot tp-dot-erotic"></span><span>Erotic</span>
          </button>
        </div>
      </div>
      <div class="tp-divider"></div>
      <div class="tp-section">
        <div class="tp-sct-label">Mode</div>
        <div class="tp-mode-group">
          <button class="tp-mode" data-mode="light">☀ Light</button>
          <button class="tp-mode" data-mode="system">⊙ Auto</button>
          <button class="tp-mode" data-mode="dark">☽ Dark</button>
        </div>
      </div>
    </div>
  </div>`;
}
