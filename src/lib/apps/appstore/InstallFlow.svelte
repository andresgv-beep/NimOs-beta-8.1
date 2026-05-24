<script>
  /**
   * InstallFlow · Proceso de instalación de una app del catálogo
   * ─────────────────────────────────────────────────────────────
   * Patrón SÍNCRONO simplificado (decisión tras Fase 5 hotfix):
   *
   *   1. Desplegar contenedor (sync · 30s-5min)
   *      · Una sola llamada a /api/docker/stack
   *      · Backend hace `docker compose up -d`, lo cual incluye `docker pull`
   *        automático si la imagen no es local
   *      · Sin pull explícito previo · evita dependencia de ?async=true
   *      · Barra indeterminada animada (no podemos reportar % real desde sync)
   *
   *   2. Registrar en NimHealth (~150ms)
   *      · El backend ya hace ForceDockerCacheRefresh() en el handler
   *      · Aquí solo damos pausa visual para que el user vea el último LED
   *
   * Si el browser corta la conexión durante el deploy (timeout proxy con
   * imágenes muy grandes), el backend SIGUE trabajando · al volver al detalle
   * la app aparecerá como instalada cuando capabilities/services se refresquen.
   *
   * Props:
   *   - view: AppView · app que se está instalando
   *
   * Eventos:
   *   - done · install completado con éxito
   *   - cancel · user pulsó cancelar (no aborta backend, solo cierra UI)
   */

  import { onMount, createEventDispatcher } from 'svelte';
  import { installApp } from './api.js';

  /** @typedef {import('./types').AppView} AppView */

  /** @type {AppView} */
  export let view;

  const dispatch = createEventDispatcher();

  // ── Estado del flow ────────────────────────────────────────────────
  /** 'deploy' | 'register' | 'done' */
  let phase = 'deploy';
  let installError = '';

  // ── Steps visuales ─────────────────────────────────────────────────
  $: steps = computeSteps(phase);

  /**
   * @param {string} ph
   * @returns {Array<{id: string, label: string, state: 'done'|'active'|'pending', detail?: string, showBar?: boolean}>}
   */
  function computeSteps(ph) {
    const PHASES = ['deploy', 'register', 'done'];
    const idx = PHASES.indexOf(ph);
    const stateOf = (stepPhase) => {
      const stepIdx = PHASES.indexOf(stepPhase);
      if (idx > stepIdx) return 'done';
      if (idx === stepIdx) return 'active';
      return 'pending';
    };

    return [
      {
        id: 'deploy',
        label: 'Descargar e instalar',
        state: stateOf('deploy'),
        detail: ph === 'deploy' ? 'Puede tardar varios minutos según el tamaño de la imagen…' : '',
        showBar: ph === 'deploy',
      },
      {
        id: 'register',
        label: 'Registrar en NimHealth',
        state: stateOf('register'),
        detail: ph === 'register' ? 'Actualizando catálogo de servicios…' : '',
      },
    ];
  }

  // ── Lifecycle ──────────────────────────────────────────────────────
  onMount(start);

  async function start() {
    if (!view?.catalog) {
      installError = 'Datos del catálogo no disponibles';
      return;
    }
    if (!view.catalog.compose) {
      installError = 'La app no especifica compose YAML';
      return;
    }

    phase = 'deploy';
    installError = '';

    try {
      // ── Step 1 · stack deploy sync ──
      // El backend ejecuta `docker compose up -d`, que hace pull automático
      // si la imagen no es local. Tarda 30s-5min según tamaño de la imagen
      // y velocidad de red.
      //
      // Pasamos view.catalog.env si existe · el backend lo mergea encima
      // de CONFIG_PATH/HOST_IP/TZ auto-inyectadas y expande referencias
      // ${VAR} entre los values antes de escribir el .env del stack.
      await installApp({
        id: view.id,
        name: view.name,
        compose: view.catalog.compose,
        icon: view.icon,
        color: view.color,
        port: view.catalog.port,
        external: view.catalog.openMode === 'external',
        env: view.catalog.env,
      });

      // ── Step 2 · pausa visual del registro NimHealth ──
      // El backend ya hizo ForceDockerCacheRefresh en el handler · esto es
      // solo para que el user vea el último LED encenderse antes de salir.
      phase = 'register';
      await new Promise((r) => setTimeout(r, 600));

      phase = 'done';
      setTimeout(() => {
        dispatch('done');
      }, 800);
    } catch (err) {
      const parts = [];
      parts.push(err?.message || String(err));
      if (err?.code) parts.push(`code: ${err.code}`);
      if (err?.status) parts.push(`status: ${err.status}`);
      installError = parts.join(' · ');
      console.error('[appstore/install] failed:', err);
    }
  }

  function handleCancel() {
    dispatch('cancel');
  }

  function handleRetry() {
    installError = '';
    phase = 'deploy';
    start();
  }
