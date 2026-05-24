<script>
  /**
   * AppStoreDetail · Vista detalle de una app del catálogo
   * ────────────────────────────────────────────────────────
   * Muestra info completa de una app:
   *   · Hero: icono grande + nombre + categoría + puerto
   *   · Descripción
   *   · Credenciales por defecto (si aplica)
   *   · Información técnica (imagen Docker + servicios del compose)
   *   · Botones de acción:
   *       - No instalada: "Instalar" (placeholder · Fase 5)
   *       - Instalada:    "Abrir" + "Detener/Iniciar" + "Desinstalar"
   *                       (placeholders · Fase 5/6)
   *   · Botón "← Volver" emite evento `back` al padre
   *
   * El componente recibe appId como prop y se reconstruye internamente
   * pidiendo catalog + installed apps. Catalog tiene caché 5min así que
   * la mayoría de veces será instantáneo.
   *
   * Para los botones de acción reales, esta fase deja stubs que loggean.
   * Fase 5 implementa el install (pull async + stack sync). Fase 6 hace
   * desinstalar/start/stop con confirmación.
   */

  import { onMount, createEventDispatcher } from 'svelte';
  import { fetchCatalog } from './catalog.js';
  import { getInstalledApps } from './api.js';
  import InstallFlow from './InstallFlow.svelte';
  import {
    composeAppViews,
    formatStatus,
    formatHealth,
    statusTone,
    formatPort,
    categoryDisplayName,
    extractComposeServices,
  } from './formatters.js';

  /** @typedef {import('./types').AppView} AppView */
  /** @typedef {import('./types').Catalog} Catalog */

  /** ID de la app a mostrar */
  export let appId = '';

  const dispatch = createEventDispatcher();

  // ── Estado ────────────────────────────────────────────────────────
  /** @type {Catalog | null} */
  let catalog = null;
  /** @type {AppView | null} */
  let view = null;
  let loading = true;
  let loadError = '';
  let iconError = false;
  let showTech = false;

  /** Modo de vista interno · 'detail' | 'installing' */
  let mode = 'detail';

  // ── Lifecycle ──────────────────────────────────────────────────────
  onMount(load);

  // Si appId cambia (e.g. el user navega de una app a otra sin cerrar)
  // recargamos. Svelte 5 con $: reaccionará.
  $: if (appId) load();

  async function load() {
    loading = true;
    loadError = '';
    iconError = false;
    showTech = false;
    try {
      const [cat, installed] = await Promise.all([
        fetchCatalog(),
        getInstalledApps().catch(() => []),
      ]);
      catalog = cat;
      const entry = cat.apps[appId];
      if (!entry) {
        throw new Error(`App "${appId}" no encontrada en el catálogo.`);
      }
      const views = composeAppViews([{ id: appId, app: entry }], installed);
      view = views[0];
    } catch (err) {
      loadError = err?.message || String(err);
      view = null;
    } finally {
      loading = false;
    }
  }

  // ── Derived ────────────────────────────────────────────────────────
  $: tone = view ? statusTone(view.status, view.health) : 'muted';
  $: services = view?.catalog?.compose ? extractComposeServices(view.catalog.compose) : [];
  $: categoryLabel = view ? categoryDisplayName(view.category, catalog?.categories || {}) : '';

  // Resolver credentials con env si la app usa passwordKey
  $: credentials = (() => {
    const c = view?.catalog?.credentials;
    if (!c) return null;
    const env = view?.catalog?.env || {};
    return {
      username: c.username || null,
      password: c.password
        ? c.password
        : c.passwordKey
          ? (env[c.passwordKey] || `\${${c.passwordKey}}`)
          : null,
      passwordIsTemplate: !c.password && !!c.passwordKey,
    };
  })();

  // ── Acciones ───────────────────────────────────────────────────────
  function handleBack() {
    dispatch('back');
  }

  function handleInstall() {
    if (!view) return;
    mode = 'installing';
  }

  async function handleInstallDone() {
    // Tras instalar, recargamos para reflejar el nuevo estado y volvemos al detalle
    mode = 'detail';
    await load();
  }

  function handleInstallCancel() {
    mode = 'detail';
  }

  function handleOpen() {
    // Placeholder · Fase 6 abrirá la URL de la app instalada
    console.log('[appstore/detail] open · pendiente Fase 6:', appId);
  }

  function handleStartStop() {
    // Placeholder · Fase 6 implementará start/stop con appAction()
    console.log('[appstore/detail] start/stop · pendiente Fase 6:', appId);
  }

  function handleUninstall() {
    // Placeholder · Fase 6 abrirá ConfirmDialog con typed confirmation
    console.log('[appstore/detail] uninstall · pendiente Fase 6:', appId);
  }
