// admin-users.js — User management (admin only)

import * as api from '../api.js';
import { escape, roleBadge, fmtDateShort, showModal } from '../utils.js';

export function cleanup() {}

export async function render(container) {
  container.innerHTML = `
    <div class="page-header">
      <div>
        <div class="page-title">User Management</div>
        <div class="page-subtitle">Manage system users</div>
      </div>
      <button class="btn btn-primary" id="users-add-btn">+ New User</button>
    </div>
    <div id="users-alert"></div>
    <div id="users-form-wrap"></div>
    <div id="users-content"><div class="loading-wrap"><span class="spinner"></span> Loading…</div></div>`;

  container.querySelector('#users-add-btn').onclick = () => showForm(null);

  async function load() {
    try {
      const users = await api.listUsers();
      renderTable(users);
    } catch (err) {
      document.getElementById('users-content').innerHTML =
        `<div class="alert alert-error">Failed to load: ${escape(err.message)}</div>`;
    }
  }

  function showForm(user) {
    const wrap = document.getElementById('users-form-wrap');
    const editing = !!user;
    wrap.innerHTML = `
      <div class="card" style="margin-bottom:1rem">
        <div class="card-title">${editing ? 'Edit User' : 'Create User'}</div>
        <form id="user-form">
          <div style="display:grid;grid-template-columns:1fr 1fr auto;gap:.75rem;align-items:end;flex-wrap:wrap">
            <div class="form-group" style="margin:0">
              <label class="form-label">Username</label>
              <input class="form-control" id="uf-username" type="text"
                value="${escape(user?.username || '')}" ${editing ? '' : 'required'} />
            </div>
            <div class="form-group" style="margin:0">
              <label class="form-label">Password ${editing ? '(leave blank to keep)' : ''}</label>
              <input class="form-control" id="uf-password" type="password"
                ${editing ? '' : 'required'} placeholder="${editing ? '••••••••' : ''}" />
            </div>
            <div class="form-group" style="margin:0">
              <label class="form-label">Role</label>
              <select class="form-control" id="uf-role">
                <option value="user"  ${(user?.role || 'user') === 'user'  ? 'selected' : ''}>user</option>
                <option value="admin" ${user?.role === 'admin' ? 'selected' : ''}>admin</option>
              </select>
            </div>
          </div>
          <div id="uf-error" style="margin-top:.5rem"></div>
          <div style="display:flex;gap:.5rem;margin-top:.75rem">
            <button class="btn btn-primary" type="submit">${editing ? 'Save' : 'Create'}</button>
            <button class="btn btn-ghost" type="button" id="uf-cancel">Cancel</button>
          </div>
        </form>
      </div>`;

    document.getElementById('uf-cancel').onclick = () => { wrap.innerHTML = ''; };

    document.getElementById('user-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      const username = document.getElementById('uf-username').value.trim();
      const password = document.getElementById('uf-password').value;
      const role     = document.getElementById('uf-role').value;
      const errEl    = document.getElementById('uf-error');
      errEl.innerHTML = '';

      try {
        if (editing) {
          const fields = { role };
          if (username) fields.username = username;
          if (password) fields.password = password;
          await api.updateUser(user.id, fields);
        } else {
          await api.createUser(username, password, role);
        }
        wrap.innerHTML = '';
        await load();
        document.getElementById('users-alert').innerHTML =
          `<div class="alert alert-success">${editing ? 'User updated.' : 'User created.'}</div>`;
        setTimeout(() => { const a = document.getElementById('users-alert'); if (a) a.innerHTML = ''; }, 3000);
      } catch (err) {
        errEl.innerHTML = `<div class="form-error">${escape(err.message)}</div>`;
      }
    });
  }

  function renderTable(users) {
    const el = document.getElementById('users-content');
    if (!users.length) {
      el.innerHTML = `<div class="empty-state"><div class="empty-state-title">No users</div></div>`;
      return;
    }
    el.innerHTML = `
      <div class="card" style="padding:0;overflow:hidden">
        <div class="table-wrap">
          <table>
            <thead>
              <tr><th>ID</th><th>Username</th><th>Role</th><th>Created</th><th>Actions</th></tr>
            </thead>
            <tbody id="users-tbody"></tbody>
          </table>
        </div>
      </div>`;

    const tbody = document.getElementById('users-tbody');
    users.forEach(u => {
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td>${escape(String(u.id))}</td>
        <td>${escape(u.username)}</td>
        <td>${roleBadge(u.role)}</td>
        <td>${fmtDateShort(u.created_at)}</td>
        <td>
          <div style="display:flex;gap:.35rem">
            <a href="#/admin/users/${escape(String(u.id))}/subscriptions" class="btn btn-ghost btn-sm">Subs</a>
            <button class="btn btn-ghost btn-sm" data-action="edit">Edit</button>
            <button class="btn btn-danger btn-sm" data-action="delete">Delete</button>
          </div>
        </td>`;
      tr.querySelector('[data-action=edit]').onclick = () => showForm(u);
      tr.querySelector('[data-action=delete]').onclick = () => {
        showModal({
          title: 'Delete user?',
          body: `Delete user "${u.username}"? This cannot be undone.`,
          confirmLabel: 'Delete',
          confirmClass: 'btn-danger',
          onConfirm: async () => {
            try { await api.deleteUser(u.id); load(); }
            catch (err) { alert(`Delete failed: ${err.message}`); }
          },
        });
      };
      tbody.appendChild(tr);
    });
  }

  await load();
}
