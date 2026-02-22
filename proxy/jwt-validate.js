// jwt-validate.js — NGINX NJS JWT validation (HS256)
// Reads the JWT from the access_token cookie.
// Sets r.variables.jwt_user_id and r.variables.jwt_user_role on success
// (declared as js_var in proxy.conf; forwarded to backend as X-User-Id / X-User-Role).
// Returns 403 on missing/invalid token, 401 on expired (triggers frontend refresh).

// Paths that bypass JWT validation entirely.
const PUBLIC_PATHS = [
  '/api/auth/login',
  '/api/auth/refresh',
  '/api/health',
];

function isPublic(path) {
  return PUBLIC_PATHS.some(p => path === p || path.startsWith(p));
}

// Parse the access_token value out of a Cookie header string.
function getTokenFromCookie(cookieHeader) {
  if (!cookieHeader) return null;
  const parts = cookieHeader.split(';');
  for (let i = 0; i < parts.length; i++) {
    const part = parts[i].trim();
    if (part.startsWith('access_token=')) {
      return part.substring('access_token='.length);
    }
  }
  return null;
}

// Base64url decode to Uint8Array
function b64urlDecode(str) {
  const b64 = str.replace(/-/g, '+').replace(/_/g, '/');
  const pad = b64.length % 4 === 0 ? '' : '='.repeat(4 - (b64.length % 4));
  return Buffer.from(b64 + pad, 'base64');
}

// HMAC-SHA256 using NGINX NJS built-in crypto
function hmacSha256(secret, data) {
  return require('crypto')
    .createHmac('sha256', secret)
    .update(data)
    .digest();
}

// Constant-time buffer comparison
function bufEqual(a, b) {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a[i] ^ b[i];
  return diff === 0;
}

function validateJwt(r) {
  // Allow public paths through unconditionally
  if (isPublic(r.uri)) {
    r.internalRedirect('@backend');
    return;
  }

  const secret = process.env.JWT_SECRET;
  if (!secret) {
    r.error('JWT_SECRET env var not set');
    r.return(500, 'Internal Server Error');
    return;
  }

  // Read JWT from cookie (HttpOnly access_token cookie)
  const token = getTokenFromCookie(r.headersIn['Cookie']);
  if (!token) {
    r.headersOut['X-Redirect-To'] = '/login';
    r.return(403, 'Missing token');
    return;
  }

  const parts = token.split('.');
  if (parts.length !== 3) {
    r.headersOut['X-Redirect-To'] = '/login';
    r.return(403, 'Malformed token');
    return;
  }

  const headerB64    = parts[0];
  const payloadB64   = parts[1];
  const sigB64       = parts[2];
  const signingInput = headerB64 + '.' + payloadB64;

  // Verify signature
  const expectedSig = hmacSha256(secret, signingInput);
  const actualSig   = b64urlDecode(sigB64);
  if (!bufEqual(expectedSig, actualSig)) {
    r.headersOut['X-Redirect-To'] = '/login';
    r.return(403, 'Invalid signature');
    return;
  }

  // Decode payload
  let payload;
  try {
    payload = JSON.parse(b64urlDecode(payloadB64).toString('utf8'));
  } catch (e) {
    r.return(403, 'Bad payload');
    return;
  }

  // Check expiry — return 401 so the frontend refresh logic can retry.
  const now = Math.floor(Date.now() / 1000);
  if (payload.exp && payload.exp < now) {
    r.return(401, 'Token expired');
    return;
  }

  // Set NJS variables (declared as js_var in proxy.conf); nginx forwards them as headers.
  r.variables.jwt_user_id   = String(payload.sub  || '');
  r.variables.jwt_user_role = String(payload.role || 'user');

  // Route /thumbnails/ to the fileserver (auth-protected, no /media/subscriptions prefix).
  if (r.uri.startsWith('/thumbnails/')) {
    const stripped = r.uri.substring('/thumbnails'.length); // => /driver/...
    r.internalRedirect('/thumbserver-internal' + stripped);
    return;
  }

  // Route /media/subscriptions/ to the fileserver (strip /media/subscriptions prefix).
  if (r.uri.startsWith('/media/subscriptions/')) {
    const stripped = r.uri.substring('/media/subscriptions'.length); // => /driver/...
    r.internalRedirect('/fileserver-internal' + stripped);
    return;
  }

  r.internalRedirect('@backend');
}

export default { validateJwt };