</script>

<div class="install-flow">
  <!-- Hero compacto -->
  <div class="hero">
    {#if view.icon}
      <img class="hero-icon" src={view.icon} alt={view.name} />
    {:else}
      <div class="hero-icon-fallback">{view.name.charAt(0)}</div>
    {/if}
    <div class="hero-text">
      <h1 class="hero-title">
        {#if installError}
          Instalación interrumpida
        {:else if phase === 'done'}
          ¡{view.name} instalada!
        {:else}
          Instalando {view.name}
        {/if}
      </h1>
      <div class="hero-meta">
        {view.catalog?.image || ''}
      </div>
    </div>
  </div>

  {#if installError}
    <div class="error-box">{installError}</div>
    <div class="actions">
      <button class="btn btn-primary" on:click={handleRetry}>Reintentar</button>
      <button class="btn btn-secondary" on:click={handleCancel}>Volver</button>
    </div>
  {:else}
    <ol class="steps">
      {#each steps as step (step.id)}
        <li class="step" class:done={step.state === 'done'} class:active={step.state === 'active'} class:pending={step.state === 'pending'}>
          <div class="step-led">
            {#if step.state === 'done'}
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round">
                <polyline points="5 12 10 17 19 7" />
              </svg>
            {:else if step.state === 'active'}
              <span class="led-pulse"></span>
            {/if}
          </div>
          <div class="step-text">
            <div class="step-label">{step.label}</div>
            {#if step.detail}
              <div class="step-detail">{step.detail}</div>
            {/if}
            {#if step.showBar && step.state === 'active'}
              <!-- Barra indeterminada · sin % real porque el sync deploy
                   no reporta progreso. La animación honesta indica "trabajando". -->
              <div class="install-bar">
                <div class="install-bar-fill"></div>
              </div>
            {/if}
          </div>
        </li>
      {/each}
    </ol>

    {#if phase !== 'done'}
      <div class="actions">
        <button class="btn btn-secondary" on:click={handleCancel}>Cancelar</button>
      </div>
      <p class="hint">
        No cierres esta ventana hasta que termine.
      </p>
    {/if}
  {/if}
</div>

<style>
  .install-flow {
    height: 100%;
    overflow-y: auto;
    padding: var(--sp-5) var(--sp-5);
    display: flex;
    flex-direction: column;
    gap: var(--sp-4);
    max-width: 640px;
    margin: 0 auto;
  }

  /* ═══ Hero ═══ */
  .hero {
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    padding: var(--sp-2) 0;
  }
  .hero-icon, .hero-icon-fallback {
    width: 56px;
    height: 56px;
    border-radius: 12px;
    flex-shrink: 0;
  }
  .hero-icon {
    object-fit: contain;
    background: var(--canvas);
    padding: 10px;
  }
  .hero-icon-fallback {
    background: var(--canvas-soft);
    color: var(--ink-dim);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 26px;
    font-weight: 600;
    font-family: var(--font-mono);
  }
  .hero-text { flex: 1; min-width: 0; }
  .hero-title {
    font-size: var(--fs-18);
    font-weight: 600;
    color: var(--ink);
    margin: 0 0 4px;
    letter-spacing: -0.3px;
  }
  .hero-meta {
    font-size: var(--fs-11);
    color: var(--ink-mute);
    font-family: var(--font-mono);
    word-break: break-all;
  }

  /* ═══ Steps ═══ */
  .steps {
    list-style: none;
    padding: 0;
    margin: var(--sp-2) 0;
    display: flex;
    flex-direction: column;
    gap: var(--sp-3);
  }
  .step {
    display: flex;
    align-items: flex-start;
    gap: var(--sp-3);
    position: relative;
    padding-left: 4px;
  }
  .step:not(:last-child)::before {
    content: '';
    position: absolute;
    left: 14px;
    top: 28px;
    width: 1px;
    height: calc(100% + var(--sp-3) - 28px);
    background: var(--line);
  }
  .step.done:not(:last-child)::before {
    background: var(--signal);
    opacity: 0.4;
  }

  .step-led {
    width: 24px;
    height: 24px;
    border-radius: 50%;
    border: 1.5px solid var(--ink-trace);
    background: var(--panel-elev);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    margin-top: 1px;
    color: var(--canvas);
    transition: border-color 0.2s, background 0.2s;
  }
  .step-led svg { width: 14px; height: 14px; }
  .led-pulse {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--info);
    animation: pulse-led 1.2s ease-in-out infinite;
  }
  @keyframes pulse-led {
    0%, 100% { opacity: 0.5; transform: scale(0.85); }
    50%      { opacity: 1;   transform: scale(1.15); }
  }
  .step.done .step-led {
    border-color: var(--signal);
    background: var(--signal);
  }
  .step.active .step-led {
    border-color: var(--info);
    background: var(--info-dim);
  }

  .step-text {
    flex: 1;
    min-width: 0;
    padding-top: 2px;
  }
  .step-label {
    font-size: var(--fs-13);
    font-weight: 500;
    color: var(--ink);
    line-height: 1.3;
  }
  .step.pending .step-label { color: var(--ink-mute); }
  .step-detail {
    font-size: var(--fs-11);
    color: var(--ink-mute);
    margin-top: 3px;
    font-family: var(--font-mono);
  }

  /* Barra indeterminada · gradient deslizándose */
  .install-bar {
    height: 4px;
    background: var(--panel-deep);
    border-radius: 2px;
    overflow: hidden;
    margin-top: 8px;
    max-width: 400px;
    border: 1px solid var(--line);
    position: relative;
  }
  .install-bar-fill {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 30%;
    background: linear-gradient(
      90deg,
      transparent 0%,
      var(--info) 50%,
      transparent 100%
    );
    animation: indeterminate 1.6s ease-in-out infinite;
  }
  @keyframes indeterminate {
    0%   { left: -30%; }
    100% { left: 100%; }
  }

  /* ═══ Error ═══ */
  .error-box {
    padding: var(--sp-3);
    background: var(--crit-dim);
    border: 1px solid var(--crit-border);
    border-radius: var(--radius-sm);
    color: var(--ink);
    font-size: var(--fs-12);
    font-family: var(--font-mono);
    line-height: 1.55;
    word-break: break-word;
  }

  /* ═══ Actions ═══ */
  .actions {
    display: flex;
    gap: var(--sp-2);
  }
  .btn {
    padding: 9px 18px;
    border-radius: var(--radius-sm);
    border: 1px solid transparent;
    font-size: var(--fs-12);
    font-weight: 600;
    font-family: inherit;
    cursor: pointer;
    transition: background 0.12s, color 0.12s, filter 0.12s;
  }
  .btn-primary {
    background: var(--signal);
    color: var(--canvas);
  }
  .btn-primary:hover { filter: brightness(1.08); }
  .btn-secondary {
    background: transparent;
    color: var(--ink-dim);
    border-color: var(--line);
  }
  .btn-secondary:hover {
    color: var(--ink);
    background: var(--line);
  }
  .hint {
    font-size: var(--fs-11);
    color: var(--ink-mute);
    margin: 0;
  }
</style>
