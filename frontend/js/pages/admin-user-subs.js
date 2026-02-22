// admin-user-subs.js — Admin view of a specific user's subscriptions

import * as api from '../api.js';
import { escape, postureBadge, stateBadge, fmtDate, showModal } from '../utils.js';

export function cleanup() {}

export async function render(container, params) {
  const { userId } = params;

  container.innerHTML = `
    <div class="page-header">
      <div>
        <a href="#/admin/users" class="btn btn-ghost btn-sm" style="margin-bottom:.5rem">← Users</a>
        <div class="page-title" id="user-subs-title">Subscriptions</div>
        <div class="page-subtitle" id="user-subs-subtitle"></div>
      </div>
      <button class="btn btn-ghost" id="user-subs-refresh">Refresh</button>
    </div>
    <div id="user-subs-content"><div class="loading-wrap"><span class="spinner"></span> Loading…</div></div>`;

  container.querySelector('#user-subs-refresh').onclick = load;

  async function load() {
    try {
      const [user, subs] = await Promise.all([
        api.getUser(userId),
        api.adminGetUserSubscriptions(userId),
      ]);

      if (user) {
        document.getElementById('user-subs-title').textContent = `${user.username}'s Subscriptions`;
        document.getElementById('user-subs-subtitle').textContent =
          `${user.role} · ${subs.length} subscription(s)`;
      }

      renderSubs(subs);
    } catch (err) {
      document.getElementById('user-subs-content').innerHTML =
        `<div class="alert alert-error">Failed to load: ${escape(err.message)}</div>`;
    }
  }

  function renderSubs(subs) {
    const el = document.getElementById('user-subs-content');
    if (!subs.length) {
      el.innerHTML = `
        <div class="empty-state">
          <div class="empty-state-icon">📺</div>
          <div class="empty-state-title">No subscriptions</div>
          <div class="empty-state-text">This user has no subscriptions yet.</div>
        </div>`;
      return;
    }

    el.innerHTML = `<div class="sub-grid" id="user-sub-grid"></div>`;
    const grid = el.querySelector('#user-sub-grid');

    subs.forEach(sub => {
      const card = document.createElement('div');
      card.className = 'sub-card';
      card.innerHTML = `
        <div style="padding:1.1rem">
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
            ${sub.worker_state ? stateBadge(sub.worker_state) : ''}
            ${sub.recording_state ? stateBadge(sub.recording_state) : ''}
          </div>
          <div class="sub-actions">
            ${sub.posture === 'active'       ? `<button class="btn btn-ghost btn-sm" data-action="pause">Pause</button>` : ''}
            ${sub.posture === 'paused'       ? `<button class="btn btn-ghost btn-sm" data-action="resume">Resume</button>` : ''}
            ${sub.posture !== 'archived'     ? `<button class="btn btn-ghost btn-sm" data-action="archive">Archive</button>` : ''}
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
