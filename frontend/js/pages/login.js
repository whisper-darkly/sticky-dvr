// login.js — Login page

import * as api from '../api.js';
import { escape } from '../utils.js';

export function render(container) {
  container.innerHTML = `
    <div class="login-wrap">
      <div class="login-card">
        <div class="login-title">sticky<span>DVR</span></div>
        <div class="login-subtitle">Sign in to your account</div>
        <div id="login-alert"></div>
        <form id="login-form" autocomplete="on">
          <div class="form-group">
            <label class="form-label" for="username">Username</label>
            <input class="form-control" type="text" id="username" name="username"
              autocomplete="username" autofocus required />
          </div>
          <div class="form-group">
            <label class="form-label" for="password">Password</label>
            <input class="form-control" type="password" id="password" name="password"
              autocomplete="current-password" required />
          </div>
          <button class="btn btn-primary" type="submit" id="login-btn" style="width:100%">
            Sign in
          </button>
        </form>
      </div>
    </div>`;

  const form = container.querySelector('#login-form');
  const alert = container.querySelector('#login-alert');
  const btn = container.querySelector('#login-btn');

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    alert.innerHTML = '';
    btn.disabled = true;
    btn.innerHTML = '<span class="spinner"></span> Signing in…';

    const username = form.username.value.trim();
    const password = form.password.value;

    try {
      await api.login(username, password);
      window.location.hash = '/';
    } catch (err) {
      const msg = err.status === 401 ? 'Invalid username or password.' : `Error: ${escape(err.message)}`;
      alert.innerHTML = `<div class="alert alert-error">${msg}</div>`;
      btn.disabled = false;
      btn.textContent = 'Sign in';
    }
  });
}
