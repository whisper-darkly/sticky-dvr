// api.js — Fetch wrapper with cookie-based auth, refresh, and error handling
// JWT access token is stored as an HttpOnly cookie by the server.
// All requests use credentials: 'include' so the browser sends it automatically.

const API_BASE = '/api';

// getToken returns truthy when the user is known to be logged in (session in sessionStorage).
// This is NOT the actual JWT value — that lives in an HttpOnly cookie.
function getToken() {
  return sessionStorage.getItem('user') ? '1' : null;
}

function _getSessionUser() {
  try {
    return JSON.parse(sessionStorage.getItem('user') || 'null');
  } catch {
    return null;
  }
}

function clearSession() {
  sessionStorage.removeItem('user');
}

let _refreshing = null;

async function refreshToken() {
  if (_refreshing) return _refreshing;
  _refreshing = (async () => {
    const res = await fetch(`${API_BASE}/auth/refresh`, { method: 'POST', credentials: 'include' });
    if (!res.ok) {
      clearSession();
      window.location.hash = '/login';
      throw new Error('Session expired');
    }
    // Server sets new access_token cookie via Set-Cookie; no token to store client-side.
  })();
  try {
    return await _refreshing;
  } finally {
    _refreshing = null;
  }
}

async function request(method, path, body, retry = true) {
  const headers = { 'Content-Type': 'application/json' };
  const opts = { method, headers, credentials: 'include' };
  if (body !== undefined) opts.body = JSON.stringify(body);

  let res = await fetch(`${API_BASE}${path}`, opts);

  if (res.status === 401 && retry) {
    try {
      await refreshToken();
      // Retry with the same opts — new cookie is set automatically by Set-Cookie.
      res = await fetch(`${API_BASE}${path}`, opts);
    } catch {
      throw new Error('Unauthorized');
    }
  }

  if (res.status === 204) return null;

  const text = await res.text();
  let json;
  try { json = JSON.parse(text); } catch { json = { error: text }; }

  if (!res.ok) {
    throw new ApiError(res.status, json.error || `HTTP ${res.status}`);
  }
  return json;
}

export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

// ---- auth ----

export async function login(username, password) {
  const res = await fetch(`${API_BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify({ username, password }),
  });
  const data = await res.json();
  if (!res.ok) throw new ApiError(res.status, data.error || 'Login failed');
  // access_token cookie is set by the server (HttpOnly); store only the user profile.
  sessionStorage.setItem('user', JSON.stringify(data.user));
  return data;
}

export async function logout() {
  try { await request('POST', '/auth/logout'); } catch {}
  clearSession();
}

// ---- me ----

export function getMe() { return request('GET', '/me'); }

// ---- subscriptions ----

export function listSubscriptions() { return request('GET', '/subscriptions'); }
export function createSubscription(driver, username) { return request('POST', '/subscriptions', { driver, username }); }
export function getSubscription(driver, username) { return request('GET', `/subscriptions/${driver}/${username}`); }
export function deleteSubscription(driver, username) { return request('DELETE', `/subscriptions/${driver}/${username}`); }
export function pauseSubscription(driver, username) { return request('POST', `/subscriptions/${driver}/${username}/pause`); }
export function resumeSubscription(driver, username) { return request('POST', `/subscriptions/${driver}/${username}/resume`); }
export function archiveSubscription(driver, username) { return request('POST', `/subscriptions/${driver}/${username}/archive`); }
export function resetError(driver, username) { return request('POST', `/subscriptions/${driver}/${username}/reset-error`); }

// ---- sources ----

export function getSourceEvents(driver, username) { return request('GET', `/sources/${driver}/${username}/events`); }
export function getSourceLogs(driver, username) { return request('GET', `/sources/${driver}/${username}/logs`); }
export function getSourceFiles(driver, username) { return request('GET', `/sources/${driver}/${username}/files`); }

export function getSourceFileStats(driver, username, path) {
  return request('GET', `/sources/${driver}/${username}/filestat?path=${encodeURIComponent(path || '')}`);
}

// ---- media files (nginx autoindex, JWT-cookie-protected) ----

export async function listMediaFiles(driver, username, subpath = '', retry = true) {
  // Always include trailing slash to avoid nginx redirect (which breaks CORS).
  const pathSuffix = subpath ? `${subpath}/` : '';
  const path = `/media/subscriptions/${driver}/${username}/${pathSuffix}`;
  let res = await fetch(path, {
    headers: { Accept: 'application/json' },
    credentials: 'include',
  });

  // Handle token expiry the same way as request() does.
  if (res.status === 401 && retry) {
    try {
      await refreshToken();
      res = await fetch(path, { headers: { Accept: 'application/json' }, credentials: 'include' });
    } catch {
      throw new ApiError(401, 'Unauthorized');
    }
  }

  if (res.status === 404) return [];
  if (!res.ok) throw new ApiError(res.status, `media: ${res.status}`);
  return res.json();
}

// ---- me: change password ----

export function changePassword(currentPw, newPw) {
  return request('POST', '/me/change-password', { current_password: currentPw, new_password: newPw });
}

// ---- admin: subscription management (by sub_id) ----

export function adminPauseSubscription(subId) { return request('POST', `/admin/subscriptions/${subId}/pause`); }
export function adminResumeSubscription(subId) { return request('POST', `/admin/subscriptions/${subId}/resume`); }
export function adminArchiveSubscription(subId) { return request('POST', `/admin/subscriptions/${subId}/archive`); }
export function adminDeleteSubscription(subId) { return request('DELETE', `/admin/subscriptions/${subId}`); }
export function adminResetError(subId) { return request('POST', `/admin/subscriptions/${subId}/reset-error`); }
export function adminGetSourceSubscribers(driver, username) { return request('GET', `/admin/sources/${driver}/${username}/subscribers`); }
export function adminRestartAllSources(includeErrored) { return request('POST', '/admin/sources/restart-all', { include_errored: !!includeErrored }); }
export function adminGetUserSubscriptions(userId) { return request('GET', `/admin/users/${userId}/subscriptions`); }

// ---- config ----

export function getConfig() { return request('GET', '/config'); }
export function putConfig(data) { return request('PUT', '/config', data); }

// ---- users ----

export function listUsers() { return request('GET', '/users'); }
export function createUser(username, password, role) { return request('POST', '/users', { username, password, role }); }
export function getUser(id) { return request('GET', `/users/${id}`); }
export function updateUser(id, fields) { return request('PUT', `/users/${id}`, fields); }
export function deleteUser(id) { return request('DELETE', `/users/${id}`); }

// ---- admin: diagnostics ----

export function getDiagnostics() { return request('GET', '/admin/diagnostics'); }

// ---- health ----

export function health() { return request('GET', '/health', undefined, false); }

export async function healthCheck() {
  try {
    const res = await fetch(`${API_BASE}/health`, { method: 'GET', credentials: 'include' });
    const text = await res.text();
    let json;
    try { json = JSON.parse(text); } catch { json = {}; }
    return { httpOk: res.ok, httpStatus: res.status, ...json };
  } catch (err) {
    return { httpOk: false, httpStatus: 0, status: 'unreachable', overseer_connected: false };
  }
}

// ---- session helpers (exported for nav/guards) ----

export { _getSessionUser as getSessionUser, getToken, clearSession };
