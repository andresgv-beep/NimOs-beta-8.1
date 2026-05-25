<script>
  /**
   * AppStoreDetail · Vista detalle de una app del catálogo · Fase 7
   * ─────────────────────────────────────────────────────────────────
   * Diseño tipo "store profesional" inspirado en Apple AppStore:
   *
   *   ┌─────────────────────────────────────────────────────────┐
   *   │ ← Volver al catálogo                                    │
   *   ├─────────────────────────────────────────────────────────┤
   *   │  ┌────┐                                                 │
   *   │  │icon│   Jellyfin                  [Instalar/Abrir]   │
   *   │  │    │   Multimedia                                   │
   *   │  └────┘   :8096 · Docker · Multi-servicio              │
   *   ├─────────────────────────────────────────────────────────┤
   *   │  [InstallFlow embedded · solo durante install]          │
   *   ├─────────────────────────────────────────────────────────┤
   *   │  Capturas                                               │
   *   │  [screenshot1] [screenshot2] [screenshot3]              │
   *   ├─────────────────────────────────────────────────────────┤
   *   │  Acerca de                                              │
   *   │  Servidor multimedia gratuito y open source...          │
   *   ├─────────────────────────────────────────────────────────┤
   *   │  Información técnica                                    │
   *   │  Imagen Docker:  jellyfin/jellyfin:latest               │
   *   │  Puerto:         :8096                                  │
   *   │  Servicios:      jellyfin (1)                           │
   *   ├─────────────────────────────────────────────────────────┤
   *   │  Credenciales por defecto (si aplica)                   │
   *   │  Usuario:  admin        [copiar]                        │
   *   │  Pass:     ••••••••     [ojo] [copiar]                  │
   *   ├─────────────────────────────────────────────────────────┤
   *   │  [Desinstalar] (solo si instalada · sutil, abajo)       │
   *   └─────────────────────────────────────────────────────────┘
   *
   * Arquitectura de responsabilidades:
   *   AppStore Detail   · descubrir, instalar, desinstalar, credenciales
   *   NimHealth Task Mgr · ciclo de vida runtime (start/stop/logs/métricas)
   *
   * Por eso NO incluye botones Detener/Iniciar. El user gestiona el runtime
   * en NimHealth. AppStore mantiene "Abrir" porque es acto de consumo del
   * app, no de gestión.
   */

  import { onMount, createEventDispatcher } from 'svelte';
  import { openWindow } from '$lib/stores/windows.js';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import { fetchCatalog } from './catalog.js';
  import { getInstalledApps, uninstallApp } from './api.js';
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

  /** Modo: 'detail' (normal) | 'installing' (con InstallFlow inline activo) */
  let mode = 'detail';

  // Credenciales · UI state
  let passwordVisible = false;
  let copyFeedback = ''; // mensaje breve tras copiar

  // Confirm dialog · uninstall
  let confirmUninstallOpen = false;
  let uninstallProcessing = false;
  let actionError = '';

  // Screenshots · intentamos cargar 1..6, oculta automáticamente las que fallan
  // El repo del catálogo guarda screenshots en /screenshots/{appId}/N.png
  /** @type {number[]} */
  let visibleShots = [1, 2, 3, 4, 5, 6];
  /** @type {Set<number>} */
  let failedShots = new Set();
  /** @type {Set<number>} · solo las imágenes que se han cargado satisfactoriamente */
  let loadedShots = new Set();

  // ── Lifecycle ──────────────────────────────────────────────────────
  onMount(load);

  $: if (appId) load();

  async function load() {
    loading = true;
    loadError = '';
    iconError = false;
    passwordVisible = false;
    copyFeedback = '';
    visibleShots = [1, 2, 3, 4, 5, 6];
    failedShots = new Set();
    loadedShots = new Set();

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
  $: isMultiService = services.length > 1;
  $: categoryLabel = view ? categoryDisplayName(view.category, catalog?.categories || {}) : '';

  // Credentials con resolveEnvRef
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

  // URL base de screenshots · catálogo público
  $: screenshotBaseUrl = `https://raw.githubusercontent.com/andresgv-beep/NimOs-appstore/main/screenshots/${appId}`;

  // Hay app instalada Y corriendo (para botón "Abrir" funcional)
  $: canOpen = view?.installed && view.status === 'running';
  // Hay app instalada pero parada (botón "Abrir" deshabilitado + nota)
  $: isStopped = view?.installed && view.status !== 'running';

  // ── Acciones ───────────────────────────────────────────────────────
  function handleBack() {
    dispatch('back');
  }

  function handleInstall() {
    if (!view) return;
    mode = 'installing';
  }

  async function handleInstallDone() {
    mode = 'detail';
    await new Promise((r) => setTimeout(r, 600));
    await load();
    if (view && !view.installed) {
      await new Promise((r) => setTimeout(r, 1000));
      await load();
    }
  }

  function handleInstallCancel() {
    mode = 'detail';
  }

  function handleOpen() {
    if (!view?.installed) return;
    const port = view.runtime?.ports?.[0]?.host || view.catalog?.port;
    if (!port) {
      actionError = 'La app no tiene puerto expuesto · no se puede abrir.';
      return;
    }
    const isExternal = view.catalog?.openMode === 'external';
    if (isExternal) {
      const host = window.location.hostname;
      const proto = window.location.protocol;
      window.open(`${proto}//${host}:${port}`, '_blank');
      return;
    }
    openWindow(
      view.id,
      { width: 1100, height: 700 },
      { isWebApp: true, port, appName: view.name }
    );
  }

  function handleUninstallClick() {
    if (!view?.installed || uninstallProcessing) return;
    confirmUninstallOpen = true;
  }

  async function handleUninstallConfirm() {
    if (!view?.installed) return;
    uninstallProcessing = true;
    actionError = '';
    try {
      await uninstallApp(view.id, 'stack');
      confirmUninstallOpen = false;
      dispatch('back');
    } catch (err) {
      actionError = err?.message || String(err);
      confirmUninstallOpen = false;
    } finally {
      uninstallProcessing = false;
    }
  }

  function handleUninstallCancel() {
    if (uninstallProcessing) return;
    confirmUninstallOpen = false;
  }

  // Credentials · copiar al clipboard
  async function copyToClipboard(value, label) {
    if (!value) return;
    try {
      await navigator.clipboard.writeText(value);
      copyFeedback = `${label} copiado`;
      setTimeout(() => { copyFeedback = ''; }, 1500);
    } catch (err) {
      copyFeedback = 'Error al copiar';
      setTimeout(() => { copyFeedback = ''; }, 2000);
    }
  }

  // Screenshots · marca un slot como fallido
  function handleShotError(n) {
    failedShots.add(n);
    failedShots = failedShots; // forzar reactividad
  }

  // Screenshots · marca un slot como cargado (para no mostrar antes de cargar)
  function handleShotLoad(n) {
    loadedShots.add(n);
    loadedShots = loadedShots;
  }

  // ¿Hay al menos un screenshot que ha cargado?
  $: hasAnyScreenshot = visibleShots.some((n) => loadedShots.has(n));
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
{:else if view}
  <div class="detail-scroll">
    <!-- Back -->
    <button class="back-btn" on:click={handleBack} type="button">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <polyline points="15 18 9 12 15 6" />
      </svg>
      Volver al catálogo
    </button>

    <!-- HERO horizontal -->
    <section class="hero">
      <div class="hero-icon" style={view.color ? `background: ${view.color}; box-shadow: 0 0 32px ${view.color}33` : ''}>
        {#if !iconError && view.icon}
          <img src={view.icon} alt={view.name} on:error={() => (iconError = true)} />
        {:else}
          <span class="hero-icon-fallback">{view.name.charAt(0).toUpperCase()}</span>
        {/if}
      </div>

      <div class="hero-info">
        <h1 class="hero-name">{view.name}</h1>
        <div class="hero-cat">
          {categoryLabel}
          {#if view.catalog?.official} · <span class="badge-official">Oficial</span>{/if}
        </div>
        <div class="hero-tags">
          {#if view.catalog?.port}
            <span class="tag tag-port">{formatPort(view.catalog.port)}</span>
          {/if}
          <span class="tag">Docker</span>
          {#if isMultiService}
            <span class="tag">Multi-servicio</span>
          {/if}
          {#if view.installed}
            <span class="tag tag-status" class:ok={tone === 'ok'} class:warn={tone === 'warn'} class:crit={tone === 'crit'}>
              <span class="status-dot"></span>
              {formatStatus(view.status)}
              {#if view.health && view.health !== 'unknown'} · {formatHealth(view.health)}{/if}
            </span>
          {/if}
        </div>
      </div>

      <div class="hero-action">
        {#if mode === 'installing'}
          <button class="btn btn-primary" disabled>
            <span class="spinner"></span>
            Instalando…
          </button>
        {:else if !view.installed}
          <button class="btn btn-primary" on:click={handleInstall}>
            Instalar {view.name}
          </button>
        {:else if canOpen}
          <button class="btn btn-primary" on:click={handleOpen}>
            Abrir {view.name}
          </button>
        {:else if isStopped}
          <button class="btn btn-primary" disabled title="Inicia el contenedor desde NimHealth">
            Abrir {view.name}
          </button>
        {/if}
      </div>
    </section>

    {#if isStopped}
      <div class="hint-row">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>
        La app está detenida. Inicia el contenedor desde <strong>NimHealth → Task Manager</strong>.
      </div>
    {/if}

    {#if actionError}
      <div class="error-row">{actionError}</div>
    {/if}

    <!-- INSTALL FLOW (embedded inline · NO sustituye el hero) -->
    {#if mode === 'installing'}
      <section class="install-section">
        <InstallFlow view={view} on:done={handleInstallDone} on:cancel={handleInstallCancel} embedded={true} />
      </section>
    {/if}

    <!-- Preload silencioso · imágenes invisibles fuera del DOM visible para
         disparar load/error sin parpadeo. Cuando una carga OK, se renderiza
         en el carrusel visible de abajo. -->
    <div class="shot-probes" aria-hidden="true">
      {#each visibleShots as n (n)}
        {#if !loadedShots.has(n) && !failedShots.has(n)}
          <img
            src="{screenshotBaseUrl}/{n}.png"
            alt=""
            on:load={() => handleShotLoad(n)}
            on:error={() => handleShotError(n)}
          />
        {/if}
      {/each}
    </div>

    <!-- SCREENSHOTS · carrusel horizontal con scroll snap -->
    {#if hasAnyScreenshot}
      <section class="section">
        <h2 class="section-title">Capturas</h2>
        <div class="screenshots">
          {#each visibleShots as n (n)}
            {#if loadedShots.has(n)}
              <div class="shot">
                <img
                  src="{screenshotBaseUrl}/{n}.png"
                  alt="{view.name} captura {n}"
                  loading="lazy"
                />
              </div>
            {/if}
          {/each}
        </div>
      </section>
    {/if}

    <!-- DESCRIPCIÓN -->
    {#if view.description}
      <section class="section">
        <h2 class="section-title">Acerca de</h2>
        <p class="description">{view.description}</p>
      </section>
    {/if}

    <!-- INFO TÉCNICA · cards grid 2 columnas estilo mockup -->
    <section class="section">
      <h2 class="section-title">Información técnica</h2>
      <div class="info-grid">
        <!-- Fila 1: Imagen Docker | Puerto -->
        <div class="info-card">
          <span class="info-card-k">Imagen Docker</span>
          <code class="info-card-v">{view.catalog?.image || '—'}</code>
        </div>
        <div class="info-card">
          <span class="info-card-k">Puerto</span>
          <code class="info-card-v">{view.catalog?.port ? formatPort(view.catalog.port) : '—'}</code>
        </div>

        <!-- Fila 2: Modo apertura | Servicios (si hay) o Container (si instalada) -->
        {#if view.catalog?.openMode}
          <div class="info-card">
            <span class="info-card-k">Modo de apertura</span>
            <code class="info-card-v">{view.catalog.openMode}</code>
          </div>
        {/if}
        {#if services.length > 1}
          <!-- Servicios multi: ocupa ambas columnas en su propia fila para que los chips quepan -->
          <div class="info-card info-card-wide">
            <span class="info-card-k">Servicios ({services.length})</span>
            <div class="info-card-chips">
              {#each services as svc}
                <code class="service-chip">{svc}</code>
              {/each}
            </div>
          </div>
        {:else if services.length === 1}
          <div class="info-card">
            <span class="info-card-k">Servicio</span>
            <code class="info-card-v">{services[0]}</code>
          </div>
        {/if}

        <!-- Fila 3 (solo si instalada): Container name -->
        {#if view.installed && view.runtime?.containerName}
          <div class="info-card info-card-wide">
            <span class="info-card-k">Container</span>
            <code class="info-card-v">{view.runtime.containerName}</code>
          </div>
        {/if}
      </div>
    </section>

    <!-- CREDENCIALES por defecto · siempre visibles (instalada o no) -->
    {#if credentials && (credentials.username || credentials.password)}
      <section class="section">
        <h2 class="section-title">Credenciales por defecto</h2>
        <div class="creds-block">
          {#if credentials.username}
            <div class="cred-row">
              <span class="cred-k">Usuario</span>
              <code class="cred-v">{credentials.username}</code>
              <button class="cred-btn" on:click={() => copyToClipboard(credentials.username, 'Usuario')} title="Copiar usuario" type="button">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                  <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                  <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                </svg>
              </button>
            </div>
          {/if}
          {#if credentials.password}
            <div class="cred-row">
              <span class="cred-k">Contraseña</span>
              <code class="cred-v" class:masked={!passwordVisible}>
                {#if passwordVisible}
                  {credentials.password}
                {:else}
                  {'•'.repeat(Math.min(credentials.password.length, 12))}
                {/if}
              </code>
              <button class="cred-btn" on:click={() => passwordVisible = !passwordVisible} title={passwordVisible ? 'Ocultar' : 'Mostrar'} type="button">
                {#if passwordVisible}
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/>
                    <line x1="1" y1="1" x2="23" y2="23"/>
                  </svg>
                {:else}
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/>
                    <circle cx="12" cy="12" r="3"/>
                  </svg>
                {/if}
              </button>
              <button class="cred-btn" on:click={() => copyToClipboard(credentials.password, 'Contraseña')} title="Copiar contraseña" type="button">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                  <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                  <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                </svg>
              </button>
              {#if credentials.passwordIsTemplate}
                <span class="cred-note">se genera al instalar</span>
              {/if}
            </div>
          {/if}
          <div class="creds-hint">
            Cambia la contraseña tras el primer inicio de sesión por seguridad.
          </div>
        </div>
        {#if copyFeedback}
          <div class="copy-feedback">{copyFeedback}</div>
        {/if}
      </section>
    {/if}

    <!-- DESINSTALAR · solo si instalada · sutil, abajo del todo -->
    {#if view.installed && mode === 'detail'}
      <section class="section uninstall-section">
        <button class="btn btn-danger-soft" on:click={handleUninstallClick} disabled={uninstallProcessing} type="button">
          Desinstalar {view.name}
        </button>
        <p class="uninstall-hint">
          Esto detendrá el contenedor y borrará volúmenes y configuración.
        </p>
      </section>
    {/if}
  </div>
{/if}

<!-- Confirm dialog uninstall -->
{#if view}
  <ConfirmDialog
    bind:open={confirmUninstallOpen}
    title="Desinstalar {view.name}"
    message="Esta acción detendrá y eliminará el contenedor, los volúmenes asociados y la configuración guardada. Escribe el nombre de la app para confirmar."
    confirmLabel="Desinstalar"
    cancelLabel="Cancelar"
    variant="danger"
    inputConfirm={view.id}
    processing={uninstallProcessing}
    on:confirm={handleUninstallConfirm}
    on:cancel={handleUninstallCancel}
  />
{/if}

<style>
  /* ═══ Estados loading/error ═══ */
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
    width: 8px; height: 8px;
    border-radius: 50%;
    background: var(--signal);
    animation: pulse 1.4s ease-in-out infinite;
  }
  @keyframes pulse {
    0%, 100% { opacity: 0.3; transform: scale(0.9); }
    50%      { opacity: 1;   transform: scale(1.1); }
  }
  .state-text { color: var(--ink-mute); font-family: var(--font-mono); font-size: var(--fs-11); }
  .err-title { color: var(--crit); font-weight: 600; font-size: var(--fs-13); }
  .err-body { color: var(--ink-dim); font-family: var(--font-mono); font-size: var(--fs-11); max-width: 420px; word-break: break-word; }

  /* ═══ Scroll container ═══ */
  .detail-scroll {
    height: 100%;
    overflow-y: auto;
    padding: var(--sp-4) var(--sp-5) var(--sp-6);
    width: 100%;
  }
  /* Wrapper interno que centra el contenido a max 920px sin sacar el scroll del marco */
  .detail-scroll > * {
    max-width: 920px;
    margin-left: auto;
    margin-right: auto;
    width: 100%;
  }
  .detail-scroll {
    display: block;
  }
  /* Separación entre secciones (sustituye el gap del flex) */
  .detail-scroll > * + * {
    margin-top: var(--sp-5);
  }

  /* ═══ Back button ═══ */
  .back-btn {
    background: transparent;
    border: 1px solid var(--line);
    color: var(--ink-dim);
    cursor: pointer;
    font-size: var(--fs-12);
    font-family: inherit;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 6px 12px;
    border-radius: var(--radius-sm);
    transition: background 0.12s, color 0.12s, border-color 0.12s;
    align-self: flex-start;
  }
  .back-btn:hover {
    color: var(--ink);
    background: var(--line);
    border-color: var(--line-bright);
  }
  .back-btn svg { width: 13px; height: 13px; }

  /* ═══ HERO horizontal ═══ */
  .hero {
    display: flex;
    align-items: center;
    gap: var(--sp-4);
    padding: var(--sp-3) 0;
  }
  .hero-icon {
    width: 96px;
    height: 96px;
    border-radius: 22px;
    background: var(--canvas);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    overflow: hidden;
    position: relative;
    transition: box-shadow 0.3s;
  }
  .hero-icon img {
    width: 64px;
    height: 64px;
    object-fit: contain;
  }
  .hero-icon-fallback {
    color: var(--ink);
    font-size: 40px;
    font-weight: 700;
    font-family: var(--font-mono);
  }
  .hero-info {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .hero-name {
    font-size: var(--fs-22);
    font-weight: 600;
    color: var(--ink);
    margin: 0;
    letter-spacing: -0.4px;
    line-height: 1.1;
  }
  .hero-cat {
    font-size: var(--fs-12);
    color: var(--ink-mute);
  }
  .badge-official {
    color: var(--signal);
    font-family: var(--font-mono);
    font-size: var(--fs-10);
    letter-spacing: 0.5px;
    text-transform: uppercase;
  }
  .hero-tags {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-top: 4px;
  }
  .tag {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 2px 9px;
    background: var(--canvas);
    border: 1px solid var(--line);
    border-radius: 999px;
    font-size: var(--fs-10);
    color: var(--ink-dim);
    font-family: var(--font-mono);
    line-height: 1.5;
  }
  .tag-port {
    color: var(--info);
    border-color: var(--info);
    background: var(--info-dim, var(--panel-deep));
  }
  .tag-status .status-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--ink-faint);
  }
  .tag-status.ok .status-dot { background: var(--signal); box-shadow: 0 0 4px var(--signal-glow); }
  .tag-status.warn .status-dot { background: var(--warn); box-shadow: 0 0 4px var(--warn-glow); }
  .tag-status.crit .status-dot { background: var(--crit); box-shadow: 0 0 4px var(--crit-glow); }

  .hero-action {
    flex-shrink: 0;
  }

  /* ═══ Hint stop · NimHealth para gestionar ═══ */
  .hint-row {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    padding: 10px 14px;
    background: var(--canvas);
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    color: var(--ink-dim);
    font-size: var(--fs-12);
  }
  .hint-row svg { width: 16px; height: 16px; color: var(--warn); flex-shrink: 0; }
  .hint-row strong { color: var(--ink); font-weight: 600; }

  /* ═══ Error row inline ═══ */
  .error-row {
    padding: 8px 12px;
    background: var(--crit-dim);
    border: 1px solid var(--crit-border);
    border-radius: var(--radius-sm);
    color: var(--ink);
    font-size: var(--fs-11);
    font-family: var(--font-mono);
    word-break: break-word;
  }

  /* ═══ Section common ═══ */
  .section {
    display: flex;
    flex-direction: column;
    gap: var(--sp-3);
  }
  .section-title {
    font-size: var(--fs-13);
    font-weight: 600;
    color: var(--ink);
    margin: 0;
    text-transform: uppercase;
    letter-spacing: 0.7px;
    color: var(--ink-mute);
  }

  /* ═══ Install section (embedded) ═══ */
  .install-section {
    background: var(--canvas);
    border: 1px solid var(--line);
    border-radius: var(--radius-md);
    padding: var(--sp-4);
  }

  /* ═══ Screenshots · carrusel horizontal con scroll snap ═══ */
  .shot-probes {
    position: absolute;
    width: 0;
    height: 0;
    overflow: hidden;
    pointer-events: none;
    opacity: 0;
  }
  .screenshots {
    display: flex;
    gap: var(--sp-3);
    overflow-x: auto;
    overflow-y: hidden;
    scroll-snap-type: x mandatory;
    scroll-behavior: smooth;
    padding: 0 0 12px;
    /* Permite "salir" del padding del scroll a izquierda/derecha al hacer scroll */
    margin: 0 calc(var(--sp-5) * -1);
    padding-left: var(--sp-5);
    padding-right: var(--sp-5);
    scrollbar-width: thin;
    scrollbar-color: var(--line-bright) transparent;
  }
  .screenshots::-webkit-scrollbar { height: 8px; }
  .screenshots::-webkit-scrollbar-track {
    background: transparent;
    border-radius: 4px;
  }
  .screenshots::-webkit-scrollbar-thumb {
    background: var(--line-bright);
    border-radius: 4px;
  }
  .screenshots::-webkit-scrollbar-thumb:hover {
    background: var(--ink-faint);
  }
  .shot {
    flex: 0 0 auto;
    width: 340px;
    aspect-ratio: 16 / 10;
    border-radius: var(--radius-md);
    overflow: hidden;
    background: var(--canvas);
    border: 1px solid var(--line);
    cursor: zoom-in;
    transition: transform 0.15s, border-color 0.15s;
    scroll-snap-align: start;
  }
  .shot:hover {
    transform: translateY(-2px);
    border-color: var(--line-bright);
  }
  .shot img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }
  /* Responsive · screenshots más pequeñas en móvil */
  @media (max-width: 640px) {
    .shot { width: 260px; }
  }

  /* ═══ Description ═══ */
  .description {
    color: var(--ink-dim);
    font-size: var(--fs-13);
    line-height: 1.65;
    margin: 0;
  }

  /* ═══ Info técnica · cards individuales en grid 2 columnas (mockup style) ═══ */
  .info-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--sp-2);
    padding: var(--sp-2) 0;
  }
  .info-card {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-3);
    background: var(--canvas);
    border: 1px solid var(--line);
    border-radius: var(--radius-md);
    padding: 12px 14px;
  }
  /* Servicios y otras filas que ocupan ambas columnas */
  .info-card-wide {
    grid-column: 1 / -1;
    align-items: center;
    flex-wrap: wrap;
  }
  .info-card-k {
    color: var(--ink-mute);
    font-size: var(--fs-11);
    flex-shrink: 0;
  }
  .info-card-v {
    color: var(--ink);
    font-family: var(--font-mono);
    font-size: var(--fs-11);
    word-break: break-all;
    text-align: right;
  }
  .info-card-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    justify-content: flex-end;
  }
  .service-chip {
    color: var(--ink-dim);
    background: var(--panel-deep);
    padding: 3px 9px;
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: var(--fs-11);
  }

  /* ═══ Credenciales · card sólida con filas separadas (estilo mockup) ═══ */
  .creds-block {
    display: flex;
    flex-direction: column;
    background: var(--canvas);
    border: 1px solid var(--line);
    border-radius: var(--radius-md);
    overflow: hidden;
  }
  .cred-row {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    padding: 16px 18px;
    font-size: var(--fs-12);
    border-bottom: 1px solid var(--line);
  }
  .cred-row:last-of-type {
    border-bottom: none;
  }
  .cred-k {
    color: var(--ink-mute);
    min-width: 100px;
    font-size: var(--fs-10);
    text-transform: uppercase;
    letter-spacing: 0.5px;
  }
  .cred-v {
    color: var(--ink);
    font-family: var(--font-mono);
    font-size: var(--fs-12);
    flex: 1;
    min-width: 0;
    word-break: break-all;
    background: transparent;
    padding: 0;
  }
  .cred-v.masked {
    letter-spacing: 2px;
  }
  .cred-btn {
    background: transparent;
    border: 1px solid var(--line);
    color: var(--ink-mute);
    border-radius: 4px;
    padding: 4px;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    transition: color 0.12s, background 0.12s, border-color 0.12s;
  }
  .cred-btn:hover {
    color: var(--ink);
    background: var(--line);
    border-color: var(--line-bright);
  }
  .cred-btn svg { width: 12px; height: 12px; }
  .cred-note {
    color: var(--info);
    font-size: var(--fs-10);
    font-style: italic;
  }
  .creds-hint {
    color: var(--ink-mute);
    font-size: var(--fs-11);
    padding: 14px 18px;
    border-top: 1px solid var(--line);
  }
  .copy-feedback {
    font-size: var(--fs-11);
    color: var(--signal);
    padding: 4px 0;
    text-align: right;
    font-family: var(--font-mono);
  }

  /* ═══ Uninstall section · sutil al final ═══ */
  .uninstall-section {
    margin-top: var(--sp-3);
    padding-top: var(--sp-4);
    border-top: 1px solid var(--line);
    align-items: flex-start;
  }
  .uninstall-hint {
    color: var(--ink-mute);
    font-size: var(--fs-11);
    margin: 0;
  }

  /* ═══ Botones ═══ */
  .btn {
    padding: 10px 22px;
    border-radius: var(--radius-sm);
    border: 1px solid transparent;
    font-size: var(--fs-12);
    font-weight: 600;
    font-family: inherit;
    cursor: pointer;
    transition: filter 0.12s, background 0.12s, color 0.12s, border-color 0.12s;
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .btn:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
  .btn-primary {
    background: var(--signal);
    color: var(--canvas);
  }
  .btn-primary:not(:disabled):hover { filter: brightness(1.08); }
  .btn-secondary {
    background: transparent;
    color: var(--ink-dim);
    border-color: var(--line);
  }
  .btn-secondary:not(:disabled):hover {
    color: var(--ink);
    background: var(--line);
  }
  .btn-danger-soft {
    background: transparent;
    color: var(--crit);
    border-color: var(--crit-border);
    padding: 8px 16px;
    font-size: var(--fs-12);
  }
  .btn-danger-soft:not(:disabled):hover {
    background: var(--crit-dim);
  }

  /* Spinner inline para botón "Instalando..." */
  .spinner {
    width: 12px;
    height: 12px;
    border: 2px solid currentColor;
    border-top-color: transparent;
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
</style>
