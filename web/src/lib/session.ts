// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Tencent. All rights reserved.

// Lightweight WebUI session storage. The token is sent as `X-Session-Token`
// (see lib/api.ts) and validated by CubeAPI's /auth/session endpoint.
//
// SECURITY (M-05): Session tokens (and API keys in lib/api.ts) are persisted in
// localStorage / sessionStorage. Any XSS payload running in the page origin can
// read these stores and steal credentials, because:
//   - localStorage is shared across every page on the origin (no path scoping),
//   - it survives tab/browser restarts, and
//   - JavaScript has unrestricted read access (no HttpOnly equivalent).
//
// Migration path to remove the exposure:
//   - Backend issues session credentials as HttpOnly + Secure + SameSite=Strict
//     (or Lax) cookies. The browser then attaches them to requests automatically
//     and JS cannot read them via document.cookie or storage APIs.
//   - Remove all localStorage/sessionStorage writes of tokens here and in
//     lib/api.ts; the `X-Session-Token` / `X-API-Key` headers become optional
//     fallbacks only.
//   - Pair with a strict Content-Security-Policy (no unsafe-inline) to shrink
//     the XSS surface that motivates the migration.
//
// TODO(M-05): Implement the HttpOnly-cookie flow. The cookie work needs backend
// changes to /auth/login and /auth/session (Set-Cookie + CSRF handling) and is
// tracked separately. Until then, keep the current localStorage approach so the
// UI keeps working — this comment documents the risk, it does NOT change behavior.

const TOKEN_KEY = 'cube.session';
const USER_KEY = 'cube.sessionUser';
const AUTH_STATUS_KEY = 'cube.authStatus';

export type AuthStatus = 'allowed' | 'guest';

export function getSessionToken(): string {
  return localStorage.getItem(TOKEN_KEY) ?? '';
}

export function getSessionUser(): string {
  return localStorage.getItem(USER_KEY) ?? '';
}

export function setSession(token: string, username: string): void {
  localStorage.setItem(TOKEN_KEY, token);
  localStorage.setItem(USER_KEY, username);
  setLastAuthStatus('allowed');
}

export function clearSession(): void {
  localStorage.removeItem(TOKEN_KEY);
  localStorage.removeItem(USER_KEY);
  setLastAuthStatus('guest');
}

export function getLastAuthStatus(): AuthStatus | null {
  const value = sessionStorage.getItem(AUTH_STATUS_KEY);
  return value === 'allowed' || value === 'guest' ? value : null;
}

export function setLastAuthStatus(status: AuthStatus): void {
  sessionStorage.setItem(AUTH_STATUS_KEY, status);
}
