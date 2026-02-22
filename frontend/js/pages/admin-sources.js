// admin-sources.js — All sources across all users (admin only)

import * as api from '../api.js';
import { escape, postureBadge, stateBadge, fmtDate, showModal } from '../utils.js';

export function cleanup() {}

let _sortCol = 'driver';
let _sortDir = 1;

export async function render(container) {
  container.innerHTML = `
    <div class="page-header">
      <div>
        <div class="page-title">All Sources</div>
        <div class="page-subtitle">All subscriptions across all users</div>
      </div>
      <button class="btn btn-ghost" id="src-refresh">Refresh</button>
    </div>
    <div id="sources-content"><div class="loading-wrap"><span class="spinner"></span> Loading…</div></div>`;

  container.querySelector('#src-refresh').onclick = load;

  let _allSubs = [];

  async function load() {
    const el = document.getElementById('sources-content');
    try {
      _allSubs = await api.listSubscriptions(); // admin sees all
      renderTable(_allSubs);
    } catch (err) {
      el.innerHTML = `<div class="alert alert-error">Failed to load: ${escape(err.message)}</div>`;
    }
  }

  function renderTable(subs) {
    const el = document.getElementById('sources-content');
    if (!subs.length) {
      el.innerHTML = `<div class="empty-state"><div class="empty-state-title">No sources found</div></div>`;
      return;
    }

    const sorted = [...subs].sort((a, b) => {
      const av = (a[_sortCol] || '').toString().toLowerCase();
      const bv = (b[_sortCol] || '').toString().toLowerCase();
      return av < bv ? -_sortDir : av > bv ? _sortDir : 0;
    });

    el.innerHTML = `
      <div class="card" style="padding:0;overflow:hidden">
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                ${col('driver', 'Driver')}
                ${col('username', 'Username')}
                ${col('posture', 'Posture')}
                ${col('worker_state', 'State')}
                ${col('created_at', 'Subscribed')}
                <th>Actions</th>
              </tr>
            </thead>
            <tbody id="src-tbody"></tbody>
          </table>
        </div>
      </div>`;

    // attach sort handlers
    el.querySelectorAll('th.sortable').forEach(th => {
      th.addEventListener('click', () => {
        const newCol = th.dataset.col;
        if (_sortCol === newCol) _sortDir *= -1;
        else { _sortCol = newCol; _sortDir = 1; }
        renderTable(subs);
      });
    });

    const tbody = document.getElementById('src-tbody');
    sorted.forEach(sub => {
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td>${escape(sub.driver)}</td>
        <td><a href="#/source/${escape(sub.driver)}/${escape(sub.username)}">${escape(sub.username)}</a></td>
        <td>${postureBadge(sub.posture)}</td>
        <td>${sub.worker_state ? stateBadge(sub.worker_state) : '<span style="color:var(--text-muted)">—</span>'}</td>
        <td>${fmtDate(sub.created_at)}</td>
        <td>
          <div style="display:flex;gap:.35rem;flex-wrap:wrap">
            ${sub.posture === 'active'       ? `<button class="btn btn-ghost btn-sm" data-action="pause">Pause</button>` : ''}
            ${sub.posture === 'paused'       ? `<button class="btn btn-ghost btn-sm" data-action="resume">Resume</button>` : ''}
            ${sub.posture !== 'archived'     ? `<button class="btn btn-ghost btn-sm" data-action="archive">Archive</button>` : ''}
            ${sub.worker_state === 'errored' ? `<button class="btn btn-ghost btn-sm" data-action="reset-error">Reset</button>` : ''}
            <button class="btn btn-danger btn-sm" data-action="delete">Delete</button>
          </div>
        </td>`;

      tr.querySelectorAll('[data-action]').forEach(btn => {
        btn.addEventListener('click', () => handleAction(sub, btn.dataset.action, load));
      });
      tbody.appendChild(tr);
    });
  }

  function col(key, label) {
    const active = _sortCol === key;
    const arrow  = active ? (_sortDir === 1 ? ' ↑' : ' ↓') : '';
    return `<th class="sortable" data-col="${key}">${label}${arrow}</th>`;
  }

  await load();
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
          await api.adminDeleteSubscription(sub.sub_id);
          refresh();
        } catch (err) {
          alert(`Delete failed: ${err.message}`);
        }
      },
    });
    return;
  }
  try {
    if (action === 'pause')       await api.adminPauseSubscription(sub.sub_id);
    if (action === 'resume')      await api.adminResumeSubscription(sub.sub_id);
    if (action === 'archive')     await api.adminArchiveSubscription(sub.sub_id);
    if (action === 'reset-error') await api.adminResetError(sub.sub_id);
    refresh();
  } catch (err) {
    alert(`Action failed: ${err.message}`);
  }
}
