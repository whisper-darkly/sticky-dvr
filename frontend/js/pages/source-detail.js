// source-detail.js — Per-source detail: events, logs, files

import * as api from '../api.js';
import { escape, postureBadge, stateBadge, eventBadge, fmtDate, fmtBytes, THUMB_REFRESH_MS } from '../utils.js';

let _detailThumbTimer = null;

export function cleanup() {
  if (_detailThumbTimer) { clearInterval(_detailThumbTimer); _detailThumbTimer = null; }
}

export async function render(container, params) {
  const { driver, username } = params;
  const isAdmin = (api.getSessionUser()?.role === 'admin');

  // Current subpath inside the source's media directory (for the file browser).
  let currentSubpath = '';

  container.innerHTML = `
    <div class="page-header">
      <div>
        <a href="#/" class="btn btn-ghost btn-sm" style="margin-bottom:.5rem">← Back</a>
        <div class="page-title" id="detail-title">${escape(username)}</div>
        <div class="page-subtitle">${escape(driver)}</div>
      </div>
      <button class="btn btn-ghost" id="detail-refresh">Refresh</button>
    </div>
    <div id="detail-content"><div class="loading-wrap"><span class="spinner"></span> Loading…</div></div>`;

  container.querySelector('#detail-refresh').onclick = load;

  async function load() {
    const el = document.getElementById('detail-content');
    try {
      const fetches = [
        api.getSubscription(driver, username),
        api.getSourceEvents(driver, username),
        api.getSourceLogs(driver, username),
      ];
      if (isAdmin) fetches.push(api.adminGetSourceSubscribers(driver, username));

      const results = await Promise.allSettled(fetches);
      const sub         = results[0].status === 'fulfilled' ? results[0].value : null;
      const events      = results[1].status === 'fulfilled' ? (results[1].value.events || []) : [];
      const logs        = results[2].status === 'fulfilled' ? (results[2].value.logs || []) : [];
      const subsResult  = (isAdmin && results[3]?.status === 'fulfilled') ? results[3].value : null;
      const subscribers = subsResult?.subscribers || [];

      // Update page header with canonical URL link if available.
      if (sub?.canonical_url) {
        const titleEl = document.getElementById('detail-title');
        if (titleEl && !titleEl.querySelector('a.canonical-link')) {
          titleEl.insertAdjacentHTML('afterend',
            `<a href="${escape(sub.canonical_url)}" target="_blank" rel="noopener"
               class="btn btn-ghost btn-sm canonical-link" style="margin-left:.5rem">↗ View on Platform</a>`);
        }
      }

      // Load files via media file server.
      let mediaEntries = [];
      try {
        mediaEntries = await api.listMediaFiles(driver, username, currentSubpath);
      } catch (_) {}

      // Build status card content.
      let statusExtra = '';
      if (sub) {
        if (sub.recording_state === 'recording') {
          const dur = sub.session_duration ? ` ${escape(sub.session_duration)}` : '';
          statusExtra = `<div style="margin-top:.5rem"><span class="badge badge-recording">● REC${dur}</span></div>`;
        } else if (sub.last_recording_at) {
          statusExtra = `<div class="detail-meta">Last recorded: ${fmtDate(sub.last_recording_at)}</div>`;
        }
      }

      // Thumbnail: shown always; cache-busted only when session is active so we
      // don't hammer the thumbnailer for sources that aren't currently recording.
      const thumbBucket = Math.floor(Date.now() / THUMB_REFRESH_MS);
      const thumbSrc = sub && sub.session_active
        ? `/thumbnails/${escape(driver)}/${escape(username)}.jpg?t=${thumbBucket}`
        : `/thumbnails/${escape(driver)}/${escape(username)}.jpg`;

      // Start or clear the thumbnail refresh timer based on session state.
      if (_detailThumbTimer) { clearInterval(_detailThumbTimer); _detailThumbTimer = null; }
      if (sub && sub.session_active) {
        _detailThumbTimer = setInterval(() => {
          const img = document.getElementById('detail-thumb-img');
          if (!img) { clearInterval(_detailThumbTimer); _detailThumbTimer = null; return; }
          const newBucket = Math.floor(Date.now() / THUMB_REFRESH_MS);
          img.src = `/thumbnails/${escape(driver)}/${escape(username)}.jpg?t=${newBucket}`;
        }, THUMB_REFRESH_MS);
      }

      el.innerHTML = `
        ${sub ? `
        <div class="card">
          <div class="card-title">Status</div>
          <div style="display:flex;gap:1rem;align-items:flex-start">
            <img id="detail-thumb-img" src="${thumbSrc}"
                 class="sub-thumb" style="width:160px;flex-shrink:0;border-radius:4px"
                 onerror="this.style.display='none'" loading="lazy" />
            <div style="min-width:0">
              <div style="display:flex;gap:.5rem;flex-wrap:wrap">
                ${postureBadge(sub.posture)}
                ${sub.worker_state ? stateBadge(sub.worker_state) : ''}
                ${sub.recording_state ? stateBadge(sub.recording_state) : ''}
              </div>
              ${statusExtra}
              <div style="margin-top:.75rem;font-size:12px;color:var(--text-muted)">
                Subscribed: ${fmtDate(sub.created_at)} · Updated: ${fmtDate(sub.updated_at)}
              </div>
            </div>
          </div>
        </div>` : ''}

        ${isAdmin && subscribers.length ? `
        <div class="card">
          <div class="card-title">Subscribers</div>
          <ul class="event-list">
            ${subscribers.map(s => `
              <li class="event-item">
                <a href="#/admin/users/${escape(String(s.user_id))}/subscriptions">${escape(s.username)}</a>
                ${postureBadge(s.posture)}
              </li>`).join('')}
          </ul>
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
          <div id="file-browser"></div>
        </div>`;

      renderFileBrowser(mediaEntries, driver, username);
    } catch (err) {
      el.innerHTML = `<div class="alert alert-error">Failed to load: ${escape(err.message)}</div>`;
    }
  }

  async function renderFileBrowser(entries, drv, uname) {
    const el = document.getElementById('file-browser');
    if (!el) return;

    // Build ".jpg names" set for thumbnail preview lookup
    const jpgNames = new Set(
      entries.filter(e => e.name.endsWith('.jpg')).map(e => e.name)
    );

    const dirs  = entries.filter(e => e.type === 'directory');
    const files = entries.filter(e => e.type === 'file' && !e.name.endsWith('.jpg'));

    if (!dirs.length && !files.length) {
      el.innerHTML = '<div class="empty-state" style="padding:1rem"><div class="empty-state-text">No files yet.</div></div>';
      return;
    }

    // Breadcrumb — only show subpath parts (not driver/source prefix)
    let breadcrumb = '';
    if (currentSubpath) {
      breadcrumb = `<a href="#" class="file-crumb" data-path="">Files</a>`;
      let accumulated = '';
      currentSubpath.split('/').filter(Boolean).forEach(part => {
        accumulated += (accumulated ? '/' : '') + part;
        const p = accumulated;
        breadcrumb += ` / <a href="#" class="file-crumb" data-path="${escape(p)}">${escape(part)}</a>`;
      });
    } else {
      breadcrumb = `<span style="color:var(--text-muted)">Files</span>`;
    }

    let html = `<div class="file-breadcrumb">${breadcrumb}</div>`;
    html += `<div class="table-wrap"><table><thead><tr>
      <th>Name</th><th>Size</th><th>Modified</th><th></th>
    </tr></thead><tbody>`;

    dirs.forEach(d => {
      const subpath = currentSubpath ? currentSubpath + '/' + d.name : d.name;
      html += `<tr>
        <td>
          <a href="#" class="file-dir-link" data-path="${escape(subpath)}">📁 ${escape(d.name)}/</a>
          <span class="file-stat-hint" id="stat-${escape(d.name)}"></span>
        </td>
        <td></td>
        <td>${fmtDate(d.mtime)}</td>
        <td></td>
      </tr>`;
    });

    files.forEach(f => {
      const nameNoExt = f.name.replace(/\.[^.]+$/, '');
      const hasThumb  = jpgNames.has(nameNoExt + '.jpg');
      const mediaPath = `/media/subscriptions/${drv}/${uname}/${currentSubpath ? currentSubpath + '/' : ''}${f.name}`;
      const thumbHtml = hasThumb
        ? `<img src="/thumbnails/${drv}/${uname}/${currentSubpath ? currentSubpath + '/' : ''}${nameNoExt}.jpg" class="file-thumb-preview" loading="lazy" />`
        : '';
      html += `<tr>
        <td>${thumbHtml}${escape(f.name)}</td>
        <td>${fmtBytes(f.size)}</td>
        <td>${fmtDate(f.mtime)}</td>
        <td><a href="${mediaPath}" target="_blank" class="btn btn-ghost btn-sm">Download</a></td>
      </tr>`;
    });

    html += `</tbody></table></div>`;
    el.innerHTML = html;

    // Wire up directory navigation
    el.querySelectorAll('.file-dir-link, .file-crumb').forEach(a => {
      a.addEventListener('click', async e => {
        e.preventDefault();
        currentSubpath = a.dataset.path;
        try {
          const newEntries = await api.listMediaFiles(drv, uname, currentSubpath);
          renderFileBrowser(newEntries, drv, uname);
        } catch (err) {
          el.innerHTML = `<div class="alert alert-error">Failed to load: ${escape(err.message)}</div>`;
        }
      });
    });

    // Fetch and overlay file stats for directory rows.
    if (dirs.length) {
      try {
        const stats = await api.getSourceFileStats(drv, uname, currentSubpath);
        if (stats?.children) {
          stats.children.forEach(child => {
            const hint = document.getElementById(`stat-${escape(child.name)}`);
            if (hint && child.type === 'directory') {
              const parts = [];
              if (child.total_bytes > 0) parts.push(fmtBytes(child.total_bytes));
              if (child.estimated_minutes > 0) parts.push(`~${child.estimated_minutes} min`);
              if (parts.length) hint.textContent = parts.join(' · ');
            }
          });
        }
      } catch (_) {}
    }
  }

  await load();
}

function renderEvents(events) {
  if (!events.length) return '<div class="empty-state" style="padding:1rem"><div class="empty-state-text">No events recorded yet.</div></div>';
  return `
    <ul class="event-list event-list-scroll">
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
