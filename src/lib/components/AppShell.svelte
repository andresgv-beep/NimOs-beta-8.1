<script>
  /**
   * AppShell · Envoltorio estándar de apps NimOS Beta 8.1 · v3.1
   * ─────────────────────────────────────────────────────────────
   * Provee el chrome común: titlebar con cubo 45° + path + LEDs,
   * sidebar con secciones, main area, footer interno opcional.
   *
   * CAMBIOS v3.1 (aditivos, sin romper apps existentes):
   *   · Nueva prop `sidebarWidth` (default 220px, antes 240px del CSS var).
   *     Storage/NimHealth/Settings/etc pasan a 220 sin modificar nada.
   *   · Nueva prop `showSidebar` (default true). Modo false declarado,
   *     diseño visual PENDIENTE de mockup — ver TODO abajo.
   *   · Nuevo slot `sidebar-content` que sustituye al render automático
   *     de `sections` cuando se pasa. Permite TreeNode (Files), o
   *     cualquier sidebar dinámico que no cabe en items planos.
   *   · Nuevo slot `sidebar-header` que sustituye al `.sb-header`
   *     interno (icono+título). Permite SVG inline en apps que lo
   *     necesiten sin perder consistencia.
   *
   * APP SHELL · ÚNICA FUENTE de:
   *   - dimensiones del sidebar (--sidebar-width)
   *   - estética del titlebar (cubo + path + LEDs)
   *   - patrón de items del sidebar (sb-item, sb-section)
   *
   * `documents/sidebar-tokens (1).css` queda DEPRECADO en este v3.1.
   * Eliminar en el paso 2 del refactor (limpieza tokens).
   *
   * Uso típico (sin cambios respecto a v3):
   *   <AppShell
   *     appId="nimhealth"
   *     title="NimHealth"
   *     headerIcon="♥"
   *     sections={[
   *       { label: 'Monitor', items: [
   *         { id: 'task', label: 'Task Manager', keyHint: 'T' },
   *       ]},
   *     ]}
   *     bind:active
   *     pathSegments={['health', 'task-manager']}
   *   >
   *     [contenido principal de la app]
   *   </AppShell>
   *
   * Uso avanzado v3.1 (Files):
   *   <AppShell
   *     appId="files"
   *     title="Files"
   *     pathSegments={['files', ...]}
   *   >
   *     <svelte:fragment slot="sidebar-content">
   *       <!-- TreeNode + grupos Local/Remoto -->
   *     </svelte:fragment>
   *     [contenido]
   *   </AppShell>
   *
   * TODO (Beta 8.2 o cuando llegue mockup):
   *   - Implementar estética modo `showSidebar={false}`. La prop ya
   *     existe y oculta el aside, pero el main hereda padding/spacing
   *     pensados para la versión con sidebar. Diseñar cuando haya
   *     mockup canónico de ventana sin sidebar.
   *
   * Estética Beta 8.1 (sin cambios):
   *   - Cubo 45° blanco con drop-shadow lechoso del boot (firma NimOS)
   *   - Path mono `nimos://app/seccion` · app luminoso, contexto en gris
   *   - LEDs C2: min/max/close cuadrados 10×10 con glow dramático
   *   - LEDs orden: min · max · close (close al final, protege accidentes)
   *   - Sin glass, sin border-radius
   */
  import { getContext } from 'svelte';
  import { user } from '$lib/stores/auth.js';
  import LED from '$lib/ui/LED.svelte';
  import KeyBind from '$lib/ui/KeyBind.svelte';
  import Badge from '$lib/ui/Badge.svelte';

  export let appId = '';
  export let title = '';
  export let headerIcon = '◆';
  export let sections = [];
  export let active = '';
  /** pathSegments: segmentos del path tras el host, ej ['health','task-manager'] */
  export let pathSegments = [];
  /** Footer interno que muestra daemon status + versión */
  export let showDaemonStatus = true;

  /* ─── v3.1 props (aditivas) ─────────────────────────────────── */
  /** Si false, oculta el aside completo. TODO: diseño pendiente. */
  export let showSidebar = true;
  /** Ancho del sidebar. Default 220px (canónico Beta 8.1). */
  export let sidebarWidth = '220px';

  const wc = getContext('windowControls');

  $: hostname = typeof window !== 'undefined' ? (window.location.hostname || 'nimos') : 'nimos';
  $: userName = $user?.username || 'user';

  function handleItem(itemId) {
    active = itemId;
  }
</script>

