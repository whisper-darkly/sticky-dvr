// utils.js — shared utilities

export function fmtDate(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  return d.toLocaleString(undefined, {
    month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit',
  });
}

export function fmtDateShort(iso) {
  if (!iso) return '—';
  return new Date(iso).toLocaleString(undefined, {
    month: 'short', day: 'numeric',
    year: 'numeric',
  });
}

export function fmtBytes(bytes) {
  if (bytes == null || bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`;
}

export function escape(str) {
  if (str == null) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

export function postureBadge(posture) {
  const p = (posture || '').toLowerCase();
  return `<span class="badge badge-${p}">${escape(posture)}</span>`;
}

export function stateBadge(state) {
  const s = (state || 'idle').toLowerCase();
  return `<span class="badge badge-${s}">${escape(state || 'idle')}</span>`;
}

export function roleBadge(role) {
  const r = (role || 'user').toLowerCase();
  return `<span class="badge badge-${r}">${escape(role)}</span>`;
}

export function eventBadge(eventType) {
  const e = (eventType || '').toLowerCase();
  return `<span class="badge badge-${e}">${escape(eventType)}</span>`;
}

// showModal displays a confirmation dialog.
// Pass `body` for plain-text content (auto-escaped) or `bodyHtml` for raw HTML content.
// `onConfirm` receives the overlay element as its first argument, allowing callers to
// read form values from the modal before it is removed from the DOM.
export function showModal({ title, body, bodyHtml, confirmLabel = 'Confirm', confirmClass = 'btn-danger', onConfirm }) {
  const overlay = document.createElement('div');
  overlay.className = 'modal-overlay';
  const bodyContent = bodyHtml !== undefined ? bodyHtml : escape(body);
  overlay.innerHTML = `
    <div class="modal">
      <div class="modal-title">${escape(title)}</div>
      <div class="modal-body">${bodyContent}</div>
      <div class="modal-actions">
        <button class="btn btn-ghost" id="modal-cancel">Cancel</button>
        <button class="btn ${confirmClass}" id="modal-confirm">${escape(confirmLabel)}</button>
      </div>
    </div>`;
  document.body.appendChild(overlay);
  overlay.querySelector('#modal-cancel').onclick = () => overlay.remove();
  overlay.querySelector('#modal-confirm').onclick = () => { onConfirm(overlay); overlay.remove(); };
}

export function navigate(hash) {
  window.location.hash = hash;
}

export function currentHash() {
  return window.location.hash.replace(/^#/, '') || '/';
}

export const DRIVERS = ['chaturbate', 'stripchat', 'bongacams', 'cam4', 'camsoda'];

// How often thumbnails are refreshed for actively recording sources (ms).
// Used as a URL bucket (?t=N) so the browser re-fetches only when the bucket changes.
export const THUMB_REFRESH_MS = 30_000;

// Parse a Go duration string like "1h23m45s" or "5m30.5s" into fractional seconds.
export function parseDurationToSecs(s) {
  if (!s) return 0;
  let total = 0;
  const m = s.match(/(?:(\d+)h)?(?:(\d+)m)?(?:([\d.]+)s)?/);
  if (!m) return 0;
  if (m[1]) total += parseInt(m[1], 10) * 3600;
  if (m[2]) total += parseInt(m[2], 10) * 60;
  if (m[3]) total += parseFloat(m[3]);
  return total;
}

// Format integer seconds as HH:MM:SS, optionally prefixed with "DD days".
// Examples: "00:04:32", "1 day 01:23:45", "3 days 00:01:00"
export function fmtDuration(secs) {
  secs = Math.max(0, Math.floor(secs));
  const days = Math.floor(secs / 86400);
  const h    = Math.floor((secs % 86400) / 3600);
  const m    = Math.floor((secs % 3600) / 60);
  const s    = secs % 60;
  const hms  = `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
  if (days === 0) return hms;
  return `${days} ${days === 1 ? 'day' : 'days'} ${hms}`;
}
