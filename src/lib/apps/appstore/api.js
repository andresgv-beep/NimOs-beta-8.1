// api.js — Cliente HTTP del backend NimOS daemon · módulo AppStore.
//
// Centraliza todas las llamadas a `/api/services`, `/api/docker/*` y
// `/api/operations/*` que necesita el módulo AppStore.
//
// Patrón replicado de `apps/storage/api.js` para consistencia:
//   - `unwrap()` normaliza respuestas v2 (objeto con `data` o `error`)
//   - funciones nombradas con tipos JSDoc
//   - errores lanzados con `.code` y `.details`
//
// Endpoints que toca este cliente (todos confirmados en daemon Beta 8.1.x):
//
//   GET    /api/services                            · capacidades + apps instaladas
//   POST   /api/docker/install [?async=true]        · instalar Docker engine
//   POST   /api/docker/uninstall                    · quitar Docker engine
//   POST   /api/docker/stack                        · deploy de app del catálogo
//   POST   /api/docker/container                    · container individual
//   DELETE /api/docker/stack/:id                    · uninstall stack
//   DELETE /api/docker/container/:id                · uninstall container
//   POST   /api/docker/container/:id/:action        · start | stop | restart
//   GET    /api/docker/pull/:image [?async=true]    · pull explícito
//   GET    /api/operations/:opId                    · polling de async ops
//
// LIMITACIONES CONOCIDAS (resueltas o documentadas en backend Fase 2):
//
//   - `dockerStackDeploy` (POST /api/docker/stack) NO tiene `?async=true`.
//     Es síncrono. Para apps grandes, usar `pullImage()` async PRIMERO
//     (con progreso real del download) y luego `installApp()` que será
//     rápido porque la imagen ya está local.
//   - APP-001-B (rebuild de container individual) devuelve 501. Solo stacks
//     se pueden rebuild via `docker compose --force-recreate`.

/** @typedef {import('./types').InstalledApp} InstalledApp */
/** @typedef {import('./types').DockerEngineStatus} DockerEngineStatus */
/** @typedef {import('./types').AppStoreCapabilities} AppStoreCapabilities */
/** @typedef {import('./types').Operation} Operation */
/** @typedef {import('./types').InstallEngineRequest} InstallEngineRequest */
/** @typedef {import('./types').InstallStackRequest} InstallStackRequest */
/** @typedef {import('./types').InstallContainerRequest} InstallContainerRequest */

import { hdrs, jsonHdrs } from '$lib/stores/auth.js';

// ────────────────────────────────────────────────────────────────────────
// unwrap — replica del helper de storage/api.js
//
// Soporta dos formatos del backend NimOS:
//   · v2:     { data: payload }  ó  { error: { code, message, details } }
//   · legacy: payload directo (objeto u array)
//
// Lanza Error con .code y .details cuando el backend devuelve error.
// ────────────────────────────────────────────────────────────────────────
async function unwrap(res, label = 'api call') {
  let body;
  try {
    body = await res.json();
  } catch {
    throw new Error(`${label}: invalid JSON response (status ${res.status})`);
  }
  if (!res.ok) {
    let code = `http_${res.status}`;
    let msg = res.statusText || 'request failed';
    let details;
    if (body?.error) {
      if (typeof body.error === 'string') {
        msg = body.error;
        code = body.error;
      } else if (typeof body.error === 'object') {
        code = body.error.code || code;
        msg = body.error.message || msg;
        details = body.error.details;
      }
    }
    const e = new Error(`${label}: ${msg}`);
    e.code = code;
    e.status = res.status;
    e.details = details;
    throw e;
  }
  if (body && typeof body === 'object' && 'data' in body && !Array.isArray(body)) {
    return body.data;
  }
  return body;
}

