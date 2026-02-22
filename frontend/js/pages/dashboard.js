// dashboard.js — Main dashboard with 5-second polling

import * as api from '../api.js';
import { escape, postureBadge, stateBadge, fmtDate, showModal, navigate } from '../utils.js';

function skeletonCards(count, cls) {
  return Array(count).fill(`<div class="skeleton ${cls}"></div>`).join('');
}

let _pollTimer = null;
let _healthTimer = null;

export function cleanup() {
  if (_pollTimer) { clearInterval(_pollTimer); _pollTimer = null; }
  if (_healthTimer) { clearInterval(_healthTimer); _healthTimer = null; }
}

export async function render(container) {
  cleanup();
  container.innerHTML = `
    <div class="page-header">
      <div>
        <div class="page-title">Dashboard</div>
        <div class="page-subtitle">Active recording sources</div>
      </div>
      <div style="display:flex;align-items:center;gap:.75rem">
        <span id="dash-health-badge" style="display:none"></span>
        <button class="btn btn-primary" id="dash-add">+ Add Subscription</button>
      </div>
    </div>
    <div class="stats-row" id="dash-stats"></div>
    <div id="dash-content">${skeletonCards(3, 'skeleton-card')}</div>
    <div class="last-updated" id="dash-updated"></div>`;

  container.querySelector('#dash-add').onclick = () => navigate('/subscriptions');

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
      renderStats(subs);
      renderSubs(subs);
      document.getElementById('dash-updated').textContent = `Last updated: ${new Date().toLocaleTimeString()}`;
    } catch (err) {
      document.getElementById('dash-content').innerHTML =
        `<div class="alert alert-error">Failed to load: ${escape(err.message)}</div>`;
    }
  }

  function renderStats(subs) {
    const total    = subs.length;
    const active   = subs.filter(s => s.posture === 'active').length;
    const paused   = subs.filter(s => s.posture === 'paused').length;
    const archived = subs.filter(s => s.posture === 'archived').length;

    document.getElementById('dash-stats').innerHTML = `
      <div class="stat-card"><div class="stat-num">${total}</div><div class="stat-label">Total</div></div>
      <div class="stat-card"><div class="stat-num" style="color:var(--primary)">${active}</div><div class="stat-label">Active</div></div>
      <div class="stat-card"><div class="stat-num" style="color:var(--warning)">${paused}</div><div class="stat-label">Paused</div></div>
      <div class="stat-card"><div class="stat-num" style="color:var(--text-muted)">${archived}</div><div class="stat-label">Archived</div></div>`;
  }

  function renderSubs(subs) {
    const el = document.getElementById('dash-content');
    if (!subs.length) {
      el.innerHTML = `
        <div class="empty-state">
          <div class="empty-state-icon">📺</div>
          <div class="empty-state-title">No subscriptions yet</div>
          <div class="empty-state-text">Add a subscription to start recording.</div>
        </div>`;
      return;
    }

    el.innerHTML = `<div class="sub-grid" id="sub-grid"></div>`;
    const grid = el.querySelector('#sub-grid');

    subs.forEach(sub => {
      const card = document.createElement('div');
      card.className = 'sub-card';
      card.innerHTML = `
        <div class="sub-card-header">
          <div>
            <div class="sub-source">
              <a href="#/source/${escape(sub.driver)}/${escape(sub.username)}">${escape(sub.username)}</a>
            </div>
            <div class="sub-driver">${escape(sub.driver)}</div>
          </div>
        </div>
        <div class="sub-badges">
          ${postureBadge(sub.posture)}
          ${sub.state ? stateBadge(sub.state) : ''}
        </div>
        <div class="sub-actions">
          ${sub.posture === 'active'   ? `<button class="btn btn-ghost btn-sm" data-action="pause">Pause</button>` : ''}
          ${sub.posture === 'paused'   ? `<button class="btn btn-ghost btn-sm" data-action="resume">Resume</button>` : ''}
          ${sub.posture !== 'archived' ? `<button class="btn btn-ghost btn-sm" data-action="archive">Archive</button>` : ''}
          ${sub.state === 'error'      ? `<button class="btn btn-ghost btn-sm" data-action="reset-error">Reset Error</button>` : ''}
          <button class="btn btn-danger btn-sm" data-action="delete">Delete</button>
        </div>`;

      card.querySelectorAll('[data-action]').forEach(btn => {
        btn.addEventListener('click', () => handleAction(sub, btn.dataset.action, load));
      });
      grid.appendChild(card);
    });
  }

  await load();
  await pollHealth();
  _pollTimer = setInterval(load, 5000);
  _healthTimer = setInterval(pollHealth, 5000);
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
