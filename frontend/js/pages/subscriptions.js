// subscriptions.js — Full subscription list with stats, sort, filter, add form

import * as api from '../api.js';
import { escape, postureBadge, stateBadge, fmtDate, showModal, DRIVERS } from '../utils.js';

let _pollTimer = null;

export function cleanup() {
  if (_pollTimer) { clearInterval(_pollTimer); _pollTimer = null; }
}

export async function render(container) {
  cleanup();

  container.innerHTML = `
    <div class="page-header">
      <div>
        <div class="page-title">Subscriptions</div>
        <div class="page-subtitle">All your recording sources</div>
      </div>
      <button class="btn btn-primary" id="subs-add-btn">+ Add Subscription</button>
    </div>

    <div id="subs-add-form" style="display:none" class="card" style="margin-bottom:1rem">
      <div class="card-title">Add Subscription</div>
      <div class="add-sub-form">
        <div class="form-group">
          <label class="form-label">Driver</label>
          <select class="form-control" id="add-driver">
            ${DRIVERS.map(d => `<option value="${d}">${d}</option>`).join('')}
          </select>
        </div>
        <div class="form-group">
          <label class="form-label">Username</label>
          <input type="text" class="form-control" id="add-username" placeholder="performer name" />
        </div>
        <button class="btn btn-primary" id="add-submit">Add</button>
        <button class="btn btn-ghost" id="add-cancel">Cancel</button>
      </div>
      <div id="add-error" class="form-error" style="margin-top:.5rem"></div>
    </div>

    <div class="stats-row" id="subs-stats"></div>

    <div class="subs-controls" style="display:flex;gap:.75rem;align-items:center;margin:.75rem 0;flex-wrap:wrap">
      <div class="form-group" style="margin:0;display:flex;align-items:center;gap:.4rem">
        <label class="form-label" style="margin:0;white-space:nowrap">Sort:</label>
        <select class="form-control" id="subs-sort" style="width:auto">
          <option value="name">Name A–Z</option>
          <option value="date">Date Added</option>
          <option value="session">Last Session</option>
          <option value="state">State</option>
        </select>
      </div>
      <div style="display:flex;gap:.4rem;align-items:center;flex-wrap:wrap">
        <span style="font-size:12px;color:var(--text-muted)">Filter:</span>
        ${['active','paused','archived'].map(p => `
          <label style="display:flex;align-items:center;gap:.25rem;font-size:13px;cursor:pointer">
            <input type="checkbox" class="filter-posture" value="${p}" checked> ${p}
          </label>`).join('')}
        <span style="font-size:12px;color:var(--text-muted);margin-left:.4rem">State:</span>
        ${['recording','idle','sleeping','errored'].map(s => `
          <label style="display:flex;align-items:center;gap:.25rem;font-size:13px;cursor:pointer">
            <input type="checkbox" class="filter-state" value="${s}" checked> ${s}
          </label>`).join('')}
      </div>
    </div>

    <div id="subs-content"><div class="loading-wrap"><span class="spinner"></span> Loading…</div></div>
    <div class="last-updated" id="subs-updated"></div>`;

  // Add form toggle
  container.querySelector('#subs-add-btn').onclick = () => {
    const form = document.getElementById('subs-add-form');
    form.style.display = form.style.display === 'none' ? '' : 'none';
    document.getElementById('add-username').value = '';
    document.getElementById('add-error').textContent = '';
  };
  container.querySelector('#add-cancel').onclick = () => {
    document.getElementById('subs-add-form').style.display = 'none';
  };
  container.querySelector('#add-submit').onclick = async () => {
    const driver   = document.getElementById('add-driver').value;
    const username = document.getElementById('add-username').value.trim();
    const errEl    = document.getElementById('add-error');
    if (!username) { errEl.textContent = 'Username is required'; return; }
    errEl.textContent = '';
    try {
      await api.createSubscription(driver, username);
      document.getElementById('subs-add-form').style.display = 'none';
      load();
    } catch (err) {
      errEl.textContent = err.message;
    }
  };

  // Sort/filter changes trigger re-render from cached data
  let _cached = [];
  container.querySelector('#subs-sort').onchange = () => renderFiltered(_cached);
  container.querySelectorAll('.filter-posture, .filter-state').forEach(cb => {
    cb.onchange = () => renderFiltered(_cached);
  });

  async function load() {
    try {
      _cached = await api.listSubscriptions();
      renderStats(_cached);
      renderFiltered(_cached);
      document.getElementById('subs-updated').textContent = `Last updated: ${new Date().toLocaleTimeString()}`;
    } catch (err) {
      document.getElementById('subs-content').innerHTML =
        `<div class="alert alert-error">Failed to load: ${escape(err.message)}</div>`;
    }
  }

  function renderStats(subs) {
    const total     = subs.length;
    const recording = subs.filter(s => s.recording_state === 'recording').length;
    const active    = subs.filter(s => s.posture === 'active').length;
    const paused    = subs.filter(s => s.posture === 'paused').length;
    const archived  = subs.filter(s => s.posture === 'archived').length;
    const errored   = subs.filter(s => s.worker_state === 'errored').length;

    document.getElementById('subs-stats').innerHTML = `
      <div class="stat-card"><div class="stat-num">${total}</div><div class="stat-label">Total</div></div>
      <div class="stat-card"><div class="stat-num" style="color:var(--danger)">${recording}</div><div class="stat-label">Recording</div></div>
      <div class="stat-card"><div class="stat-num" style="color:var(--primary)">${active}</div><div class="stat-label">Active</div></div>
      <div class="stat-card"><div class="stat-num" style="color:var(--warning)">${paused}</div><div class="stat-label">Paused</div></div>
      <div class="stat-card"><div class="stat-num" style="color:var(--text-muted)">${archived}</div><div class="stat-label">Archived</div></div>
      <div class="stat-card"><div class="stat-num" style="color:var(--danger)">${errored}</div><div class="stat-label">Errored</div></div>`;
  }

  function renderFiltered(subs) {
    const el = document.getElementById('subs-content');

    // Get active filters
    const postureFilters = new Set(
      [...document.querySelectorAll('.filter-posture:checked')].map(c => c.value)
    );
    const stateFilters = new Set(
      [...document.querySelectorAll('.filter-state:checked')].map(c => c.value)
    );

    let filtered = subs.filter(s => {
      const postureOk = postureFilters.has(s.posture);
      const state = s.recording_state || s.worker_state || 'idle';
      const stateOk = stateFilters.has(state);
      return postureOk && stateOk;
    });

    // Sort
    const sortBy = document.getElementById('subs-sort')?.value || 'name';
    filtered = [...filtered].sort((a, b) => {
      if (sortBy === 'name') return a.username.localeCompare(b.username);
      if (sortBy === 'date') return new Date(b.created_at) - new Date(a.created_at);
      if (sortBy === 'session') {
        const ta = a.last_recording_at ? new Date(a.last_recording_at).getTime() : 0;
        const tb = b.last_recording_at ? new Date(b.last_recording_at).getTime() : 0;
        return tb - ta;
      }
      if (sortBy === 'state') {
        const stateOrder = { recording: 0, idle: 1, sleeping: 2, errored: 3 };
        const sa = stateOrder[a.recording_state] ?? stateOrder[a.worker_state] ?? 9;
        const sb = stateOrder[b.recording_state] ?? stateOrder[b.worker_state] ?? 9;
        return sa - sb;
      }
      return 0;
    });

    if (!filtered.length) {
      el.innerHTML = `
        <div class="empty-state">
          <div class="empty-state-icon">📺</div>
          <div class="empty-state-title">No subscriptions match your filters</div>
          <div class="empty-state-text">Try adjusting the filter options above, or click "+ Add Subscription" to get started.</div>
        </div>`;
      return;
    }

    el.innerHTML = `<div class="sub-grid" id="sub-grid"></div>`;
    const grid = el.querySelector('#sub-grid');

    filtered.forEach(sub => {
      const card = document.createElement('div');
      card.className = 'sub-card';

      const isRecording = sub.recording_state === 'recording';
      const thumbSrc = `/thumbnails/${escape(sub.driver)}/${escape(sub.username)}.jpg` +
        (isRecording ? `?t=${Math.floor(Date.now() / 30000)}` : '');

      const sessionInfo = isRecording
        ? (sub.session_duration ? `<span class="badge badge-recording">● REC ${escape(sub.session_duration)}</span>` : `<span class="badge badge-recording">● REC</span>`)
        : (sub.last_recording_at ? `<div class="detail-meta">Last session: ${fmtDate(sub.last_recording_at)}</div>` : '');

      const canonicalLink = sub.canonical_url
        ? `<a href="${escape(sub.canonical_url)}" target="_blank" rel="noopener" class="btn btn-ghost btn-sm" style="font-size:11px">↗ Platform</a>`
        : '';

      card.innerHTML = `
        <div class="sub-thumb">
          <img src="${thumbSrc}" onerror="this.parentElement.style.display='none'" loading="lazy" />
        </div>
        <div style="padding:1.1rem">
          <div class="sub-card-header">
            <div>
              <div class="sub-source">
                <a href="#/source/${escape(sub.driver)}/${escape(sub.username)}">${escape(sub.username)}</a>
                ${canonicalLink}
              </div>
              <div class="sub-driver">${escape(sub.driver)}</div>
            </div>
          </div>
          <div class="sub-badges" style="margin:.4rem 0">
            ${postureBadge(sub.posture)}
            ${sub.worker_state ? stateBadge(sub.worker_state) : ''}
            ${sub.recording_state ? stateBadge(sub.recording_state) : ''}
          </div>
          ${sessionInfo}
          <div class="sub-actions" style="margin-top:.5rem">
            ${sub.posture === 'active'       ? `<button class="btn btn-ghost btn-sm" data-action="pause">Pause</button>` : ''}
            ${sub.posture === 'paused'       ? `<button class="btn btn-ghost btn-sm" data-action="resume">Resume</button>` : ''}
            ${sub.posture !== 'archived'     ? `<button class="btn btn-ghost btn-sm" data-action="archive">Archive</button>` : ''}
            ${sub.posture === 'archived'     ? `<button class="btn btn-ghost btn-sm" data-action="resume">Reactivate</button>` : ''}
            ${sub.worker_state === 'errored' ? `<button class="btn btn-ghost btn-sm" data-action="reset-error">Reset Error</button>` : ''}
            <button class="btn btn-danger btn-sm" data-action="delete">Delete</button>
          </div>
        </div>`;

      card.querySelectorAll('[data-action]').forEach(btn => {
        btn.addEventListener('click', () => handleAction(sub, btn.dataset.action, load));
      });
      grid.appendChild(card);
    });
  }

  await load();
  _pollTimer = setInterval(load, 5000);
}

async function handleAction(sub, action, refresh) {
  if (action === 'delete') {
    showModal({
      title: 'Delete subscription?',
      body: `Remove ${sub.driver}/${sub.username}? This cannot be undone.`,
      confirmLabel: 'Delete',
      confirmClass: 'btn-danger',
      onConfirm: async () => {
        try {
          await api.deleteSubscription(sub.driver, sub.username);
          refresh();
        } catch (err) {
          alert(`Delete failed: ${err.message}`);
        }
      },
    });
    return;
  }
  try {
    if (action === 'pause')       await api.pauseSubscription(sub.driver, sub.username);
    if (action === 'resume')      await api.resumeSubscription(sub.driver, sub.username);
    if (action === 'archive')     await api.archiveSubscription(sub.driver, sub.username);
    if (action === 'reset-error') await api.resetError(sub.driver, sub.username);
    refresh();
  } catch (err) {
    alert(`Action failed: ${err.message}`);
  }
}
