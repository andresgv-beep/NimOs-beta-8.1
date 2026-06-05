// api.js — Cliente HTTP del módulo Network (v4).
//
// Centraliza las llamadas a /api/v4/network/* en funciones nombradas.
// Mismo patrón que storage/api.js: separar "qué pedir" de "cómo pedirlo",
// y dejar el manejo de errores en un solo sitio (unwrap).
//
// Scope actual: subsistema de exposición (apps expuestas vía Caddy + config
// global + estado de certs). Ports/Router/DDNS se añadirán cuando se migren
// sus secciones al patrón modular.
//
// Endpoints backend (Beta 8.1):
//   GET    /api/v4/network/exposure          — apps + snapshot de certs
//   POST   /api/v4/network/exposure          — exponer app
//   GET    /api/v4/network/exposure/:id      — detalle
//   PUT    /api/v4/network/exposure/:id      — editar
//   DELETE /api/v4/network/exposure/:id      — quitar
//   GET    /api/v4/network/exposure/config   — config global
//   PUT    /api/v4/network/exposure/config   — guardar config

import { hdrs, jsonHdrs } from '$lib/stores/auth.js';

const BASE = '/api/v4/network';

// unwrap — normaliza la respuesta del daemon.
//
// El daemon responde con jsonOk → objeto directo (p.ej. {apps:[...]}) o
// jsonError → status != 2xx con {error: "mensaje"}. Devuelve el body si OK;
// lanza Error con .code si falla.
async function unwrap(res, label = 'api call') {
  let body;
  try {
    body = await res.json();
  } catch {
    throw new Error(`${label}: respuesta no es JSON (status ${res.status})`);
  }
  if (!res.ok) {
    let code = `http_${res.status}`;
    let msg = res.statusText || 'request failed';
    if (body && body.error) {
      if (typeof body.error === 'string') {
        msg = body.error;
        code = body.error;
      } else if (typeof body.error === 'object') {
        code = body.error.code || code;
        msg = body.error.message || msg;
      }
    }
    const e = new Error(`${label}: ${msg}`);
    e.code = code;
    throw e;
  }
  return body;
}

// ────────────────────────────────────────────────────────────────────────
// Exposición — config global
// ────────────────────────────────────────────────────────────────────────

/** getExposureConfig — lee la config global (dominio, enabled). */
export async function getExposureConfig() {
  const res = await fetch(`${BASE}/exposure/config`, { headers: hdrs() });
  const body = await unwrap(res, 'exposure config');
  return body.config;
}

/** saveExposureConfig — actualiza config global. */
export async function saveExposureConfig({ baseDomain, caddyAdminURL, enabled, httpPort, httpsPort }) {
  const payload = {};
  if (baseDomain !== undefined) payload.base_domain = baseDomain;
  if (caddyAdminURL !== undefined) payload.caddy_admin_url = caddyAdminURL;
  if (enabled !== undefined) payload.enabled = enabled;
  if (httpPort !== undefined) payload.http_port = httpPort;
  if (httpsPort !== undefined) payload.https_port = httpsPort;
  const res = await fetch(`${BASE}/exposure/config`, {
    method: 'PUT',
    headers: jsonHdrs(),
    body: JSON.stringify(payload),
  });
  const body = await unwrap(res, 'save exposure config');
  return body.config;
}

// ────────────────────────────────────────────────────────────────────────
// Exposición — apps
// ────────────────────────────────────────────────────────────────────────

/**
 * listExposure — devuelve { apps: [...], certs: {reachable, certs:[...]} }.
 * El campo certs puede no venir si el observer no ha corrido aún.
 */
export async function listExposure() {
  const res = await fetch(`${BASE}/exposure`, { headers: hdrs() });
  const body = await unwrap(res, 'exposure list');
  return {
    apps: body.apps || [],
    certs: body.certs || null,
  };
}

/** exposeApp — registra/expone una app nueva. */
export async function exposeApp({ appId, displayName, subdomain, path, upstreamHost, upstreamPort, enabled = true }) {
  const res = await fetch(`${BASE}/exposure`, {
    method: 'POST',
    headers: jsonHdrs(),
    body: JSON.stringify({
      app_id: appId,
      display_name: displayName || appId,
      subdomain: subdomain || '',
      path: path || '',
      upstream_host: upstreamHost,
      upstream_port: upstreamPort,
      enabled,
    }),
  });
  const body = await unwrap(res, 'expose app');
  return body.app;
}

/** updateExposedApp — edita una app expuesta (config o enabled). */
export async function updateExposedApp(id, fields) {
  const payload = {};
  if (fields.displayName !== undefined) payload.display_name = fields.displayName;
  if (fields.subdomain !== undefined) payload.subdomain = fields.subdomain;
  if (fields.path !== undefined) payload.path = fields.path;
  if (fields.upstreamHost !== undefined) payload.upstream_host = fields.upstreamHost;
  if (fields.upstreamPort !== undefined) payload.upstream_port = fields.upstreamPort;
  if (fields.enabled !== undefined) payload.enabled = fields.enabled;
  const res = await fetch(`${BASE}/exposure/${encodeURIComponent(id)}`, {
    method: 'PUT',
    headers: jsonHdrs(),
    body: JSON.stringify(payload),
  });
  const body = await unwrap(res, 'update exposed app');
  return body.app;
}

/** unexposeApp — deja de exponer (borra) una app. */
export async function unexposeApp(id) {
  const res = await fetch(`${BASE}/exposure/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    headers: hdrs(),
  });
  return unwrap(res, 'unexpose app');
}

// ────────────────────────────────────────────────────────────────────────
// Helpers de presentación (derivados, sin red)
// ────────────────────────────────────────────────────────────────────────

/**
 * fullDomainFor — construye el dominio completo de una app dado el dominio
 * base. Devuelve "" si no hay forma de enrutar.
 */
export function fullDomainFor(app, baseDomain) {
  if (!baseDomain) return '';
  if (app.subdomain) return `${app.subdomain}.${baseDomain}`;
  if (app.path) return `${baseDomain}${app.path}`;
  return baseDomain;
}

/**
 * certForApp — empareja una app con su cert observado por subject.
 * Devuelve el objeto cert o null si no hay match.
 */
export function certForApp(app, baseDomain, certSnapshot) {
  if (!certSnapshot || !certSnapshot.certs) return null;
  const full = fullDomainFor(app, baseDomain);
  if (!full) return null;
  // El subdomain matchea por subject exacto; el path comparte el dominio base.
  const target = app.subdomain ? full : baseDomain;
  return certSnapshot.certs.find((c) => c.subject === target) || null;
}

/**
 * appState — deriva el estado visual de una app a partir de su convergence,
 * enabled, y cert. Devuelve { kind, label } donde kind ∈
 * 'exposed' | 'paused' | 'applying' | 'cert_pending' | 'cert_warn'.
 */
export function appState(app, cert, caddyReachable) {
  if (!app.enabled) {
    return { kind: 'paused', label: 'pausada' };
  }
  const conv = app.convergence || {};
  if (conv.applied < conv.desired) {
    return { kind: 'applying', label: 'aplicando…' };
  }
  if (caddyReachable === false) {
    return { kind: 'cert_pending', label: 'Caddy no responde' };
  }
  if (!cert) {
    return { kind: 'cert_pending', label: 'emitiendo certificado…' };
  }
  if (typeof cert.days_left === 'number' && cert.days_left < 15) {
    return { kind: 'cert_warn', label: `cert expira en ${cert.days_left}d` };
  }
  return { kind: 'exposed', label: 'expuesta' };
}
