// subscriptions.js — Full subscription management

import * as api from '../api.js';
import { escape, postureBadge, stateBadge, fmtDate, showModal, navigate, DRIVERS } from '../utils.js';

function skeletonRows(count) {
  return Array(count).fill(`<div class="skeleton skeleton-row"></div>`).join('');
}

export function cleanup() {}

export async function render(container) {
  container.innerHTML = `
    <div class="page-header">
      <div>
        <div class="page-title">Subscriptions</div>
        <div class="page-subtitle">Manage your recording subscriptions</div>
      </div>
    </div>

    <div class="card">
      <div class="card-title">Add Subscription</div>
      <form id="add-sub-form">
        <div class="add-sub-form">
          <div class="form-group">
            <label class="form-label">Driver</label>
            <select class="form-control" id="new-driver">
              ${DRIVERS.map(d => `<option value="${d}">${d}</option>`).join('')}
            </select>
          </div>
          <div class="form-group" style="flex:2">
            <label class="form-label">Username</label>
            <input class="form-control" type="text" id="new-username" placeholder="streamer_name" />
          </div>
          <button class="btn btn-primary" type="submit" id="add-btn">Add</button>
        </div>
        <div id="add-error"></div>
      </form>
    </div>

    <div id="subs-content">${skeletonRows(4)}</div>`;

  const form = container.querySelector('#add-sub-form');
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const driver   = container.querySelector('#new-driver').value;
    const username = container.querySelector('#new-username').value.trim();
    const errEl    = container.querySelector('#add-error');
    errEl.innerHTML = '';
    if (!username) {
      errEl.innerHTML = '<div class="form-error">Username is required.</div>';
      return;
    }
    const btn = container.querySelector('#add-btn');
    btn.disabled = true;
    try {
      await api.createSubscription(driver, username);
      container.querySelector('#new-username').value = '';
      await loadSubs();
    } catch (err) {
      errEl.innerHTML = `<div class="form-error">${escape(err.message)}</div>`;
    } finally {
      btn.disabled = false;
    }
  });

  async function loadSubs() {
    try {
      const subs = await api.listSubscriptions();
      renderTable(subs);
    } catch (err) {
      document.getElementById('subs-content').innerHTML =
        `<div class="alert alert-error">Failed to load: ${escape(err.message)}</div>`;
    }
  }

  function renderTable(subs) {
    const el = document.getElementById('subs-content');
    if (!subs.length) {
      el.innerHTML = `
        <div class="empty-state">
          <div class="empty-state-icon">📋</div>
          <div class="empty-state-title">No subscriptions</div>
          <div class="empty-state-text">Use the form above to add your first subscription.</div>
        </div>`;
      return;
    }

    el.innerHTML = `
      <div class="card" style="padding:0;overflow:hidden">
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Source</th>
                <th>Driver</th>
                <th>Posture</th>
                <th>State</th>
                <th>Created</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody id="subs-tbody"></tbody>
          </table>
        </div>
      </div>`;

    const tbody = document.getElementById('subs-tbody');
    subs.forEach(sub => {
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td><a href="#/source/${escape(sub.driver)}/${escape(sub.username)}">${escape(sub.username)}</a></td>
        <td>${escape(sub.driver)}</td>
        <td>${postureBadge(sub.posture)}</td>
        <td>${sub.state ? stateBadge(sub.state) : '<span class="text-muted">—</span>'}</td>
        <td>${fmtDate(sub.created_at)}</td>
        <td>
          <div style="display:flex;gap:.35rem;flex-wrap:wrap">
            ${sub.posture === 'active'   ? `<button class="btn btn-ghost btn-sm" data-action="pause">Pause</button>` : ''}
            ${sub.posture === 'paused'   ? `<button class="btn btn-ghost btn-sm" data-action="resume">Resume</button>` : ''}
            ${sub.posture !== 'archived' ? `<button class="btn btn-ghost btn-sm" data-action="archive">Archive</button>` : ''}
            ${sub.state === 'error'      ? `<button class="btn btn-ghost btn-sm" data-action="reset-error">Reset</button>` : ''}
            <button class="btn btn-danger btn-sm" data-action="delete">Delete</button>
          </div>
        </td>`;

      tr.querySelectorAll('[data-action]').forEach(btn => {
        btn.addEventListener('click', () => handleAction(sub, btn.dataset.action, loadSubs));
      });
      tbody.appendChild(tr);
    });
  }

  await loadSubs();
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
        } catch (err) { alert(`Delete failed: ${err.message}`); }
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
  } catch (err) { alert(`Action failed: ${err.message}`); }
}
