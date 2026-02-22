// jwt-validate.js — NGINX NJS JWT validation (HS256)
// Validates Authorization: Bearer <token> header using JWT_SECRET env var.
// Sets r.variables.jwt_user_id and r.variables.jwt_user_role on success
// (declared as js_var in proxy.conf; forwarded to backend as X-User-Id / X-User-Role).
// Returns 403 on missing/invalid/expired token.

// Paths that bypass JWT validation entirely.
const PUBLIC_PATHS = [
  '/api/auth/login',
  '/api/auth/refresh',
  '/thumbnails/',
];

function isPublic(path) {
  return PUBLIC_PATHS.some(p => path === p || path.startsWith(p));
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

  const authHeader = r.headersIn['Authorization'] || '';
  if (!authHeader.startsWith('Bearer ')) {
    r.headersOut['X-Redirect-To'] = '/login';
    r.return(403, 'Missing token');
    return;
  }

  const token = authHeader.slice(7).trim();
  const parts = token.split('.');
  if (parts.length !== 3) {
    r.headersOut['X-Redirect-To'] = '/login';
    r.return(403, 'Malformed token');
    return;
  }

  const [headerB64, payloadB64, sigB64] = parts;
  const signingInput = `${headerB64}.${payloadB64}`;

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

  // Check expiry
  const now = Math.floor(Date.now() / 1000);
  if (payload.exp && payload.exp < now) {
    r.headersOut['X-Redirect-To'] = '/login';
    r.return(403, 'Token expired');
    return;
  }

  // Set NJS variables (declared as js_var in proxy.conf); nginx forwards them as headers.
  r.variables.jwt_user_id   = String(payload.sub  || '');
  r.variables.jwt_user_role = String(payload.role || 'user');

  r.internalRedirect('@backend');
}

export default { validateJwt };
