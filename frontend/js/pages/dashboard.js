// dashboard.js — Live "Recording Now" view: shows sources with an active session

import * as api from '../api.js';
import { escape, stateBadge, fmtDate, navigate,
         THUMB_REFRESH_MS, parseDurationToSecs, fmtDuration } from '../utils.js';

let _pollTimer  = null;
let _healthTimer = null;
let _tickTimer  = null;

export function cleanup() {
  if (_pollTimer)  { clearInterval(_pollTimer);  _pollTimer  = null; }
  if (_healthTimer){ clearInterval(_healthTimer); _healthTimer = null; }
  if (_tickTimer)  { clearInterval(_tickTimer);   _tickTimer  = null; }
}

export async function render(container) {
  cleanup();
  container.innerHTML = `
    <div class="page-header">
      <div>
        <div class="page-title">Dashboard</div>
        <div class="page-subtitle">Currently Recording</div>
      </div>
      <span id="dash-health-badge" style="display:none"></span>
    </div>
    <div id="dash-content"><div class="loading-wrap"><span class="spinner"></span> Loading…</div></div>
    <div class="last-updated" id="dash-updated"></div>`;

  async function pollHealth() {
    const badge = document.getElementById('dash-health-badge');
    if (!badge) return;
    const h = await api.healthCheck();
    if (h.httpOk && h.overseer_connected) {
      badge.style.display = 'none';
    } else if (h.httpStatus === 503 && h.overseer_connected === false) {
      badge.style.display = '';
      badge.className = 'badge badge-warning';
      badge.textContent = 'Overseer offline';
    } else if (!h.httpOk) {
      badge.style.display = '';
      badge.className = 'badge badge-error';
      badge.textContent = 'Backend unreachable';
    }
  }

  async function load() {
    try {
      const subs = await api.listSubscriptions();
      renderRecording(subs);
      document.getElementById('dash-updated').textContent = `Last updated: ${new Date().toLocaleTimeString()}`;
    } catch (err) {
      document.getElementById('dash-content').innerHTML =
        `<div class="alert alert-error">Failed to load: ${escape(err.message)}</div>`;
    }
  }

  function renderRecording(subs) {
    const el = document.getElementById('dash-content');

    // Show sources with an active session (session_active covers the full session:
    // segment boundaries and brief SLEEP periods are included — only SESSION END or
    // process exit clear this flag, so streamers that briefly go offline stay visible).
    const active = subs
      .filter(s => s.session_active)
      .sort((a, b) => {
        const ta = a.last_recording_at ? new Date(a.last_recording_at).getTime() : 0;
        const tb = b.last_recording_at ? new Date(b.last_recording_at).getTime() : 0;
        return tb - ta;
      });

    if (!active.length) {
      el.innerHTML = `
        <div class="empty-state">
          <div class="empty-state-icon">📡</div>
          <div class="empty-state-title">No sources recording right now</div>
          <div class="empty-state-text">Active recordings will appear here. <a href="#/subscriptions">Manage subscriptions →</a></div>
        </div>`;
      return;
    }

    el.innerHTML = `<div class="sub-grid" id="sub-grid"></div>`;
    const grid = el.querySelector('#sub-grid');
    // URL bucket: changes every THUMB_REFRESH_MS so the browser fetches a fresh thumbnail.
    const thumbBucket = Math.floor(Date.now() / THUMB_REFRESH_MS);

    active.forEach(sub => {
      const card = document.createElement('div');
      card.className = 'sub-card';
      const thumbSrc = `/thumbnails/${escape(sub.driver)}/${escape(sub.username)}.jpg?t=${thumbBucket}`;

      // Compute the ticker reference: prefer last_heartbeat + session_duration as the
      // most accurate anchor; fall back to last_recording_at as the segment start.
      const baseSecs = parseDurationToSecs(sub.session_duration);
      const refMs = sub.last_heartbeat
        ? new Date(sub.last_heartbeat).getTime()
        : (sub.last_recording_at ? new Date(sub.last_recording_at).getTime() : 0);

      // Initial displayed duration (will be ticked up every second by _tickTimer).
      const initSecs = refMs ? baseSecs + Math.max(0, Math.floor((Date.now() - refMs) / 1000)) : baseSecs;
      const durText = (baseSecs > 0 || refMs) ? ` ${fmtDuration(initSecs)}` : '';

      card.innerHTML = `
        <div class="sub-thumb">
          <img src="${thumbSrc}"
               onerror="this.parentElement.style.display='none'" loading="lazy" />
        </div>
        <div style="padding:1.1rem">
          <div class="sub-card-header">
            <div>
              <div class="sub-source">
                <a href="#/source/${escape(sub.driver)}/${escape(sub.username)}">${escape(sub.username)}</a>
              </div>
              <div class="sub-driver">${escape(sub.driver)}</div>
            </div>
          </div>
          <div class="sub-badges" style="margin:.4rem 0">
            <span class="badge badge-recording"
                  data-rec-base="${baseSecs}"
                  data-rec-ref="${refMs}">● REC${escape(durText)}</span>
          </div>
          <div class="sub-actions">
            <button class="btn btn-ghost btn-sm" data-action="pause">Pause</button>
          </div>
        </div>`;

      card.querySelectorAll('[data-action]').forEach(btn => {
        btn.addEventListener('click', () => handleAction(sub, btn.dataset.action, load));
      });
      grid.appendChild(card);
    });
  }

  // Tick the REC duration badges every second without re-fetching data.
  function tickDurations() {
    const now = Date.now();
    document.querySelectorAll('.badge-recording[data-rec-base]').forEach(el => {
      const base = parseInt(el.dataset.recBase || '0', 10);
      const ref  = parseInt(el.dataset.recRef  || '0', 10);
      if (!ref && !base) return;
      const elapsed = ref ? Math.max(0, Math.floor((now - ref) / 1000)) : 0;
      el.textContent = `● REC ${fmtDuration(base + elapsed)}`;
    });
  }

  await load();
  await pollHealth();
  _pollTimer   = setInterval(load, 5000);
  _healthTimer = setInterval(pollHealth, 5000);
  _tickTimer   = setInterval(tickDurations, 1000);
  tickDurations(); // immediate first tick
}

async function handleAction(sub, action, refresh) {
  try {
    if (action === 'pause') await api.pauseSubscription(sub.driver, sub.username);
    refresh();
  } catch (err) {
    alert(`Action failed: ${err.message}`);
  }
}
