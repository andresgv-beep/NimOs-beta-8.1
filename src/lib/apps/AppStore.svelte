<script>
  /**
   * AppStore · Entry point del módulo (con DEBUG TEMPORAL)
   * ────────────────────────────────────────────────────────
   * En esta versión TEMPORAL muestra el JSON crudo de /api/services
   * para inspeccionar el shape real desde dentro de la propia ventana.
   * Quitar este código tras debug · ver comentario "DEBUG" abajo.
   */

  import { onMount } from 'svelte';
  import { getCapabilities } from './appstore/api.js';
  import AppStoreSetup from './appstore/AppStoreSetup.svelte';

  /** @typedef {import('./appstore/types').AppStoreCapabilities} AppStoreCapabilities */

  /** @type {AppStoreCapabilities | null} */
  let capabilities = null;
  let loading = true;
  let loadError = '';

  // ── DEBUG temporal · respuesta cruda de /api/services ─────────────
  /** @type {any} */
  let debugServicesRaw = null;
  let debugServicesError = '';

  onMount(async () => {
    await loadCapabilities();
    // Pedir /api/services en paralelo para inspección.
    // Cuando arreglemos getCapabilities, este bloque se quita.
    try {
      const res = await fetch('/api/services', { credentials: 'include' });
      const text = await res.text();
      try {
        debugServicesRaw = JSON.parse(text);
      } catch {
        debugServicesRaw = { _rawText: text };
      }
    } catch (err) {
      debugServicesError = err?.message || String(err);
    }
  });

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

  // ── DEBUG · derivar un resumen del Docker engine si está en la lista ──
  function findDockerCandidate(raw) {
    if (!raw || typeof raw !== 'object') return null;
    const list = raw.services || raw.data?.services || raw.data || raw;
    if (!Array.isArray(list)) return null;
    // Buscar cualquier service que tenga "docker" en algún campo string
    return list.filter((s) => {
      if (!s) return false;
      const str = JSON.stringify(s).toLowerCase();
      return str.includes('docker') || str.includes('container');
    });
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
{:else}
  <!-- ═════════════ DEBUG TEMPORAL ═════════════
       Muestra el JSON crudo de /api/services y los flags derivados.
       Quitar este bloque entero (incluido el {:else}/{/if}) cuando
       getCapabilities() esté arreglado. -->
  <div class="debug-pane">
    <div class="debug-head">DEBUG TEMPORAL · respuesta cruda de /api/services</div>

    <div class="debug-section">
      <div class="debug-label">Capabilities derivadas:</div>
      <pre class="debug-block">{JSON.stringify(capabilities, null, 2)}</pre>
    </div>

    {#if debugServicesError}
      <div class="debug-section">
        <div class="debug-label">ERROR al pedir /api/services:</div>
        <pre class="debug-block err">{debugServicesError}</pre>
      </div>
    {:else if debugServicesRaw}
      <div class="debug-section">
        <div class="debug-label">
          Top-level keys de la respuesta:
          <span class="debug-meta">{Object.keys(debugServicesRaw).join(', ')}</span>
        </div>
      </div>

      {#if findDockerCandidate(debugServicesRaw)}
        <div class="debug-section">
          <div class="debug-label">Candidatos relacionados con Docker:</div>
          <pre class="debug-block">{JSON.stringify(findDockerCandidate(debugServicesRaw), null, 2)}</pre>
        </div>
      {/if}

      <details class="debug-section">
        <summary>Respuesta cruda completa (clic para expandir)</summary>
        <pre class="debug-block raw">{JSON.stringify(debugServicesRaw, null, 2)}</pre>
      </details>
    {:else}
      <div class="debug-section">
        <div class="debug-label">Cargando /api/services…</div>
      </div>
    {/if}

    <div class="debug-footer">
      Copia el contenido relevante al chat para que Claude lo analice.
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

  /* ═══ DEBUG TEMPORAL ═══ */
  .debug-pane {
    height: 100%;
    overflow-y: auto;
    padding: var(--sp-4) var(--sp-5);
    background: var(--panel-elev);
    color: var(--ink-dim);
    font-family: var(--font-mono);
    font-size: var(--fs-11);
    line-height: 1.55;
  }
  .debug-head {
    color: var(--warn);
    font-weight: 600;
    margin-bottom: var(--sp-3);
    padding-bottom: var(--sp-2);
    border-bottom: 1px solid var(--warn-border);
    letter-spacing: 0.5px;
  }
  .debug-section {
    margin-bottom: var(--sp-4);
  }
  .debug-label {
    color: var(--ink);
    font-weight: 500;
    margin-bottom: var(--sp-2);
  }
  .debug-meta {
    color: var(--info);
    font-weight: 400;
    margin-left: var(--sp-2);
  }
  .debug-block {
    background: var(--canvas);
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    padding: var(--sp-3);
    overflow-x: auto;
    color: var(--ink-dim);
    font-size: var(--fs-11);
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 400px;
    overflow-y: auto;
  }
  .debug-block.err {
    border-color: var(--crit-border);
    background: var(--crit-dim);
    color: var(--ink);
  }
  .debug-block.raw {
    max-height: 600px;
  }
  details summary {
    cursor: pointer;
    color: var(--info);
    user-select: none;
    margin-bottom: var(--sp-2);
  }
  details summary:hover {
    color: var(--ink);
  }
  .debug-footer {
    margin-top: var(--sp-4);
    padding-top: var(--sp-3);
    border-top: 1px solid var(--line);
    color: var(--ink-mute);
    font-size: var(--fs-10);
    font-style: italic;
  }
</style>
