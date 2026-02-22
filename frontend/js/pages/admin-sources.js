// admin-sources.js — All sources across all users (admin only)

import * as api from '../api.js';
import { escape, postureBadge, stateBadge, fmtDate, showModal } from '../utils.js';

let _sortCol = 'driver';
let _sortDir = 1;
let _selectedIds   = new Set(); // sub_ids currently checked
let _filterPostures = new Set(); // empty = show all
let _filterStates   = new Set(); // empty = show all

export function cleanup() {
  _selectedIds.clear();
  _filterPostures.clear();
  _filterStates.clear();
}

export async function render(container) {
  // Reset selection on each page load; preserve sort.
  _selectedIds.clear();
  _filterPostures.clear();
  _filterStates.clear();

  container.innerHTML = `
    <div class="page-header">
      <div>
        <div class="page-title">All Sources</div>
        <div class="page-subtitle">All subscriptions across all users</div>
      </div>
      <button class="btn btn-ghost" id="src-refresh">Refresh</button>
    </div>

    <div class="filter-bar" id="src-filter-bar">
      <span class="filter-label">Posture</span>
      <button class="filter-chip" data-filter="posture" data-value="active">Active</button>
      <button class="filter-chip" data-filter="posture" data-value="paused">Paused</button>
      <button class="filter-chip" data-filter="posture" data-value="archived">Archived</button>
      <span class="filter-sep"></span>
      <span class="filter-label">State</span>
      <button class="filter-chip" data-filter="state" data-value="running">Running</button>
      <button class="filter-chip" data-filter="state" data-value="starting">Starting</button>
      <button class="filter-chip" data-filter="state" data-value="idle">Idle</button>
      <button class="filter-chip" data-filter="state" data-value="errored">Errored</button>
    </div>

    <div class="bulk-bar" id="src-bulk-bar">
      <span class="bulk-count" id="bulk-count">0 selected</span>
      <select class="bulk-select" id="bulk-action">
        <option value="">— Action —</option>
        <option value="resume">Start / Resume</option>
        <option value="pause">Pause</option>
        <option value="restart">Restart</option>
      </select>
      <button class="btn btn-primary btn-sm" id="bulk-apply" disabled>Apply to Selected</button>
      <span id="bulk-status"></span>
    </div>

    <div id="sources-content"><div class="loading-wrap"><span class="spinner"></span> Loading…</div></div>`;

  container.querySelector('#src-refresh').onclick = load;

  // Filter chips
  container.querySelector('#src-filter-bar').addEventListener('click', e => {
    const chip = e.target.closest('.filter-chip');
    if (!chip) return;
    const set = chip.dataset.filter === 'posture' ? _filterPostures : _filterStates;
    const val = chip.dataset.value;
    if (set.has(val)) set.delete(val);
    else set.add(val);
    chip.classList.toggle('active', set.has(val));
    renderTable(_allSubs);
  });

  // Bulk action dropdown enables/disables Apply button
  container.querySelector('#bulk-action').addEventListener('change', syncApplyBtn);

  // Apply to selected
  container.querySelector('#bulk-apply').addEventListener('click', () => {
    const action = container.querySelector('#bulk-action').value;
    if (!action || _selectedIds.size === 0) return;
    const targets = _allSubs.filter(s => _selectedIds.has(s.sub_id));
    applyBulk(action, targets);
  });

  let _allSubs = [];

  function syncApplyBtn() {
    const action = container.querySelector('#bulk-action')?.value;
    const btn = container.querySelector('#bulk-apply');
    if (btn) btn.disabled = _selectedIds.size === 0 || !action;
  }

  async function load() {
    try {
      _allSubs = await api.listSubscriptions(); // admin sees all
      renderTable(_allSubs);
    } catch (err) {
      document.getElementById('sources-content').innerHTML =
        `<div class="alert alert-error">Failed to load: ${escape(err.message)}</div>`;
    }
  }

  function visible(subs) {
    return subs.filter(s => {
      if (_filterPostures.size && !_filterPostures.has(s.posture)) return false;
      const st = s.worker_state || 'idle';
      if (_filterStates.size && !_filterStates.has(st)) return false;
      return true;
    });
  }

  function renderTable(subs) {
    const el = document.getElementById('sources-content');
    const shown = visible(subs);

    if (!shown.length) {
      el.innerHTML = `<div class="empty-state"><div class="empty-state-title">No sources match the current filters</div></div>`;
      updateBulkBar();
      return;
    }

    const sorted = [...shown].sort((a, b) => {
      const av = (a[_sortCol] || '').toString().toLowerCase();
      const bv = (b[_sortCol] || '').toString().toLowerCase();
      return av < bv ? -_sortDir : av > bv ? _sortDir : 0;
    });

    const allChecked = sorted.length > 0 && sorted.every(s => _selectedIds.has(s.sub_id));

    el.innerHTML = `
      <div class="card" style="padding:0;overflow:hidden">
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th class="col-check"><input type="checkbox" id="select-all" ${allChecked ? 'checked' : ''}></th>
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

    el.querySelector('#select-all').addEventListener('change', e => {
      if (e.target.checked) sorted.forEach(s => _selectedIds.add(s.sub_id));
      else sorted.forEach(s => _selectedIds.delete(s.sub_id));
      renderTable(subs);
    });

    el.querySelectorAll('th.sortable').forEach(th => {
      th.addEventListener('click', () => {
        if (_sortCol === th.dataset.col) _sortDir *= -1;
        else { _sortCol = th.dataset.col; _sortDir = 1; }
        renderTable(subs);
      });
    });

    const tbody = document.getElementById('src-tbody');
    sorted.forEach(sub => {
      const tr = document.createElement('tr');
      if (_selectedIds.has(sub.sub_id)) tr.classList.add('row-selected');

      tr.innerHTML = `
        <td class="col-check"><input type="checkbox" class="row-check" data-id="${sub.sub_id}" ${_selectedIds.has(sub.sub_id) ? 'checked' : ''}></td>
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
            <button class="btn btn-ghost btn-sm" data-action="restart">Restart</button>
            <button class="btn btn-danger btn-sm" data-action="delete">Delete</button>
          </div>
        </td>`;

      tr.querySelector('.row-check').addEventListener('change', e => {
        const id = parseInt(e.target.dataset.id, 10);
        if (e.target.checked) _selectedIds.add(id);
        else _selectedIds.delete(id);
        tr.classList.toggle('row-selected', e.target.checked);
        // Sync select-all state without full re-render.
        const sa = document.getElementById('select-all');
        if (sa) sa.checked = sorted.every(s => _selectedIds.has(s.sub_id));
        updateBulkBar();
      });

      tr.querySelectorAll('[data-action]').forEach(btn => {
        btn.addEventListener('click', () => handleAction(sub, btn.dataset.action, load));
      });

      tbody.appendChild(tr);
    });

    updateBulkBar();
  }

  function updateBulkBar() {
    const n = _selectedIds.size;
    const lbl = document.getElementById('bulk-count');
    if (lbl) lbl.textContent = n === 1 ? '1 selected' : `${n} selected`;
    syncApplyBtn();
  }

  async function applyBulk(action, targets) {
    const statusEl = document.getElementById('bulk-status');
    const labels = { resume: 'Start/Resume', pause: 'Pause', restart: 'Restart' };

    const run = async () => {
      container.querySelector('#bulk-apply').disabled = true;
      statusEl.innerHTML = `<span class="spinner"></span>`;

      let ok = 0, skipped = 0, failed = 0;
      await Promise.allSettled(targets.map(async sub => {
        try {
          if (action === 'pause') {
            if (sub.posture !== 'active') { skipped++; return; }
            await api.adminPauseSubscription(sub.sub_id);
          } else if (action === 'resume') {
            if (sub.posture !== 'paused') { skipped++; return; }
            await api.adminResumeSubscription(sub.sub_id);
          } else if (action === 'restart') {
            await api.adminRestartSubscription(sub.sub_id);
          }
          ok++;
        } catch { failed++; }
      }));

      const parts = [`${ok} succeeded`];
      if (skipped) parts.push(`${skipped} skipped`);
      if (failed) parts.push(`${failed} failed`);
      const cls = failed ? 'alert-error' : 'alert-success';
      statusEl.innerHTML = `<span class="alert ${cls}" style="padding:.2rem .6rem;font-size:.8125rem">${parts.join(' · ')}</span>`;
      await load();
    };

    if (action === 'restart') {
      showModal({
        title: `Restart ${targets.length} source${targets.length !== 1 ? 's' : ''}?`,
        body: `Stop and re-submit ${targets.length} source${targets.length !== 1 ? 's' : ''} with the current configuration. In-progress recordings will be interrupted.`,
        confirmLabel: 'Restart',
        confirmClass: 'btn-warning',
        onConfirm: run,
      });
    } else {
      await run();
    }
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
        try { await api.adminDeleteSubscription(sub.sub_id); refresh(); }
        catch (err) { alert(`Delete failed: ${err.message}`); }
      },
    });
    return;
  }
  if (action === 'restart') {
    showModal({
      title: 'Restart source?',
      body: `Restart ${sub.driver}/${sub.username}? The current recording will be interrupted.`,
      confirmLabel: 'Restart',
      confirmClass: 'btn-warning',
      onConfirm: async () => {
        try { await api.adminRestartSubscription(sub.sub_id); refresh(); }
        catch (err) { alert(`Restart failed: ${err.message}`); }
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
