// admin-diagnostics.js — System diagnostics: recorder, converter, thumbnailer metrics

import * as api from '../api.js';
import { escape } from '../utils.js';

let _interval = null;

export function cleanup() {
  if (_interval) { clearInterval(_interval); _interval = null; }
}

export async function render(container) {
  container.innerHTML = `
    <div class="page-header">
      <div>
        <div class="page-title">System Diagnostics</div>
        <div class="page-subtitle">Live pool and lifetime metrics for all worker services</div>
      </div>
      <div style="display:flex;align-items:center;gap:.75rem">
        <span class="last-updated" id="diag-updated"></span>
        <button class="btn btn-ghost" id="diag-refresh">Refresh</button>
      </div>
    </div>
    <div id="diag-content">
      <div class="loading-wrap"><span class="spinner"></span> Loading…</div>
    </div>`;

  container.querySelector('#diag-refresh').onclick = load;

  async function load() {
    try {
      const data = await api.getDiagnostics();
      renderCards(data);
      document.getElementById('diag-updated').textContent =
        'Updated ' + new Date().toLocaleTimeString();
    } catch (err) {
      document.getElementById('diag-content').innerHTML =
        `<div class="alert alert-error">Failed to load diagnostics: ${escape(err.message)}</div>`;
    }
  }

  await load();
  _interval = setInterval(load, 5000);
}

function renderCards(data) {
  const el = document.getElementById('diag-content');
  el.innerHTML = `
    <div class="diag-grid">
      ${renderServiceCard('Recorder',    data.recorder,    '#4f6ef7')}
      ${renderServiceCard('Converter',   data.converter,   '#8b5cf6')}
      ${renderServiceCard('Thumbnailer', data.thumbnailer, '#06b6d4')}
    </div>`;
}

function renderServiceCard(title, info, accentColor) {
  const badge = info.connected
    ? '<span class="badge badge-success">Connected</span>'
    : '<span class="badge badge-error">Disconnected</span>';

  const errorHtml = (!info.connected && info.error)
    ? `<div class="diag-error">${escape(info.error)}</div>`
    : '';

  const poolHtml   = info.pool    ? renderPool(info.pool)       : '';
  const metricsHtml = info.metrics ? renderMetrics(info.metrics) : '';

  return `
    <div class="card diag-card" style="border-top-color:${accentColor}">
      <div class="diag-card-header">
        <span class="card-title">${escape(title)}</span>
        ${badge}
      </div>
      ${errorHtml}
      ${poolHtml}
      ${metricsHtml}
    </div>`;
}

function renderPool(p) {
  const used    = p.running    ?? 0;
  const limit   = p.limit      ?? 0;
  const queued  = p.queue_depth ?? 0;
  const idle    = Math.max(0, limit - used);
  const pct     = limit > 0 ? Math.round((used / limit) * 100) : 0;

  // Fill colour: green < 70%, amber < 90%, red ≥ 90%
  const barColor = pct >= 90 ? 'var(--danger)' : pct >= 70 ? 'var(--warning)' : 'var(--success)';

  return `
    <div class="diag-section">
      <div class="diag-section-label">Pool</div>
      <div class="diag-pool-row">
        <div class="diag-pool-stats">
          ${poolStat(used,   'Running')}
          ${poolStat(idle,   'Idle')}
          ${poolStat(queued, 'Queued')}
          ${poolStat(limit,  'Limit')}
        </div>
        <div class="diag-pool-bar-wrap">
          <div class="diag-pool-bar">
            <div class="diag-pool-bar-fill" style="width:${pct}%;background:${barColor}"></div>
          </div>
          <div class="diag-pool-bar-label">${used} / ${limit} workers</div>
        </div>
      </div>
    </div>`;
}

function poolStat(value, label) {
  return `
    <div class="diag-pool-stat">
      <div class="stat-num">${value}</div>
      <div class="stat-label">${label}</div>
    </div>`;
}

function renderMetrics(m) {
  return `
    <div class="diag-section">
      <div class="diag-section-label">Lifetime Counters</div>
      <div class="stats-row diag-metrics">
        ${statCard(m.tasks_started,   'Started')}
        ${statCard(m.tasks_completed, 'Completed')}
        ${statCard(m.tasks_errored,   'Errored')}
        ${statCard(m.tasks_restarted, 'Restarted')}
      </div>
    </div>`;
}

function statCard(value, label) {
  return `
    <div class="stat-card">
      <div class="stat-num">${value ?? 0}</div>
      <div class="stat-label">${label}</div>
    </div>`;
}
