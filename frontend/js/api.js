// api.js — Fetch wrapper with auth, refresh, and error handling

const API_BASE = '/api';

function getToken() {
  return sessionStorage.getItem('access_token');
}

function setToken(token) {
  sessionStorage.setItem('access_token', token);
}

function getUser() {
  try {
    return JSON.parse(sessionStorage.getItem('user') || 'null');
  } catch {
    return null;
  }
}

function clearSession() {
  sessionStorage.removeItem('access_token');
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
    const data = await res.json();
    setToken(data.access_token);
    return data.access_token;
  })();
  try {
    return await _refreshing;
  } finally {
    _refreshing = null;
  }
}

async function request(method, path, body, retry = true) {
  const token = getToken();
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers['Authorization'] = `Bearer ${token}`;

  const opts = { method, headers, credentials: 'include' };
  if (body !== undefined) opts.body = JSON.stringify(body);

  let res = await fetch(`${API_BASE}${path}`, opts);

  if (res.status === 401 && retry) {
    try {
      const newToken = await refreshToken();
      headers['Authorization'] = `Bearer ${newToken}`;
      res = await fetch(`${API_BASE}${path}`, { ...opts, headers });
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
  setToken(data.access_token);
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

// ---- config ----

export function getConfig() { return request('GET', '/config'); }
export function putConfig(data) { return request('PUT', '/config', data); }

// ---- users ----

export function listUsers() { return request('GET', '/users'); }
export function createUser(username, password, role) { return request('POST', '/users', { username, password, role }); }
export function getUser(id) { return request('GET', `/users/${id}`); }
export function updateUser(id, fields) { return request('PUT', `/users/${id}`, fields); }
export function deleteUser(id) { return request('DELETE', `/users/${id}`); }

// ---- health ----

export function health() { return request('GET', '/health', undefined, false); }

// healthCheck returns raw health data without throwing on 503 (overseer offline).
// Returns: { ok: bool, status: int, overseer_connected: bool, status: string }
export async function healthCheck() {
  const token = getToken();
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers['Authorization'] = `Bearer ${token}`;
  try {
    const res = await fetch(`${API_BASE}/health`, { method: 'GET', headers, credentials: 'include' });
    const text = await res.text();
    let json;
    try { json = JSON.parse(text); } catch { json = {}; }
    return { httpOk: res.ok, httpStatus: res.status, ...json };
  } catch (err) {
    return { httpOk: false, httpStatus: 0, status: 'unreachable', overseer_connected: false };
  }
}

// ---- session helpers (exported for nav/guards) ----

export { getUser as getSessionUser, getToken, clearSession };