// ────────────────────────────────────────────────────────────────────────
// CAPABILITIES · derivadas de /api/services + /api/storage/v2/pools
//
// El frontend consulta dos endpoints diferentes para componer las
// capabilities del AppStore:
//
//   1. /api/storage/v2/pools  → ¿hay al menos un pool BTRFS?
//   2. /api/services          → ¿está Docker engine instalado y corriendo?
//
// Los pools NO están en /api/services (esa lista son service instances de
// NimHealth: Docker engine, NimShield, etc). El servicio canonical para
// pools es Storage v2.
//
// Esta función es el ÚNICO sitio donde el frontend interpreta el shape
// de ambas APIs. Si cambian, este es el punto de fix.
// ────────────────────────────────────────────────────────────────────────

/**
 * Lista completa de service instances desde NimHealth (Docker, NimShield...).
 *
 * El backend responde con `{ services: [...] }` envolviendo el array dentro
 * de un objeto. Esta función deshace ese wrap y devuelve solo el array.
 *
 * @returns {Promise<Array<Object>>}
 */
export async function getServices() {
  const res = await fetch('/api/services', { headers: hdrs() });
  const body = await unwrap(res, 'services');
  // El backend devuelve { services: [...] } · deshacer el wrap.
  if (body && typeof body === 'object' && Array.isArray(body.services)) {
    return body.services;
  }
  // Fallback defensivo · por si en algún futuro el backend devuelve el array directo.
  if (Array.isArray(body)) return body;
  return [];
}

/**
 * Status específico del Docker engine.
 * Devuelve null si Docker no está registrado en NimHealth todavía.
 *
 * @returns {Promise<DockerEngineStatus | null>}
 */
export async function getDockerEngine() {
  const services = await getServices();
  return services.find((s) => s.type === 'docker-engine' || s.id === 'containers' || s.appId === 'containers') || null;
}

/**
 * Comprueba si hay al menos un pool BTRFS gestionable.
 *
 * Consulta /api/storage/v2/pools y considera "hasPool" cuando existe al menos
 * un pool en la respuesta. No filtra por estado · cualquier pool registrado
 * cuenta. El install de Docker valida después que el pool esté montado.
 *
 * @returns {Promise<boolean>}
 */
export async function hasAnyPool() {
  try {
    const res = await fetch('/api/storage/v2/pools', {
      headers: hdrs(),
    });
    if (!res.ok) return false;
    const body = await res.json();
    // Storage v2 envuelve en { data: [...] } según patrón observado en storage/api.js
    const data = body?.data ?? body;
    if (Array.isArray(data)) return data.length > 0;
    // Alternativa · { pools: [...] }
    if (data?.pools && Array.isArray(data.pools)) return data.pools.length > 0;
    return false;
  } catch (err) {
    console.warn('[appstore/api] hasAnyPool failed:', err);
    return false;
  }
}

/**
 * Deriva las capabilities del sistema relevantes para AppStore.
 *
 * Esta función decide qué pantalla mostrar al user:
 *   - !hasPool         → empty state "sin pool"
 *   - !dockerInstalled → empty state "sin docker"
 *   - else             → catálogo
 *
 * @returns {Promise<AppStoreCapabilities>}
 */
export async function getCapabilities() {
  /** @type {AppStoreCapabilities} */
  const caps = {
    hasPool: false,
    dockerInstalled: false,
    dockerRunning: false,
    // TODO Fase futura · derivar de session permissions
    hasPermission: true,
  };

  // Lanzamos las dos consultas en paralelo · son independientes.
  const [pool, services] = await Promise.all([
    hasAnyPool(),
    getServices().catch(() => []),
  ]);

  caps.hasPool = pool;

  // Docker engine: presencia de la instance "containers" + status running.
  // En el cache de Docker engine en /api/services, el id es 'containers' y
  // el appId también. El status se enriquece desde la cache del observer.
  const docker = services.find(
    (s) => s?.type === 'docker-engine' || s?.id === 'containers' || s?.appId === 'containers'
  );
  if (docker) {
    caps.dockerInstalled = true;
    caps.dockerRunning = docker.status === 'running';
  }

  return caps;
}

/**
 * Lista de apps Docker instaladas (filtradas de /api/services).
 *
 * @returns {Promise<InstalledApp[]>}
 */
