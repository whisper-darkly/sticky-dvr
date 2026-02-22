// profile.js — User profile: view info, change password

import * as api from '../api.js';
import { escape } from '../utils.js';

export function cleanup() {}

export async function render(container) {
  const user = api.getSessionUser();

  container.innerHTML = `
    <div class="page-header">
      <div>
        <div class="page-title">Profile</div>
        <div class="page-subtitle">Your account settings</div>
      </div>
    </div>

    <div class="card" style="max-width:480px">
      <div class="card-title">Account Info</div>
      <div style="margin-bottom:.75rem">
        <div class="form-label">Username</div>
        <div style="font-size:1rem;font-weight:600">${escape(user?.username || '—')}</div>
      </div>
      <div>
        <div class="form-label">Role</div>
        <div>${user?.role === 'admin' ? '<span class="badge badge-admin">admin</span>' : '<span class="badge">user</span>'}</div>
      </div>
    </div>

    <div class="card" style="max-width:480px;margin-top:1rem">
      <div class="card-title">Change Password</div>
      <div id="pw-alert"></div>
      <form id="pw-form">
        <div class="form-group">
          <label class="form-label">Current Password</label>
          <input type="password" class="form-control" id="pw-current" autocomplete="current-password" />
        </div>
        <div class="form-group">
          <label class="form-label">New Password</label>
          <input type="password" class="form-control" id="pw-new" autocomplete="new-password" />
        </div>
        <div class="form-group">
          <label class="form-label">Confirm New Password</label>
          <input type="password" class="form-control" id="pw-confirm" autocomplete="new-password" />
        </div>
        <button type="submit" class="btn btn-primary" id="pw-submit">Change Password</button>
      </form>
    </div>`;

  const form    = container.querySelector('#pw-form');
  const alertEl = container.querySelector('#pw-alert');

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    alertEl.innerHTML = '';

    const current  = document.getElementById('pw-current').value;
    const newPw    = document.getElementById('pw-new').value;
    const confirm  = document.getElementById('pw-confirm').value;

    if (!current || !newPw) {
      alertEl.innerHTML = '<div class="alert alert-error">All fields are required.</div>';
      return;
    }
    if (newPw !== confirm) {
      alertEl.innerHTML = '<div class="alert alert-error">New passwords do not match.</div>';
      return;
    }

    const btn = document.getElementById('pw-submit');
    btn.disabled = true;
    try {
      await api.changePassword(current, newPw);
      alertEl.innerHTML = '<div class="alert alert-success">Password changed successfully.</div>';
      form.reset();
    } catch (err) {
      alertEl.innerHTML = `<div class="alert alert-error">${escape(err.message)}</div>`;
    } finally {
      btn.disabled = false;
    }
  });
}
