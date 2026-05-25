<script>
  /**
   * AppCard · Card individual de una app del catálogo
   * ──────────────────────────────────────────────────
   * Se usa en el grid del AppStoreOverview (mockup 3) y potencialmente en
   * listas filtradas futuras. Maneja:
   *   · Render del icono desde URL (con fallback a placeholder si falla)
   *   · Indicador "instalada" (punto verde sutil esquina superior derecha)
   *   · Nombre + categoría display
   *   · Click delega via evento `select` con el appId
   *
   * El componente NO toma decisiones de filtrado · solo renderiza un AppView
   * pasado por el padre. Pasivo y reusable.
   */

  import { createEventDispatcher } from 'svelte';
  import { categoryDisplayName } from './formatters.js';

  /** @typedef {import('./types').AppView} AppView */

  /** @type {AppView} */
  export let app;
  /** Display map opcional para resolver category slug → "Multimedia" */
  /** @type {Object<string,string>} */
  export let categoriesMap = {};
  /** Sprint Updates · marca este card como "tiene actualización disponible".
   * Solo se renderiza si app.installed === true. Cuando true, muestra:
   *   - Badge azul "NUEVA" arriba izquierda
   * Cuando app.installed === true (sin importar hasUpdate), muestra:
   *   - Tic verde abajo derecha · "instalada · todo bien"
   */
  export let hasUpdate = false;

  const dispatch = createEventDispatcher();

  let iconError = false;

  function handleClick() {
    dispatch('select', { appId: app.id });
  }

  $: categoryLabel = categoryDisplayName(app.category, categoriesMap);
</script>

<button class="app-card" class:has-update={app.installed && hasUpdate} on:click={handleClick} type="button" title={app.description || app.name}>
  {#if app.installed && hasUpdate}
    <span class="update-badge" title="Actualización disponible">NUEVA</span>
  {/if}
  <div class="app-icon-wrap">
    {#if !iconError && app.icon}
      <img
        class="app-icon"
        src={app.icon}
        alt={app.name}
        on:error={() => (iconError = true)}
        loading="lazy"
      />
    {:else}
      <!-- Fallback: inicial sobre placeholder -->
      <div class="app-icon-fallback" style={app.color ? `background: ${app.color}` : ''}>
        {app.name.charAt(0).toUpperCase()}
      </div>
    {/if}
  </div>
  <div class="app-name">{app.name}</div>
  <div class="app-category">{categoryLabel}</div>
  {#if app.installed}
    <span class="installed-check" title="Instalada">
      <!-- Tic SVG inline · color verde via currentColor -->
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
        <polyline points="20 6 9 17 4 12"/>
      </svg>
    </span>
  {/if}
</button>

<style>
  .app-card {
    background: var(--panel-deep);
    border: 1px solid var(--line);
    border-radius: var(--radius-md);
    padding: 16px 14px 14px;
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    gap: 10px;
    cursor: pointer;
    transition: border-color 0.15s, transform 0.15s, background 0.15s;
    font-family: inherit;
    color: var(--ink);
    width: 100%;
    /* Contenedor para los absolutes .update-badge y .installed-check */
    position: relative;
  }
  .app-card:hover {
    border-color: var(--line-bright);
    background: var(--panel);
    transform: translateY(-1px);
  }
  .app-card:focus-visible {
    outline: 1px solid var(--info);
    outline-offset: 2px;
  }

  .app-icon-wrap {
    width: 56px;
    height: 56px;
    border-radius: 12px;
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    position: relative;
    background: var(--canvas);
    overflow: hidden;
  }
  .app-icon {
    width: 36px;
    height: 36px;
    object-fit: contain;
    display: block;
  }
  .app-icon-fallback {
    width: 100%;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--canvas-soft);
    color: var(--ink-dim);
    font-size: 22px;
    font-weight: 600;
    font-family: var(--font-mono);
  }

  /* ═══ Sprint Updates · indicadores de instalación + update ═══ */

  /* App-card en modo "tiene update" · sutil border azul para llamar la atención */
  .app-card.has-update {
    border-color: var(--info-dim, rgba(77, 184, 255, 0.3));
  }

  /* Badge "NUEVA" · esquina superior izquierda · solo si app.installed && hasUpdate */
  .update-badge {
    position: absolute;
    top: 6px;
    left: 6px;
    font-size: 9px;
    font-weight: 700;
    letter-spacing: 0.6px;
    padding: 3px 7px;
    border-radius: 4px;
    background: var(--info);
    color: var(--canvas);
    font-family: var(--font-mono);
    z-index: 3;
    box-shadow: 0 0 8px var(--info-glow, rgba(77, 184, 255, 0.4));
    text-transform: uppercase;
  }

  /* Tic verde · esquina inferior derecha · siempre si app.installed */
  .installed-check {
    position: absolute;
    bottom: 8px;
    right: 8px;
    width: 18px;
    height: 18px;
    border-radius: 50%;
    background: var(--signal);
    color: var(--canvas);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 2;
    box-shadow: 0 0 6px var(--signal-glow, rgba(0, 255, 159, 0.4));
  }
  .installed-check svg {
    width: 11px;
    height: 11px;
  }

  .app-name {
    font-size: var(--fs-13);
    color: var(--ink);
    font-weight: 600;
    line-height: 1.3;
    margin-top: 2px;
    word-break: break-word;
    max-width: 100%;
  }
  .app-category {
    font-size: var(--fs-11);
    color: var(--ink-mute);
    line-height: 1.3;
  }
</style>
