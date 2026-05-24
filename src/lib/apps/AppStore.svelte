<script>
  /**
   * AppStore · Entry point del módulo
   * ──────────────────────────────────
   * Decide qué pantalla mostrar según las capabilities del sistema:
   *
   *   1. ¿No hay pool? → AppStoreSetup (mockup 1 "sin pool")
   *   2. ¿No hay Docker? → AppStoreSetup (mockup 2 "sin docker")
   *   3. ¿Todo OK? → catálogo (Fase 3 · todavía placeholder)
   *
   * Esta Fase 2 entrega los casos 1 y 2. El caso 3 muestra un placeholder
   * temporal hasta que llegue Fase 3 con AppStoreOverview.
   *
   * La lógica de decisión vive en api.js::getCapabilities() · esta vista
   * solo es un router que reacciona al estado derivado.
   *
   * Reintento manual:
   *   El usuario puede pulsar "Reintentar" en cualquier empty state · eso
   *   re-invoca getCapabilities() y refresca el flujo. Útil tras crear un
   *   pool o instalar Docker desde otro tab.
   */

  import { onMount } from 'svelte';
  import { getCapabilities } from './appstore/api.js';
  import AppStoreSetup from './appstore/AppStoreSetup.svelte';

  /** @typedef {import('./appstore/types').AppStoreCapabilities} AppStoreCapabilities */

  /** @type {AppStoreCapabilities | null} */
  let capabilities = null;
  let loading = true;
  let loadError = '';

  onMount(loadCapabilities);

  async function loadCapabilities() {
    loading = true;
    loadError = '';
    try {
      capabilities = await getCapabilities();
    } catch (err) {
      loadError = err?.message || String(err);
      capabilities = null;
    } finally {
      loading = false;
    }
  }

  /**
   * Callback que AppStoreSetup invoca cuando termina · pedimos capabilities
   * de nuevo para detectar el cambio (Docker recién instalado, etc.) y
   * pasar al catálogo.
   */
  async function handleSetupReady() {
    await loadCapabilities();
  }
</script>

{#if loading}
  <div class="appstore-loading">
    <div class="loading-dot"></div>
    <div class="loading-text">Cargando…</div>
  </div>
{:else if loadError}
  <div class="appstore-error">
    <div class="err-title">No se pudo cargar el AppStore</div>
    <div class="err-body">{loadError}</div>
    <button class="err-btn" on:click={loadCapabilities}>Reintentar</button>
  </div>
{:else if !capabilities?.hasPool || !capabilities?.dockerInstalled}
  <!-- Setup · sin pool o sin Docker -->
  <AppStoreSetup {capabilities} onReady={handleSetupReady} />
{:else}
  <!-- Catálogo · Fase 3 implementará AppStoreOverview · placeholder mientras -->
  <div class="appstore-placeholder">
    <div class="ph-icon">⊞</div>
    <div class="ph-title">AppStore listo</div>
    <div class="ph-desc">
      Docker instalado y pool montado. El catálogo de apps se construye en
      la siguiente fase del frontend.
    </div>
    <div class="ph-status">
      <span>Docker:</span>
      <span class:ok={capabilities.dockerRunning} class:warn={!capabilities.dockerRunning}>
        {capabilities.dockerRunning ? 'running' : 'stopped'}
      </span>
    </div>
  </div>
{/if}

<style>
  /* ═══ Loading ═══ */
  .appstore-loading {
    height: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--sp-3);
    background: var(--panel-elev);
    color: var(--ink-mute);
    font-family: var(--font-mono);
    font-size: var(--fs-11);
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
  .loading-text {
    letter-spacing: 0.5px;
  }

  /* ═══ Error fatal · cuando getCapabilities falla del todo ═══ */
  .appstore-error {
    height: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--sp-3);
    padding: var(--sp-5);
    background: var(--panel-elev);
    color: var(--ink);
    text-align: center;
  }
  .err-title {
    font-size: var(--fs-13);
    font-weight: 600;
    color: var(--crit);
  }
  .err-body {
    font-size: var(--fs-11);
    color: var(--ink-dim);
    font-family: var(--font-mono);
    max-width: 420px;
    line-height: 1.55;
    word-break: break-word;
  }
  .err-btn {
    margin-top: var(--sp-2);
    padding: 8px 16px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--line);
    background: transparent;
    color: var(--ink-dim);
    font-size: var(--fs-12);
    font-family: inherit;
    cursor: pointer;
    transition: background 0.12s, color 0.12s;
  }
  .err-btn:hover {
    color: var(--ink);
    background: var(--line);
  }

  /* ═══ Placeholder · catálogo aún no implementado (Fase 3) ═══ */
  .appstore-placeholder {
    height: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--sp-3);
    padding: var(--sp-5);
    background: var(--panel-elev);
    color: var(--ink-dim);
    text-align: center;
    font-family: var(--font-sans);
  }
  .ph-icon {
    font-size: 32px;
    color: var(--signal);
    line-height: 1;
  }
  .ph-title {
    font-size: var(--fs-14);
    font-weight: 600;
    color: var(--ink);
  }
  .ph-desc {
    font-size: var(--fs-12);
    max-width: 420px;
    line-height: 1.55;
  }
  .ph-status {
    margin-top: var(--sp-2);
    padding: var(--sp-2) var(--sp-3);
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    font-size: var(--fs-11);
    font-family: var(--font-mono);
    color: var(--ink-mute);
    display: flex;
    gap: var(--sp-2);
  }
  .ph-status .ok { color: var(--signal); }
  .ph-status .warn { color: var(--warn); }
</style>