/**
 * Devuelve las apps Docker registradas en NimHealth, usadas por
 * composeAppViews para detectar qué apps del catálogo están instaladas.
 *
 * Estructura real del backend /api/services:
 *
 *   {
 *     services: [
 *       { appId: "nfs",        id: "nfs@system",     ... },         ← sistema
 *       { appId: "samba",      id: "samba@system",   ... },         ← sistema
 *       { appId: "containers", id: "docker@data3",   children: [    ← contenedor padre
 *         { id: "jellyfin",    type: "docker-app", ... },           ← app Docker
 *         { id: "immich",      type: "docker-app", ... },           ← app Docker
 *         { id: "codeserver",  type: "docker-app", ... },           ← app Docker
 *       ]},
 *     ]
 *   }
 *
 * Las apps Docker viven anidadas como `children` del service tipo "containers"
 * (modelo Beta 8.1: Docker es un service del NAS que tiene apps como children).
 * Hay que recorrerlas para poder cruzarlas con el catálogo del AppStore.
 *
 * Devolvemos solo los children de servicios containers, con ids planos como
 * "jellyfin", "immich" · que es la dimensión que usa composeAppViews para
 * hacer match contra los ids del catálogo.
 */
export async function getInstalledApps() {
  const services = await getServices();
  const out = [];
  for (const svc of services || []) {
    if (svc?.appId !== 'containers') continue;
    for (const child of svc.children || []) {
      out.push(child);
    }
  }
  return out;
}

// ────────────────────────────────────────────────────────────────────────
// DOCKER ENGINE · install / uninstall
// ────────────────────────────────────────────────────────────────────────

/**
 * Instala Docker engine en el pool indicado.
 *
 * Si `async: true`, devuelve { operationId, pollUrl } y el cliente debe
 * llamar `waitForOperation(operationId)` para ver el progreso.
 *
 * Si `async: false` (default · legacy), bloquea ~30s-5min y devuelve el
 * resultado final.
 *
 * @param {InstallEngineRequest} request
 * @param {Object} [opts]
 * @param {boolean} [opts.async]
 * @returns {Promise<Object>}  Sync: { ok, path, dockerAvailable }
 *                              Async: { operationId, pollUrl, status, type }
 */
/**
 * Instala Docker engine en el pool indicado.
 *
 * SÍNCRONO POR DISEÑO · esta operación se ejecuta UNA SOLA VEZ por NAS
 * (cuando se prepara el sistema para apps Docker). Mantener infraestructura
 * async para algo que sucede una vez en la vida del sistema sería
 * over-engineering. El backend procesa la instalación en ~3-7 minutos
 * (apt install, modprobe, daemon.json, share creation, registro NimHealth)
 * y devuelve cuando completa.
 *
 * Si la conexión HTTP cae durante el proceso (proxy timeout, navegador
 * cerrado), el backend SIGUE trabajando. Al recargar AppStore, capabilities
 * reportará dockerInstalled:true y el flujo continúa normal.
 *
 * Para operaciones que SÍ se repiten (docker pull de imagen al instalar app
 * del catálogo), ver pullImage() que sí usa el patrón async.
 *
 * @param {InstallEngineRequest} request
 * @returns {Promise<Object>}  { ok, path, dockerAvailable }
 */
export async function installDockerEngine(request) {
  const res = await fetch('/api/docker/install', {
    method: 'POST',
    headers: jsonHdrs(),
    body: JSON.stringify(request),
  });
  return unwrap(res, 'install docker engine');
}

/**
 * Desinstala Docker engine. Síncrono.
 *
 * @returns {Promise<Object>}
 */
export async function uninstallDockerEngine() {
  const res = await fetch('/api/docker/uninstall', {
    method: 'POST',
    headers: hdrs(),
  });
  return unwrap(res, 'uninstall docker engine');
}

// ────────────────────────────────────────────────────────────────────────
// APPS · install / uninstall / action
// ────────────────────────────────────────────────────────────────────────

