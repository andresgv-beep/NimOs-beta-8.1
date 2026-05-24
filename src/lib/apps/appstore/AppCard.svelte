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
  import { categoryDisplayName, statusTone } from './formatters.js';

  /** @typedef {import('./types').AppView} AppView */

  /** @type {AppView} */
  export let app;
  /** Display map opcional para resolver category slug → "Multimedia" */
  /** @type {Object<string,string>} */
  export let categoriesMap = {};

  const dispatch = createEventDispatcher();

  let iconError = false;

  function handleClick() {
    dispatch('select', { appId: app.id });
  }

  $: tone = app.installed ? statusTone(app.status, app.health) : 'muted';
  $: categoryLabel = categoryDisplayName(app.category, categoriesMap);
</script>

<button class="app-card" on:click={handleClick} type="button" title={app.description || app.name}>
  <div class="app-icon-wrap" class:installed={app.installed}>
    {#if app.installed}
      <span class="installed-dot" class:ok={tone === 'ok'} class:warn={tone === 'warn'} class:crit={tone === 'crit'} class:info={tone === 'info'}></span>
    {/if}
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

  /* Indicador instalada · pill superior derecha */
  .installed-dot {
    position: absolute;
    top: 4px;
    right: 4px;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--signal);
    box-shadow: 0 0 5px var(--signal-glow);
    z-index: 2;
  }
  .installed-dot.warn {
    background: var(--warn);
    box-shadow: 0 0 5px var(--warn-glow);
  }
  .installed-dot.crit {
    background: var(--crit);
    box-shadow: 0 0 5px var(--crit-glow);
  }
  .installed-dot.info {
    background: var(--info);
    box-shadow: 0 0 5px var(--info-glow);
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