</script>

{#if loading}
  <div class="detail-state">
    <div class="loading-dot"></div>
    <div class="state-text">Cargando detalle…</div>
  </div>
{:else if loadError}
  <div class="detail-state">
    <div class="err-title">No se pudo cargar el detalle</div>
    <div class="err-body">{loadError}</div>
    <button class="btn btn-secondary" on:click={handleBack}>← Volver</button>
  </div>
{:else if mode === 'installing' && view}
  <InstallFlow {view} on:done={handleInstallDone} on:cancel={handleInstallCancel} />
{:else if view}
  <div class="detail">
    <!-- Barra superior con botón Volver -->
    <div class="detail-toolbar">
      <button class="back-btn" on:click={handleBack} type="button">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
          <path d="M19 12H5" />
          <path d="M12 19l-7-7 7-7" />
        </svg>
        Volver al catálogo
      </button>

      {#if view.installed}
        <div class="status-pill" class:ok={tone === 'ok'} class:warn={tone === 'warn'} class:crit={tone === 'crit'} class:info={tone === 'info'}>
          <span class="status-dot"></span>
          {formatStatus(view.status)}
          {#if view.health && view.health !== 'unknown'}
            · {formatHealth(view.health)}
          {/if}
        </div>
      {/if}
    </div>

    <!-- Hero -->
    <div class="hero">
      <div class="hero-icon">
        {#if !iconError && view.icon}
          <img src={view.icon} alt={view.name} on:error={() => (iconError = true)} />
        {:else}
          <div class="hero-icon-fallback" style={view.color ? `background: ${view.color}` : ''}>
            {view.name.charAt(0).toUpperCase()}
          </div>
        {/if}
      </div>
      <div class="hero-text">
        <h1 class="hero-name">{view.name}</h1>
        <div class="hero-meta">
          <span>{categoryLabel}</span>
          {#if view.catalog?.port}
            <span class="dot-sep">·</span>
            <span class="port">{formatPort(view.catalog.port)}</span>
          {/if}
          {#if view.catalog?.official}
            <span class="dot-sep">·</span>
            <span class="badge-official">Oficial</span>
          {/if}
        </div>
      </div>
    </div>

    <!-- Descripción -->
    {#if view.description}
      <p class="description">{view.description}</p>
    {/if}

    <!-- Botones de acción -->
    <div class="actions">
      {#if !view.installed}
        <button class="btn btn-primary" on:click={handleInstall}>
          Instalar {view.name}
        </button>
      {:else}
        {#if view.status === 'running'}
          <button class="btn btn-primary" on:click={handleOpen}>
            Abrir {view.name}
          </button>
          <button class="btn btn-secondary" on:click={handleStartStop}>
            Detener
          </button>
        {:else}
          <button class="btn btn-secondary" on:click={handleStartStop}>
            Iniciar
          </button>
        {/if}
        <button class="btn btn-danger-soft" on:click={handleUninstall}>
          Desinstalar
        </button>
      {/if}
    </div>

    <!-- Credenciales por defecto -->
    {#if credentials && (credentials.username || credentials.password)}
      <section class="info-block">
        <h2 class="info-title">Credenciales por defecto</h2>
        <p class="info-hint">
          Cámbialas tras el primer acceso por seguridad.
        </p>
        <div class="cred-list">
          {#if credentials.username}
            <div class="cred-row">
              <span class="cred-label">Usuario</span>
              <code class="cred-value">{credentials.username}</code>
            </div>
          {/if}
          {#if credentials.password}
            <div class="cred-row">
              <span class="cred-label">Contraseña</span>
              <code class="cred-value">{credentials.password}</code>
              {#if credentials.passwordIsTemplate}
                <span class="cred-note">se generará al instalar</span>
              {/if}
            </div>
          {/if}
        </div>
      </section>
    {/if}

    <!-- Información técnica (plegable) -->
    <section class="info-block">
      <button class="info-toggle" on:click={() => (showTech = !showTech)} type="button">
        <svg
          class="chev"
          class:open={showTech}
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
        >
          <polyline points="9 6 15 12 9 18" />
        </svg>
        Información técnica
      </button>

      {#if showTech}
        <div class="tech-body">
          <div class="tech-row">
            <span class="tech-label">Imagen Docker</span>
            <code class="tech-value">{view.catalog?.image || '—'}</code>
          </div>
          {#if view.catalog?.openMode}
            <div class="tech-row">
              <span class="tech-label">Modo de apertura</span>
              <code class="tech-value">{view.catalog.openMode}</code>
            </div>
          {/if}
          {#if services.length > 0}
            <div class="tech-row">
              <span class="tech-label">
                Servicios{services.length > 1 ? ` (${services.length})` : ''}
              </span>
              <div class="services">
                {#each services as svc}
                  <code class="service-chip">{svc}</code>
                {/each}
              </div>
            </div>
          {/if}
          {#if view.installed && view.runtime?.containerName}
            <div class="tech-row">
              <span class="tech-label">Container</span>
              <code class="tech-value">{view.runtime.containerName}</code>
            </div>
          {/if}
        </div>
      {/if}
    </section>
  </div>
{/if}

<style>
  /* ═══ Estados (loading/error) ═══ */
  .detail-state {
    height: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--sp-3);
    padding: var(--sp-5);
    text-align: center;
  }
  .loading-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--signal);
    animation: pulse 1.4s ease-in-out infinite;
  }
  @keyframes pulse {
    0%, 100% { opacity: 0.3; transform: scale(0.9); }
    50%      { opacity: 1;   transform: scale(1.1); }
  }
  .state-text {
    color: var(--ink-mute);
    font-family: var(--font-mono);
    font-size: var(--fs-11);
  }
  .err-title {
    color: var(--crit);
    font-weight: 600;
    font-size: var(--fs-13);
  }
  .err-body {
    color: var(--ink-dim);
    font-family: var(--font-mono);
    font-size: var(--fs-11);
    max-width: 420px;
    word-break: break-word;
  }

  /* ═══ Layout detalle ═══ */
  .detail {
    height: 100%;
    overflow-y: auto;
    padding: var(--sp-4) var(--sp-5) var(--sp-5);
    display: flex;
    flex-direction: column;
    gap: var(--sp-4);
  }

  .detail-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-3);
  }
  .back-btn {
    background: none;
    border: none;
    color: var(--ink-dim);
    cursor: pointer;
    font-size: var(--fs-12);
    font-family: inherit;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 8px;
    border-radius: var(--radius-sm);
    transition: background 0.12s, color 0.12s;
  }
  .back-btn:hover {
    color: var(--ink);
    background: var(--line);
  }
  .back-btn svg {
    width: 14px;
    height: 14px;
  }

  /* Status pill (cuando instalada) */
  .status-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    border-radius: 999px;
    background: var(--panel-deep);
    border: 1px solid var(--line);
    font-size: var(--fs-11);
    font-family: var(--font-mono);
    color: var(--ink-dim);
  }
  .status-pill .status-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--ink-faint);
  }
  .status-pill.ok .status-dot { background: var(--signal); box-shadow: 0 0 4px var(--signal-glow); }
  .status-pill.warn .status-dot { background: var(--warn); box-shadow: 0 0 4px var(--warn-glow); }
  .status-pill.crit .status-dot { background: var(--crit); box-shadow: 0 0 4px var(--crit-glow); }
  .status-pill.info .status-dot { background: var(--info); box-shadow: 0 0 4px var(--info-glow); }

  /* ═══ Hero ═══ */
  .hero {
    display: flex;
    align-items: center;
    gap: var(--sp-4);
    padding: var(--sp-3) 0;
  }
  .hero-icon {
    width: 80px;
    height: 80px;
    border-radius: 16px;
    background: var(--canvas);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    overflow: hidden;
  }
  .hero-icon img {
    width: 52px;
    height: 52px;
    object-fit: contain;
  }
  .hero-icon-fallback {
    width: 100%;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--canvas-soft);
    color: var(--ink-dim);
    font-size: 32px;
    font-weight: 600;
    font-family: var(--font-mono);
  }
  .hero-text {
    flex: 1;
    min-width: 0;
  }
  .hero-name {
    font-size: var(--fs-22);
    font-weight: 600;
    color: var(--ink);
    margin: 0 0 6px;
    letter-spacing: -0.4px;
  }
  .hero-meta {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: var(--fs-12);
    color: var(--ink-mute);
    flex-wrap: wrap;
  }
  .dot-sep {
    color: var(--ink-trace);
  }
  .port {
    font-family: var(--font-mono);
    color: var(--info);
  }
  .badge-official {
    font-size: var(--fs-10);
    padding: 1px 6px;
    border-radius: 3px;
    background: var(--signal-soft);
    color: var(--signal);
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.5px;
  }

  /* ═══ Descripción ═══ */
  .description {
    color: var(--ink-dim);
    font-size: var(--fs-13);
    line-height: 1.6;
    max-width: 720px;
    margin: 0;
  }

  /* ═══ Acciones ═══ */
  .actions {
    display: flex;
    gap: var(--sp-2);
    flex-wrap: wrap;
  }
  .btn {
    padding: 9px 18px;
    border-radius: var(--radius-sm);
    border: 1px solid transparent;
    font-size: var(--fs-12);
    font-weight: 600;
    font-family: inherit;
    cursor: pointer;
    transition: filter 0.12s, background 0.12s, color 0.12s, border-color 0.12s;
  }
  .btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
    filter: none;
  }
  .btn-primary {
    background: var(--signal);
    color: var(--canvas);
  }
  .btn-primary:not(:disabled):hover {
    filter: brightness(1.08);
  }
  .btn-secondary {
    background: transparent;
    color: var(--ink-dim);
    border: 1px solid var(--line);
  }
  .btn-secondary:not(:disabled):hover {
    color: var(--ink);
    background: var(--line);
  }
  .btn-danger-soft {
    background: transparent;
    color: var(--crit);
    border: 1px solid var(--crit-border);
  }
  .btn-danger-soft:not(:disabled):hover {
    background: var(--crit-dim);
  }

  /* ═══ Info blocks ═══ */
  .info-block {
    border-top: 1px solid var(--line);
    padding-top: var(--sp-4);
    margin-top: var(--sp-2);
    display: flex;
    flex-direction: column;
    gap: var(--sp-2);
  }
  .info-title {
    font-size: var(--fs-13);
    font-weight: 600;
    color: var(--ink);
    margin: 0;
  }
  .info-hint {
    font-size: var(--fs-11);
    color: var(--ink-mute);
    margin: 0;
  }

  /* Credenciales */
  .cred-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
    background: var(--panel-deep);
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    padding: var(--sp-3);
  }
  .cred-row {
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    font-size: var(--fs-12);
  }
  .cred-label {
    color: var(--ink-mute);
    min-width: 100px;
    font-size: var(--fs-11);
  }
  .cred-value {
    color: var(--ink);
    background: var(--canvas);
    padding: 2px 8px;
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: var(--fs-12);
  }
  .cred-note {
    color: var(--info);
    font-size: var(--fs-10);
    font-style: italic;
  }

  /* Toggle de info técnica */
  .info-toggle {
    background: none;
    border: none;
    color: var(--ink);
    font-size: var(--fs-13);
    font-weight: 600;
    font-family: inherit;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 0;
    text-align: left;
  }
  .info-toggle:hover { color: var(--info); }
  .chev {
    width: 12px;
    height: 12px;
    transition: transform 0.15s;
    color: var(--ink-faint);
  }
  .chev.open {
    transform: rotate(90deg);
  }
  .tech-body {
    display: flex;
    flex-direction: column;
    gap: var(--sp-2);
    padding: var(--sp-2) 0 0;
  }
  .tech-row {
    display: flex;
    align-items: baseline;
    gap: var(--sp-3);
    font-size: var(--fs-12);
  }
  .tech-label {
    color: var(--ink-mute);
    min-width: 140px;
    font-size: var(--fs-11);
  }
  .tech-value {
    color: var(--ink);
    background: var(--canvas);
    padding: 2px 8px;
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: var(--fs-11);
    word-break: break-all;
  }
  .services {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .service-chip {
    color: var(--ink-dim);
    background: var(--canvas);
    padding: 2px 8px;
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: var(--fs-11);
  }
</style>