/**
 * Instala una app del catálogo (vía docker-compose · stack deploy).
 *
 * BACKEND SÍNCRONO · este endpoint NO soporta async. Para apps grandes
 * usar `pullImage()` async antes de llamar a esta función para que el
 * download tenga progreso real.
 *
 * Body que espera el backend (POST /api/docker/stack):
 *   {
 *     id:       string,             // app id sanitizado
 *     name:     string,             // display name
 *     compose:  string,              // YAML completo del docker-compose
 *     icon?:    string,             // URL del icono
 *     color?:   string,             // hex color
 *     port?:    number,             // puerto principal (legacy compat)
 *     external?: boolean,            // true → openMode="external" en BD
 *     env?:     Object<string,any>  // claves para .env (sobrescriben las
 *                                     auto-inyectadas CONFIG_PATH y HOST_IP)
 *   }
 *
 * NOTA: el body usa `external: bool`, NO `openMode: string`. El backend
 * convierte internamente a openMode. Aquí traducimos para que el caller
 * pueda usar tanto openMode como external.
 *
 * @param {InstallStackRequest} request
 * @returns {Promise<Object>}  { ok, stack, path }
 */
export async function installApp(request) {
  if (!request?.id || !request?.compose) {
    throw new Error('installApp: id and compose are required');
  }
  // Traducir openMode → external para que el backend lo interprete bien.
  // Si el caller pasa external directo, también vale.
  const body = {
    id: request.id,
    name: request.name,
    compose: request.compose,
    icon: request.icon,
    color: request.color,
    port: request.port,
    env: request.env,
  };
  if (typeof request.external === 'boolean') {
    body.external = request.external;
  } else if (request.openMode === 'external') {
    body.external = true;
  }
  const res = await fetch('/api/docker/stack', {
    method: 'POST',
    headers: jsonHdrs(),
    body: JSON.stringify(body),
  });
  return unwrap(res, 'install app');
}

/**
 * Instala un container individual (sin compose).
 *
 * Para uso desde catálogos custom o apps simples. El catálogo oficial
 * actual entrega `compose` siempre · usa `installApp()` para esos.
 *
 * @param {InstallContainerRequest} request
 * @returns {Promise<Object>}
 */
export async function installContainer(request) {
  if (!request?.id || !request?.image) {
    throw new Error('installContainer: id and image are required');
  }
  const res = await fetch('/api/docker/container', {
    method: 'POST',
    headers: jsonHdrs(),
    body: JSON.stringify(request),
  });
  return unwrap(res, 'install container');
}

/**
 * Desinstala una app · stack o container.
 *
 * Backend race-free desde APP-031: marca deleting=1 sync, cleanup async.
 * El observer ya no la muestra como activa en cuanto este endpoint retorna OK.
 *
 * @param {string} id     App ID
 * @param {string} id · app id (jellyfin, immich...)
 * @param {"stack"|"container"} type
 * @param {Object} [opts]
 * @param {boolean} [opts.wipe=false] · true = borrado completo (compose down -v +
 *   borra stack files y CONFIG_PATH); false = desinstalación suave (datos preservados,
 *   reinstalar más tarde recupera todo donde estaba). Default seguro: false.
 * @returns {Promise<Object>}
 */
export async function uninstallApp(id, type, opts = {}) {
  if (!id) throw new Error('uninstallApp: id required');
  if (type !== 'stack' && type !== 'container') {
    throw new Error(`uninstallApp: invalid type "${type}"`);
  }
  const wipe = opts.wipe === true;
  const base = type === 'stack'
    ? `/api/docker/stack/${encodeURIComponent(id)}`
    : `/api/docker/container/${encodeURIComponent(id)}`;
  const path = wipe ? `${base}?wipe=true` : base;
  const res = await fetch(path, {
    method: 'DELETE',
    headers: hdrs(),
  });
  return unwrap(res, `uninstall ${type}`);
}

/**
 * Start / stop / restart de una app instalada.
 *
 * @param {string} id
 * @param {"start"|"stop"|"restart"} action
 * @returns {Promise<Object>}
 */
