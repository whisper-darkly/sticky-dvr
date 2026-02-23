// main.js — SPA router, navigation, auth guard

import * as api from './api.js';
import { escape, navigate } from './utils.js';
import { initTheme, initThemePicker, themPickerHTML } from './theme.js';
import { render as renderLogin  }         from './pages/login.js';
import { render as renderDashboard,  cleanup as cleanDash  }  from './pages/dashboard.js';
import { render as renderSubscriptions, cleanup as cleanSubs } from './pages/subscriptions.js';
import { render as renderSource,     cleanup as cleanSource } from './pages/source-detail.js';
import { render as renderAdminCfg,   cleanup as cleanCfg   }  from './pages/admin-config.js';
import { render as renderAdminUsers, cleanup as cleanUsers }  from './pages/admin-users.js';
import { render as renderAdminSrc,   cleanup as cleanAdminSrc } from './pages/admin-sources.js';
import { render as renderAdminUserSubs, cleanup as cleanAdminUserSubs } from './pages/admin-user-subs.js';
import { render as renderAdminDiag, cleanup as cleanAdminDiag } from './pages/admin-diagnostics.js';
import { render as renderProfile, cleanup as cleanProfile } from './pages/profile.js';

// Apply theme on module load (covers the case where the inline script ran
// before cookies were set, e.g. very first visit).
initTheme();

let _currentCleanup = null;

function callCleanup() {
  if (_currentCleanup) { try { _currentCleanup(); } catch {} _currentCleanup = null; }
}

const ADMIN_ROUTES = ['/admin/config', '/admin/users', '/admin/sources', '/admin/diagnostics'];

async function route() {
  const hash = window.location.hash.replace(/^#/, '') || '/';
  let user = api.getSessionUser();

  // Auth guard: if no user in sessionStorage (new tab / browser restart), try to
  // restore the session from the access_token cookie via /api/me.
  if (!user && hash !== '/login') {
    try {
      const me = await api.getMe();
      sessionStorage.setItem('user', JSON.stringify(me));
      user = me;
    } catch {
      navigate('/login');
      return;
    }
  }

  // Redirect already-authed users away from login
  if (hash === '/login' && user) {
    navigate('/');
    return;
  }
  // Admin guard
  if (ADMIN_ROUTES.some(r => hash.startsWith(r)) && user?.role !== 'admin') {
    navigate('/');
    return;
  }

  callCleanup();

  const container = document.getElementById('page-content');

  // Build nav
  renderNav(user);

  // Route to page
  if (hash === '/login') {
    document.querySelector('.nav').style.display = 'none';
    renderLogin(container);
    return;
  }
  document.querySelector('.nav').style.display = '';

  if (hash === '/' || hash === '/dashboard') {
    _currentCleanup = cleanDash;
    renderDashboard(container);

  } else if (hash === '/subscriptions') {
    _currentCleanup = cleanSubs;
    renderSubscriptions(container);

  } else if (hash === '/profile') {
    _currentCleanup = cleanProfile;
    renderProfile(container);

  } else if (hash.startsWith('/source/')) {
    // /source/{driver}/{username}
    const parts = hash.split('/').slice(2);
    const driver = parts[0] || '';
    const username = parts.slice(1).join('/') || '';
    _currentCleanup = cleanSource;
    renderSource(container, { driver, username });

  } else if (hash === '/admin/config') {
    _currentCleanup = cleanCfg;
    renderAdminCfg(container);

  } else if (hash === '/admin/users') {
    _currentCleanup = cleanUsers;
    renderAdminUsers(container);

  } else if (hash === '/admin/sources') {
    _currentCleanup = cleanAdminSrc;
    renderAdminSrc(container);

  } else if (hash.startsWith('/admin/users/') && hash.endsWith('/subscriptions')) {
    // /admin/users/{id}/subscriptions
    const userId = hash.split('/')[3];
    _currentCleanup = cleanAdminUserSubs;
    renderAdminUserSubs(container, { userId });

  } else if (hash === '/admin/diagnostics') {
    _currentCleanup = cleanAdminDiag;
    renderAdminDiag(container);

  } else {
    container.innerHTML = `
      <div class="empty-state" style="padding:4rem">
        <div class="empty-state-icon">🔍</div>
        <div class="empty-state-title">Page not found</div>
        <div class="empty-state-text"><a href="#/">Go to dashboard</a></div>
      </div>`;
  }
}

function renderNav(user) {
  const nav = document.querySelector('.nav');
  if (!user) { nav.style.display = 'none'; return; }
  const isAdmin = user.role === 'admin';
  const hash = window.location.hash.replace(/^#/, '') || '/';

  nav.innerHTML = `
    <span class="nav-brand">sticky<span>DVR</span></span>
    <nav class="nav-links">
      <a href="#/" class="${hash === '/' || hash === '/dashboard' ? 'active' : ''}">Dashboard</a>
      <a href="#/subscriptions" class="${hash === '/subscriptions' ? 'active' : ''}">Subscriptions</a>
      ${isAdmin ? `
        <a href="#/admin/config"       class="${hash === '/admin/config' ? 'active' : ''}">Config</a>
        <a href="#/admin/users"        class="${hash === '/admin/users' ? 'active' : ''}">Users</a>
        <a href="#/admin/sources"      class="${hash === '/admin/sources' ? 'active' : ''}">Sources</a>
        <a href="#/admin/diagnostics"  class="${hash === '/admin/diagnostics' ? 'active' : ''}">Diagnostics</a>
      ` : ''}
    </nav>
    <div class="nav-user">
      <span>Signed in as <strong>${escape(user.username)}</strong></span>
      ${isAdmin ? '<span class="badge badge-admin">admin</span>' : ''}
      <a href="#/profile" class="btn btn-ghost btn-sm">Profile</a>
      <button class="btn btn-ghost btn-sm" id="nav-logout">Sign out</button>
      ${themPickerHTML()}
    </div>`;

  document.getElementById('nav-logout').onclick = async () => {
    callCleanup();
    await api.logout();
    navigate('/login');
  };

  initThemePicker();
}

// Kick off
window.addEventListener('hashchange', route);
window.addEventListener('load', route);