<div class="app-shell" style="--sidebar-width: {sidebarWidth};">
  <!-- ═══════════ TITLEBAR · cubo + path + LEDs ═══════════ -->
  <div class="titlebar">

    <!-- Izquierda · cubo 45° + path -->
    <div class="tb-left">
      <span class="ink-cube" aria-hidden="true"></span>
      <span class="tb-path">
        <span class="scheme">nimos://</span><span class="host">{hostname}</span>
        {#each pathSegments as seg, i}
          <span class="sep">/</span>
          {#if i === pathSegments.length - 1}
            <span class="current">{seg}</span>
          {:else}
            <span class="seg">{seg}</span>
          {/if}
        {/each}
      </span>
    </div>

    <!-- Derecha · acciones + LEDs C2 -->
    <div class="tb-right">
      <div class="tb-actions">
        <slot name="titlebar-actions" />
      </div>
      {#if wc}
        <div class="wc-bar">
          <button
            class="wc-led min"
            on:click={wc.minimize}
            title="Minimizar"
            aria-label="Minimizar"
          ></button>
          <button
            class="wc-led max"
            on:click={wc.maximize}
            title="Maximizar"
            aria-label="Maximizar"
          ></button>
          <button
            class="wc-led close"
            on:click={wc.close}
            title="Cerrar"
            aria-label="Cerrar"
          ></button>
        </div>
      {/if}
    </div>
  </div>

  <!-- ═══════════ APP BODY · sidebar + main ═══════════ -->
  <div class="app-body" class:no-sidebar={!showSidebar}>

    {#if showSidebar}
      <!-- Sidebar -->
      <aside class="sidebar">
        {#if $$slots['sidebar-header']}
          <slot name="sidebar-header" />
        {:else}
          <div class="sb-header">
            <div class="sb-header-icon">{headerIcon}</div>
            <div class="sb-title">{title}</div>
          </div>
        {/if}

        <div class="sb-scroll">
          {#if $$slots['sidebar-content']}
            <slot name="sidebar-content" />
          {:else}
            {#each sections as section}
              <div class="sb-section">
                <span>{section.label}</span>
              </div>
              {#each section.items as item}
                <div
                  class="sb-item"
                  class:active={active === item.id}
                  on:click={() => handleItem(item.id)}
                  on:keydown={(e) => e.key === 'Enter' && handleItem(item.id)}
                  role="button"
                  tabindex="0"
                >
                  <span class="sb-prefix">{active === item.id ? '▸' : '\u00A0'}</span>
                  <span class="sb-label">{item.label}</span>
                  {#if item.badge !== undefined && item.badge !== null && item.badge !== 0}
                    <Badge size="sm" variant={item.badgeVariant || 'default'}>{item.badge}</Badge>
                  {/if}
                  {#if item.keyHint}
                    <KeyBind key={item.keyHint} active={active === item.id} />
                  {/if}
                </div>
              {/each}
            {/each}
          {/if}
        </div>

        {#if showDaemonStatus}
          <div class="sb-footer">
            <div class="sb-footer-row">
              <LED size={7} />
              <span class="k">daemon</span>
              <span class="v">running</span>
            </div>
            <div class="sb-footer-row">
              <span class="k">user</span>
              <span class="v">{userName}</span>
            </div>
          </div>
        {/if}
      </aside>
    {/if}

    <!-- Main -->
    <div class="main">
      {#if $$slots['page-header']}
        <div class="page-header">
          <slot name="page-header" />
        </div>
      {/if}
      <slot name="toolbar" />
      <div class="content">
        <slot />
      </div>
      <slot name="footer-raw" />
      {#if $$slots.footer}
        <div class="inner-footer">
          <div class="left">
            <slot name="footer" />
          </div>
          <div class="right">
            <slot name="footer-right" />
          </div>
        </div>
      {/if}
    </div>

  </div>
</div>

<style>
  .app-shell {
    width: 100%;
    height: 100%;
    background: transparent;
    font-family: var(--font-sans);
    color: var(--ink, #f2f2f5);
    display: flex;
    flex-direction: column;
    min-width: 780px;
    overflow: hidden;
  }

  /* ═══════════════════════════════════════════════════════════
     TITLEBAR · estética NimOS Beta 8.1
     ═══════════════════════════════════════════════════════════ */
  .titlebar {
    height: var(--titlebar-height, 36px);
    background: var(--panel-elev, #1c1c1c);
    border-bottom: 1px solid var(--line, rgba(255, 255, 255, 0.08));
    display: flex;
    align-items: center;
    font-family: var(--font-mono, 'JetBrains Mono', monospace);
    font-size: 11px;
    user-select: none;
    flex-shrink: 0;
  }
  .tb-left {
    padding: 0 16px;
    display: flex;
    align-items: center;
    gap: 12px;
    flex: 1;
    min-width: 0;
  }

  /* ─── Cubo 45° blanco · firma NimOS micro ─── */
  .ink-cube {
    display: inline-block;
    width: 10px;
    height: 10px;
    background: #ffffff;
    transform: rotate(45deg);
    flex-shrink: 0;
    filter:
      drop-shadow(0 0 5px var(--accent-glow-soft, rgba(220, 255, 235, 0.6)))
      drop-shadow(0 0 2px var(--accent-glow-hard, rgba(255, 255, 255, 0.7)));
    transition: filter 0.2s, opacity 0.2s;
  }

  /* ─── Path · nimos://host/seg/seg/current ─── */
  .tb-path {
    color: var(--ink-dim, #c8c8cf);
    font-family: var(--font-mono, monospace);
    font-size: 11px;
    letter-spacing: 0.3px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-feature-settings: "tnum";
  }
  .tb-path .scheme  { color: var(--ink-trace, #44444a); }
  .tb-path .host    {
    color: var(--ink, #f2f2f5);
    font-weight: 700;
    text-shadow: 0 0 6px var(--accent-glow-soft, rgba(220, 255, 235, 0.6));
  }
  .tb-path .sep     { color: var(--ink-trace, #44444a); margin: 0 1px; }
  .tb-path .seg     { color: var(--ink-mute, #9a9aa3); }
  .tb-path .current { color: var(--ink, #f2f2f5); font-weight: 500; }

  /* ─── Acciones derecha · slot opcional ─── */
  .tb-right {
    display: flex;
    align-items: center;
  }
  .tb-actions {
    display: flex;
    gap: 6px;
    padding: 0 10px;
  }

  /* ═══════════════════════════════════════════════════════════
     LEDs C2 · GLOW DRAMÁTICO · min/max/close (orden NimOS)
     ═══════════════════════════════════════════════════════════ */
  .wc-bar {
    display: flex;
    gap: 10px;
    align-items: center;
    padding: 0 16px;
  }
  .wc-led {
    width: 10px;
    height: 10px;
    background: var(--led-color);
    border: none;
    cursor: pointer;
    padding: 0;
    transition: filter 0.12s, transform 0.12s;
    position: relative;
  }
  .wc-led.min {
    --led-color: var(--warn, #fbbf24);
    box-shadow:
      0 0 8px var(--warn-glow, rgba(251, 191, 36, 0.5)),
      0 0 16px rgba(251, 191, 36, 0.25);
  }
  .wc-led.max {
    --led-color: var(--signal, #00ff9f);
    box-shadow:
      0 0 8px var(--signal-glow, rgba(0, 255, 159, 0.5)),
      0 0 16px rgba(0, 255, 159, 0.25);
  }
  .wc-led.close {
    --led-color: var(--crit, #f87171);
    box-shadow:
      0 0 8px var(--crit-glow, rgba(248, 113, 113, 0.5)),
      0 0 16px rgba(248, 113, 113, 0.25);
  }
  .wc-led:hover {
    filter: brightness(1.4);
    transform: scale(1.1);
  }
  .wc-led:active {
    transform: scale(0.92);
  }

  /* ═══════════════════════════════════════════════════════════
     ESTADO INACTIVO · cubo y LEDs apagados desde el padre
     ───────────────────────────────────────────────────────────
     WindowFrame aplica `.inactive` al .window padre.
     Aquí cazamos esa clase con :global() para atenuar.
     ═══════════════════════════════════════════════════════════ */
  :global(.window.inactive) .ink-cube {
    filter: drop-shadow(0 0 2px rgba(220, 255, 235, 0.18));
    opacity: 0.45;
  }
  :global(.window.inactive) .tb-path .host {
    text-shadow: none;
    color: var(--ink-dim, #c8c8cf);
  }
  :global(.window.inactive) .wc-led.min {
    box-shadow: 0 0 2px rgba(251, 191, 36, 0.2);
    opacity: 0.4;
  }
  :global(.window.inactive) .wc-led.max {
    box-shadow: 0 0 2px rgba(0, 255, 159, 0.2);
    opacity: 0.4;
  }
  :global(.window.inactive) .wc-led.close {
    box-shadow: 0 0 2px rgba(248, 113, 113, 0.2);
    opacity: 0.4;
  }

  /* ═══════════════════════════════════════════════════════════
     APP BODY · sidebar + main
     ───────────────────────────────────────────────────────────
     v3.1: --sidebar-width viene del style attr del .app-shell
     (sobrescribible vía prop sidebarWidth). Default fijado en
     220px en el script. Si app.css declara --sidebar-width
     a otro valor, gana el style attr local.
     ═══════════════════════════════════════════════════════════ */
  .app-body {
    flex: 1;
    display: grid;
    grid-template-columns: var(--sidebar-width) 1fr;
    overflow: hidden;
    min-height: 0;
  }
  /* v3.1: modo sin sidebar — TODO diseño pendiente */
  .app-body.no-sidebar {
    grid-template-columns: 1fr;
  }

  /* ─── Sidebar ─── */
  .sidebar {
    background: var(--side-bg, #1c1c1c);
    border-right: 1px solid var(--side-border, rgba(255, 255, 255, 0.08));
    display: flex;
    flex-direction: column;
    font-family: var(--font-sans);
    font-size: 13px;
    overflow: hidden;
  }
  .sb-header {
    padding: 16px 16px 14px;
    display: flex;
    align-items: center;
    gap: 10px;
    flex-shrink: 0;
  }
  .sb-header-icon {
    width: 26px;
    height: 26px;
    background: var(--signal-dim, rgba(0, 255, 159, 0.15));
    color: var(--signal, #00ff9f);
    display: flex;
    align-items: center;
    justify-content: center;
    font-weight: 600;
    font-size: 13px;
  }
  .sb-title {
    color: var(--ink, #f2f2f5);
    font-weight: 600;
    letter-spacing: 0.5px;
    text-transform: uppercase;
    font-size: 11px;
  }

  .sb-scroll {
    flex: 1;
    overflow-y: auto;
    padding: 2px 10px 10px;
  }

  .sb-section {
    padding: 14px 6px 6px;
    font-size: 10px;
    color: var(--ink-trace, #44444a);
    text-transform: uppercase;
    letter-spacing: 1.5px;
    font-weight: 600;
  }

  .sb-item {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    margin: 1px 0;
    color: var(--ink-dim, #c8c8cf);
    cursor: pointer;
    transition: background 0.12s, color 0.12s;
    font-size: 13px;
    font-weight: 400;
  }
  .sb-item:hover {
    background: var(--side-hover, rgba(255, 255, 255, 0.04));
    color: var(--ink, #f2f2f5);
  }
  .sb-item.active {
    background: var(--side-active-bg, rgba(122, 158, 177, 0.10));
    color: var(--side-active-fg, #7a9eb1);
  }
  .sb-item {
    position: relative;
  }
  .sb-prefix {
    display: none;
  }
  .sb-label {
    flex: 1;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  /* Sidebar footer · daemon status */
  .sb-footer {
    padding: 12px 16px;
    border-top: 1px solid var(--side-border, rgba(255, 255, 255, 0.08));
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 11px;
    color: var(--ink-mute, #9a9aa3);
    flex-shrink: 0;
    background: transparent;
    font-family: var(--font-mono, monospace);
  }
  .sb-footer-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .sb-footer .k {
    color: var(--ink-trace, #44444a);
    letter-spacing: 1px;
    text-transform: uppercase;
    font-size: 9px;
  }
  .sb-footer .v {
    color: var(--ink-dim, #c8c8cf);
    margin-left: auto;
    font-weight: 500;
  }

  /* ═══════════════════════════════════════════════════════════
     MAIN · área de contenido
     ═══════════════════════════════════════════════════════════ */
  .main {
    display: flex;
    flex-direction: column;
    overflow: hidden;
    background: var(--main-bg, #161616);
    min-width: 0;
  }
  .content {
    flex: 1;
    overflow: auto;
    min-height: 0;
  }

  /* Page header opcional · título y descripción debajo del titlebar */
  .page-header {
    padding: 14px 22px;
    background: transparent;
    font-family: var(--font-sans);
    font-size: 14px;
    color: var(--ink, #f2f2f5);
    letter-spacing: -0.1px;
    flex-shrink: 0;
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 44px;
    border-bottom: 1px solid var(--line, rgba(255, 255, 255, 0.08));
  }
  .page-header :global(b),
  .page-header :global(strong) {
    color: var(--ink, #f2f2f5);
    font-weight: 600;
  }
  .page-header :global(.ph-desc),
  .page-header :global(.ph-path) {
    color: var(--ink-mute, #9a9aa3);
    font-size: 12px;
    font-weight: 400;
    letter-spacing: 0;
  }
  .page-header :global(.ph-right) {
    margin-left: auto;
    display: flex;
    align-items: center;
    gap: 8px;
  }

  /* Inner footer (status bar interno de la app) */
  .inner-footer {
    height: 30px;
    background: var(--canvas-soft, #111111);
    border-top: 1px solid var(--line, rgba(255, 255, 255, 0.08));
    display: flex;
    align-items: center;
    padding: 0 18px;
    font-family: var(--font-mono, monospace);
    font-size: 10px;
    color: var(--ink-mute, #9a9aa3);
    letter-spacing: 0.5px;
    flex-shrink: 0;
  }
  .inner-footer .left, .inner-footer .right {
    display: flex;
    align-items: center;
    gap: 14px;
  }
  .inner-footer .left  { flex: 1; }
  .inner-footer .right { margin-left: auto; }
</style>
