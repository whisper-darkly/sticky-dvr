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

export function showModal({ title, body, confirmLabel = 'Confirm', confirmClass = 'btn-danger', onConfirm }) {
  const overlay = document.createElement('div');
  overlay.className = 'modal-overlay';
  overlay.innerHTML = `
    <div class="modal">
      <div class="modal-title">${escape(title)}</div>
      <div class="modal-body">${escape(body)}</div>
      <div class="modal-actions">
        <button class="btn btn-ghost" id="modal-cancel">Cancel</button>
        <button class="btn ${confirmClass}" id="modal-confirm">${escape(confirmLabel)}</button>
      </div>
    </div>`;
  document.body.appendChild(overlay);
  overlay.querySelector('#modal-cancel').onclick = () => overlay.remove();
  overlay.querySelector('#modal-confirm').onclick = () => { overlay.remove(); onConfirm(); };
}

export function navigate(hash) {
  window.location.hash = hash;
}

export function currentHash() {
  return window.location.hash.replace(/^#/, '') || '/';
}

export const DRIVERS = ['chaturbate', 'stripchat', 'bongacams', 'cam4', 'camsoda'];
