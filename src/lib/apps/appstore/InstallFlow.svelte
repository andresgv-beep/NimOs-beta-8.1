<script>
  /**
   * InstallFlow · Pantalla del proceso de instalación de una app del catálogo
   * ───────────────────────────────────────────────────────────────────────
   * Cubre el patrón pull-then-deploy diseñado en Fase 1:
   *
   *   1. Pull async (operación larga · 10s-2min · imagen 300MB-2GB)
   *      · Usa /api/docker/pull/:image?async=true
   *      · Polling cada 1s con waitForOperation()
   *      · Reporta progreso visible al user
   *
   *   2. Stack deploy sync (operación corta · 2-5s · imagen ya local)
   *      · Usa /api/docker/stack
   *      · Bloquea esperando respuesta
   *      · Backend ya tiene CONFIG_PATH y HOST_IP auto-inyectados (APP-064)
   *
   *   3. Registro NimHealth automático (sync · ~150ms)
   *      · ForceDockerCacheRefresh() invocado en el handler del backend
   *
   * Steps visualizados como LEDs done/active/pending:
   *   ● Descargar imagen   (progress real del pull async)
   *   ● Desplegar stack    (active mientras dura el stack deploy)
   *   ● Registrar en NimHealth (último step · al success final)
   *
   * Props:
   *   - view: AppView   · app que se está instalando
   *   - onDone: () => void   · invocado al success (parent navega a detalle/catálogo)
   *   - onCancel: () => void · invocado si user cancela (vuelve al detalle)
   */

  import { onMount, onDestroy, createEventDispatcher } from 'svelte';
  import {
    pullImage,
    waitForOperation,
    installApp,
  } from './api.js';

  /** @typedef {import('./types').AppView} AppView */
  /** @typedef {import('./types').Operation} Operation */

  /** @type {AppView} */
  export let view;

  const dispatch = createEventDispatcher();

  // ── Estado del flow ────────────────────────────────────────────────
  /** Step actual · 'pull' | 'deploy' | 'register' | 'done' */
  let phase = 'pull';

  /** @type {Operation | null} */
  let pullOp = null;
  let installError = '';

  // AbortController para cancelar el polling si el componente se desmonta.
  /** @type {AbortController | null} */
  let pullAbort = null;

  // ── Steps visuales ─────────────────────────────────────────────────
  // Estado derivado de `phase` y `pullOp.progress`.
  $: steps = computeSteps(phase, pullOp);

  /**
   * @param {string} ph
   * @param {Operation | null} op
   * @returns {Array<{id: string, label: string, state: 'done'|'active'|'pending', detail?: string}>}
   */
  function computeSteps(ph, op) {
    const PHASES = ['pull', 'deploy', 'register', 'done'];
    const idx = PHASES.indexOf(ph);

    /** @param {string} stepPhase */
    const stateOf = (stepPhase) => {
      const stepIdx = PHASES.indexOf(stepPhase);
      if (idx > stepIdx) return 'done';
      if (idx === stepIdx) return 'active';
      return 'pending';
    };

    const pullDetail = op && ph === 'pull'
      ? (op.message || `Descargando · ${op.progress}%`)
      : ph === 'pull'
        ? 'Conectando con Docker Hub…'
        : '';

    return [
      {
        id: 'pull',
        label: 'Descargar imagen',
        state: stateOf('pull'),
        detail: pullDetail,
      },
      {
        id: 'deploy',
        label: 'Desplegar contenedor',
        state: stateOf('deploy'),
        detail: ph === 'deploy' ? 'Creando stack docker-compose…' : '',
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

  onDestroy(() => {
    if (pullAbort) pullAbort.abort();
  });

  async function start() {
    if (!view?.catalog) {
      installError = 'Datos del catálogo no disponibles';
      return;
    }
    const image = view.catalog.image;
    if (!image) {
      installError = 'La app no especifica imagen Docker';
      return;
    }

    pullAbort = new AbortController();
    phase = 'pull';
    pullOp = null;
    installError = '';

    try {
      // ── Step 1 · pull async ──
      const pullRes = await pullImage(image, { async: true });
      if (!pullRes?.operationId) {
        throw new Error('Backend no devolvió operationId en pull');
      }
      const finalPull = await waitForOperation(
        pullRes.operationId,
        (op) => {
          pullOp = op;
        },
        { signal: pullAbort.signal, intervalMs: 1000 }
      );
      pullOp = finalPull;
      if (finalPull.status !== 'succeeded') {
        throw new Error(
          finalPull.error || `Pull falló · status=${finalPull.status}`
        );
      }

      // ── Step 2 · stack deploy sync ──
      phase = 'deploy';
      await installApp({
        id: view.id,
        name: view.name,
        compose: view.catalog.compose,
        icon: view.icon,
        color: view.color,
        port: view.catalog.port,
        external: view.catalog.openMode === 'external',
      });

      // ── Step 3 · registro NimHealth ──
      // El backend ya hace ForceDockerCacheRefresh() en el handler. Aquí
      // solo mostramos visualmente que ese paso ocurrió. Pequeña pausa
      // para que el user vea el LED verde antes de cerrar.
      phase = 'register';
      await new Promise((r) => setTimeout(r, 400));

      phase = 'done';
      // Notificar al padre al cabo de un instante (para que vea el último
      // LED encenderse antes de cambiar de vista).
      setTimeout(() => {
        dispatch('done');
      }, 800);
    } catch (err) {
      if (err?.name === 'AbortError') return;
      const parts = [];
      parts.push(err?.message || String(err));
      if (err?.code) parts.push(`code: ${err.code}`);
      if (err?.status) parts.push(`status: ${err.status}`);
      installError = parts.join(' · ');
      console.error('[appstore/install] failed:', err);
    }
  }

  function handleCancel() {
    if (pullAbort) pullAbort.abort();
    dispatch('cancel');
  }

  function handleRetry() {
    installError = '';
    phase = 'pull';
    pullOp = null;
    start();
  }
</script>

<div class="install-flow">
  <!-- Hero compacto (icono + nombre) -->
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
    <div class="error-box">
      {installError}
    </div>
    <div class="actions">
      <button class="btn btn-primary" on:click={handleRetry}>Reintentar</button>
      <button class="btn btn-secondary" on:click={handleCancel}>Volver</button>
    </div>
  {:else}
    <!-- Steps con LEDs -->
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
            {#if step.id === 'pull' && step.state === 'active' && pullOp}
              <!-- Barra de progreso real del pull async -->
              <div class="progress-bar">
                <div class="progress-fill" style="width: {pullOp.progress || 0}%"></div>
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
        Puedes cerrar esta ventana · la instalación continúa en background.
      </p>
    {/if}
  {/if}
</div>

<style>
  .install-flow {
    height: 100%;
    overflow-y: auto;
    padding: var(--sp-5) var(--sp-5) var(--sp-5);
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
  /* Línea vertical entre LEDs */
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
  .step-led svg {
    width: 14px;
    height: 14px;
  }
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
  .step.pending .step-label {
    color: var(--ink-mute);
  }
  .step-detail {
    font-size: var(--fs-11);
    color: var(--ink-mute);
    margin-top: 3px;
    font-family: var(--font-mono);
  }

  /* Barra de progreso del pull async */
  .progress-bar {
    height: 4px;
    background: var(--panel-deep);
    border-radius: 2px;
    overflow: hidden;
    margin-top: 8px;
    max-width: 400px;
  }
  .progress-fill {
    height: 100%;
    background: var(--info);
    transition: width 0.4s ease-out;
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

  /* ═══ Actions + hint ═══ */
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