export async function appAction(id, action) {
  if (!id) throw new Error('appAction: id required');
  if (!['start', 'stop', 'restart'].includes(action)) {
    throw new Error(`appAction: invalid action "${action}"`);
  }
  const res = await fetch(
    `/api/docker/container/${encodeURIComponent(id)}/${action}`,
    {
      method: 'POST',
      headers: hdrs(),
    }
  );
  return unwrap(res, `app ${action}`);
}

// ────────────────────────────────────────────────────────────────────────
// PULL · download de imagen Docker (sin install)
// ────────────────────────────────────────────────────────────────────────

/**
 * Hace docker pull de una imagen.
 *
 * Soporta async desde Fase 2 Batch 3 del backend (APP-053). Con `async: true`
 * devuelve operationId para polling. Sin async, bloquea 10s-2min.
 *
 * Estrategia recomendada para install de apps grandes:
 *   1. pullImage(app.image, { async: true }) → operationId
 *   2. waitForOperation(operationId, onProgress) → mostrar progreso al user
 *   3. installApp({ id, compose }) → rápido porque la imagen ya está
 *
 * @param {string} image                      "jellyfin/jellyfin:latest"
 * @param {Object} [opts]
 * @param {boolean} [opts.async]
 * @returns {Promise<Object>}
 */
export async function pullImage(image, { async: asyncMode = false } = {}) {
  if (!image) throw new Error('pullImage: image required');
  const path = `/api/docker/pull/${encodeURIComponent(image)}`;
  const url = asyncMode ? `${path}?async=true` : path;
  const res = await fetch(url, {
    method: 'GET',
    headers: hdrs(),
  });
  return unwrap(res, 'pull image');
}

// ────────────────────────────────────────────────────────────────────────
// OPERATIONS · polling de async ops
// ────────────────────────────────────────────────────────────────────────

/**
 * Lee el estado actual de una operation async.
 *
 * Estados terminales: "succeeded", "failed", "cancelled".
 * No-terminales: "pending", "running".
 *
 * @param {string} opId
 * @returns {Promise<Operation>}
 */
export async function getOperation(opId) {
  if (!opId) throw new Error('getOperation: opId required');
  const res = await fetch(`/api/operations/${encodeURIComponent(opId)}`, {
    headers: hdrs(),
  });
  return unwrap(res, 'get operation');
}

/**
 * Polling de una operation hasta estado terminal.
 *
 * Llama `onProgress(op)` cada vez que recibe un update (incluyendo el inicial
 * y el terminal). Resolves con la operation final cuando alcanza terminal.
 *
 * Cancelable via `opts.signal` (AbortController). En caso de abort, lanza
 * AbortError; el trabajo del backend SIGUE corriendo (el frontend solo deja
 * de pollear), porque async ops del backend no son cancelables (todavía).
 *
 * @param {string} opId
 * @param {(op: Operation) => void} [onProgress]
 * @param {Object} [opts]
 * @param {number} [opts.intervalMs]              Default 1000
 * @param {AbortSignal} [opts.signal]
 * @returns {Promise<Operation>}
 */
export async function waitForOperation(
  opId,
  onProgress,
  { intervalMs = 1000, signal } = {}
) {
  if (!opId) throw new Error('waitForOperation: opId required');
  const TERMINAL = new Set(['succeeded', 'failed', 'cancelled']);

  while (true) {
    if (signal?.aborted) {
      const e = new Error('waitForOperation aborted');
      e.name = 'AbortError';
      throw e;
    }
    const op = await getOperation(opId);
    if (typeof onProgress === 'function') {
      try {
        onProgress(op);
      } catch (cbErr) {
        // No abortamos el polling por errores del callback de UI
        console.error('[appstore/api] onProgress threw:', cbErr);
      }
    }
    if (TERMINAL.has(op.status)) {
      return op;
    }
    // Esperar el intervalo, respetando el signal
    await new Promise((resolve, reject) => {
      const t = setTimeout(resolve, intervalMs);
      if (signal) {
        signal.addEventListener(
          'abort',
          () => {
            clearTimeout(t);
            const e = new Error('waitForOperation aborted');
            e.name = 'AbortError';
            reject(e);
          },
          { once: true }
        );
      }
    });
  }
}
