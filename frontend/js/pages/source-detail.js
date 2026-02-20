// source-detail.js — Per-source detail: events, logs, files

import * as api from '../api.js';
import { escape, postureBadge, stateBadge, eventBadge, fmtDate, fmtBytes } from '../utils.js';

export function cleanup() {}

export async function render(container, params) {
  const { driver, username } = params;

  container.innerHTML = `
    <div class="page-header">
      <div>
        <a href="#/subscriptions" class="btn btn-ghost btn-sm" style="margin-bottom:.5rem">← Back</a>
        <div class="page-title">${escape(username)}</div>
        <div class="page-subtitle">${escape(driver)}</div>
      </div>
      <button class="btn btn-ghost" id="detail-refresh">Refresh</button>
    </div>
    <div id="detail-content"><div class="loading-wrap"><span class="spinner"></span> Loading…</div></div>`;

  container.querySelector('#detail-refresh').onclick = load;

  async function load() {
    const el = document.getElementById('detail-content');
    try {
      const [subRes, eventsRes, logsRes, filesRes] = await Promise.allSettled([
        api.getSubscription(driver, username),
        api.getSourceEvents(driver, username),
        api.getSourceLogs(driver, username),
        api.getSourceFiles(driver, username),
      ]);

      const sub    = subRes.status === 'fulfilled'    ? subRes.value    : null;
      const events = eventsRes.status === 'fulfilled' ? (eventsRes.value.events || []) : [];
      const logs   = logsRes.status === 'fulfilled'   ? (logsRes.value.logs || []) : [];
      const files  = filesRes.status === 'fulfilled'  ? (filesRes.value.files || []) : [];

      el.innerHTML = `
        ${sub ? `
        <div class="card">
          <div class="card-title">Status</div>
          <div style="display:flex;gap:.5rem;flex-wrap:wrap">
            ${postureBadge(sub.posture)}
            ${sub.state ? stateBadge(sub.state) : ''}
          </div>
          <div style="margin-top:.75rem;font-size:12px;color:var(--text-muted)">
            Subscribed: ${fmtDate(sub.created_at)} · Updated: ${fmtDate(sub.updated_at)}
          </div>
        </div>` : ''}

        <div class="detail-grid">
          <div>
            <div class="card">
              <div class="card-title">Recent Events</div>
              ${renderEvents(events)}
            </div>
          </div>
          <div>
            <div class="card">
              <div class="card-title">Logs</div>
              ${renderLogs(logs)}
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card-title">Files</div>
          ${renderFiles(files)}
        </div>`;
    } catch (err) {
      el.innerHTML = `<div class="alert alert-error">Failed to load: ${escape(err.message)}</div>`;
    }
  }

  await load();
}

function renderEvents(events) {
  if (!events.length) return '<div class="empty-state" style="padding:1rem"><div class="empty-state-text">No events recorded yet.</div></div>';
  return `
    <ul class="event-list">
      ${events.map(ev => `
        <li class="event-item">
          ${eventBadge(ev.event_type)}
          <span>PID ${escape(String(ev.pid))}${ev.exit_code != null ? ` · exit ${escape(String(ev.exit_code))}` : ''}</span>
          <span class="event-ts" style="margin-left:auto">${fmtDate(ev.ts)}</span>
        </li>`).join('')}
    </ul>`;
}

function renderLogs(logs) {
  if (!logs || (Array.isArray(logs) && !logs.length)) {
    return '<div class="empty-state" style="padding:1rem"><div class="empty-state-text">No logs available.</div></div>';
  }
  const text = Array.isArray(logs) ? logs.join('\n') : String(logs);
  return `<div class="log-box">${escape(text)}</div>`;
}

function renderFiles(files) {
  if (!files.length) {
    return '<div class="empty-state" style="padding:1rem"><div class="empty-state-text">No files available.</div></div>';
  }
  return `
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Filename</th>
            <th>Size</th>
            <th>Created</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          ${files.map(f => `
            <tr>
              <td>${escape(f.filename || f.name || '—')}</td>
              <td>${fmtBytes(f.size)}</td>
              <td>${fmtDate(f.created_at)}</td>
              <td>${f.url ? `<a href="${escape(f.url)}" target="_blank" class="btn btn-ghost btn-sm">Download</a>` : ''}</td>
            </tr>`).join('')}
        </tbody>
      </table>
    </div>`;
}
