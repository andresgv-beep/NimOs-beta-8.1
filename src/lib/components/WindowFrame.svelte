<script>
  /**
   * WindowFrame · Marco de ventana NimOS Beta 8.1
   * ───────────────────────────────────────────────
   * Envuelve cada app abierta, maneja drag, resize, maximize.
   * El chrome de titlebar lo pone AppShell por dentro — WindowFrame
   * solo es el contenedor flotante con bordes técnicos NimOS.
   *
   * Estética técnica retro NimOS:
   *   - Sin glass · sin border-radius
   *   - Bisel inferior-derecho 22px (firma macro)
   *   - Borde duro técnico + sombra hard 5px abajo-derecha
   *   - Halo lechoso del boot suave alrededor
   *   - Estado activo · ventana al 100%, brillos fuertes
   *   - Estado inactivo · atenuación sutil (opacity 0.92 + LEDs/cubo apagados)
   *
   * Lógica preservada (sin cambios):
   *   - Drag desde drag-zone invisible en titlebar
   *   - Resize desde handle en bisel inferior-derecho
   *   - Maximize con cálculo de viewport y ui-zoom
   *   - Focus management con z-index
   *   - Carga lazy de apps con dynamic import
   *   - Context windowControls para AppShell
   */
  import { onMount, tick, setContext } from 'svelte';
  import {
    closeWindow, focusWindow, minimizeWindow, maximizeWindow,
    updateWindowPos, getWindowPos, windowList,
  } from '$lib/stores/windows.js';
  import { APP_META } from '$lib/apps.js';

  export let win;

  $: meta = APP_META[win.appId] || { name: win.appId, fallback: '📦' };

  // ¿Esta ventana es la del foco? (zIndex más alto entre las no minimizadas)
  $: isFocused = !win.minimized && win.zIndex === Math.max(
    ...$windowList.filter(w => !w.minimized).map(w => w.zIndex),
    0
  );

  // Expose window controls vía context a AppShell
  setContext('windowControls', {
    close:    () => closeWindow(win.id),
    minimize: () => minimizeWindow(win.id),
    maximize: () => doMaximize(),
    getWin:   () => win,
  });

  let x = 0, y = 0, w = 800, h = 520;

  onMount(async () => {
    await tick();
    const p = getWindowPos(win.id);
    x = p.x; y = p.y; w = p.width; h = p.height;
  });

  // ─── Drag ───
  let dragging = false;
  let dragOffset = { x: 0, y: 0 };

  function getZoom() {
    return parseFloat(document.documentElement.style.zoom) || 1;
  }

  function onTitleMouseDown(e) {
    if (e.target.closest('.wc-led')) return;
    if (e.target.closest('.tb-actions button')) return;
    if (win.maximized) return;
    focusWindow(win.id);
    dragging = true;
    const z = getZoom();
    dragOffset = { x: e.clientX / z - x, y: e.clientY / z - y };
    window.addEventListener('mousemove', onDrag);
    window.addEventListener('mouseup', onDragEnd);
  }

  function onDrag(e) {
    if (!dragging) return;
    const z = getZoom();
    x = e.clientX / z - dragOffset.x;
    y = Math.max(0, e.clientY / z - dragOffset.y);
    updateWindowPos(win.id, { x, y });
  }

  function onDragEnd() {
    dragging = false;
    window.removeEventListener('mousemove', onDrag);
    window.removeEventListener('mouseup', onDragEnd);
  }

  // ─── Resize ───
  let resizing = false;
  let resizeStart = { mx: 0, my: 0, w: 0, h: 0 };

  function onResizeMouseDown(e) {
    if (win.maximized) return;
    e.stopPropagation();
    resizing = true;
    const z = getZoom();
    resizeStart = { mx: e.clientX / z, my: e.clientY / z, w, h };
    window.addEventListener('mousemove', onResize);
    window.addEventListener('mouseup', onResizeEnd);
  }

  function onResize(e) {
    if (!resizing) return;
    const z = getZoom();
    w = Math.max(400, resizeStart.w + (e.clientX / z - resizeStart.mx));
    h = Math.max(300, resizeStart.h + (e.clientY / z - resizeStart.my));
    updateWindowPos(win.id, { width: w, height: h });
  }

  function onResizeEnd() {
    resizing = false;
    window.removeEventListener('mousemove', onResize);
    window.removeEventListener('mouseup', onResizeEnd);
  }

  // ─── Maximize ───
  function doMaximize() {
    maximizeWindow(win.id);
    tick().then(() => {
      const p = getWindowPos(win.id);
      x = p.x; y = p.y; w = p.width; h = p.height;
    });
  }
</script>

<div
  class="window"
  class:maximized={win.maximized}
  class:dragging
  class:inactive={!isFocused}
  style="z-index:{win.zIndex}; left:{x}px; top:{y}px; width:{w}px; height:{h}px;"
  on:mousedown={() => focusWindow(win.id)}
  role="application"
>
  <!-- Drag zone invisible en la titlebar -->
  <div
    class="drag-zone"
    on:mousedown={onTitleMouseDown}
    role="presentation"
  ></div>

  <!-- App content — el .content ocupa toda la ventana, incluyendo titlebar -->
  <div class="content">
    {#if win.isWebApp && win.webAppPort}
      {#await import('$lib/apps/WebApp.svelte') then module}
        <svelte:component
          this={module.default}
          appId={win.appId}
          port={win.webAppPort}
          name={win.webAppName}
        />
      {/await}
    {:else if win.appId === 'files'}
      {#await import('$lib/apps/FileManager.svelte') then module}
        <svelte:component this={module.default} />
      {/await}
    {:else if win.appId === 'nimsettings'}
      {#await import('$lib/apps/Settings.svelte') then module}
        <svelte:component this={module.default} />
      {/await}
    {:else if win.appId === 'storage'}
      {#await import('$lib/apps/StorageApp.svelte') then module}
        <svelte:component this={module.default} />
      {/await}
    {:else if win.appId === 'network'}
      {#await import('$lib/apps/NetworkApp.svelte') then module}
        <svelte:component this={module.default} />
      {/await}
    {:else if win.appId === 'nimtorrent'}
      {#await import('$lib/apps/NimTorrent.svelte') then module}
        <svelte:component this={module.default} />
      {/await}
    {:else if win.appId === 'appstore'}
      {#await import('$lib/apps/AppStore.svelte') then module}
        <svelte:component this={module.default} />
      {/await}
    {:else if win.appId === 'nimbackup'}
      {#await import('$lib/apps/NimBackup.svelte') then module}
        <svelte:component this={module.default} />
      {/await}
    {:else if win.appId === 'notes'}
      {#await import('$lib/apps/Notes.svelte') then module}
        <svelte:component this={module.default} />
      {/await}
    {:else if win.appId === 'nimhealth'}
      {#await import('$lib/apps/NimHealth.svelte') then module}
        <svelte:component this={module.default} />
      {/await}
    {:else if win.appId === 'nimshield'}
      {#await import('$lib/apps/NimShield.svelte') then module}
        <svelte:component this={module.default} />
      {/await}
    {:else if win.appId === 'terminal'}
      {#await import('$lib/apps/Terminal.svelte') then module}
        <svelte:component this={module.default} />
      {/await}
    {:else}
      <div class="placeholder">
        <span class="ph-ic">{meta.fallback}</span>
        <p>{meta.name}</p>
        <small>Coming soon</small>
      </div>
    {/if}
  </div>

  {#if !win.maximized}
    <div class="resize-handle" on:mousedown={onResizeMouseDown} role="presentation"></div>
  {/if}
</div>

<style>
  /* ═══════════════════════════════════════════════════════════
     WINDOW FRAME · estética técnica retro NimOS Beta 8.1
     ═══════════════════════════════════════════════════════════
     · Bisel inferior-derecho 22px (firma macro)
     · Borde duro técnico · sombra hard 5px + glow lechoso
     · Sin backdrop-filter · sin border-radius
     · Estados activa/inactiva con atenuación sutil
     ═══════════════════════════════════════════════════════════ */
  .window {
    position: fixed;
    display: flex;
    flex-direction: column;
    background: var(--window-bg, #161616);
    border: 1px solid var(--window-border, rgba(255, 255, 255, 0.14));
    box-shadow: var(--window-shadow,
      5px 5px 0 rgba(0, 0, 0, 0.6),
      0 0 60px rgba(220, 255, 235, 0.04)
    );
    /* Bisel firma NimOS · 22px inferior-derecho */
    clip-path: polygon(
      0 0,
      100% 0,
      100% calc(100% - var(--bev-window, 22px)),
      calc(100% - var(--bev-window, 22px)) 100%,
      0 100%
    );
    transition: opacity 0.15s ease;
    animation: win-in 0.32s cubic-bezier(0.16, 1, 0.3, 1) both;
    will-change: transform;
  }

  .window.dragging { user-select: none; }

  /* Estado inactivo · ventana atenuada
     Los LEDs y el cubo se apagan vía CSS en AppShell con :host-context o
     :global. Aquí solo bajamos un punto la opacidad general. */
  .window.inactive {
    opacity: 0.92;
  }

  /* Ventana maximizada · sin bisel, sin borde, sin sombra */
  .window.maximized {
    clip-path: none !important;
    border: none !important;
    box-shadow: none !important;
    left: 0 !important;
    top: 0 !important;
    width: calc(100vw / var(--ui-zoom, 1)) !important;
    height: calc((100vh - var(--taskbar-height, 52px)) / var(--ui-zoom, 1)) !important;
  }

  .drag-zone {
    position: absolute;
    top: 0;
    left: 0;
    right: 140px; /* deja espacio para los LEDs a la derecha */
    height: 36px;
    z-index: 5;
    cursor: default;
    pointer-events: auto;
  }

  .content {
    flex: 1;
    overflow: hidden;
    min-height: 0;
    background: transparent;
  }

  /* Placeholder · cuando se abre un app sin módulo todavía */
  .placeholder {
    width: 100%;
    height: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 10px;
    color: var(--ink-mute, #9a9aa3);
    background: transparent;
    font-family: var(--font-sans);
  }
  .ph-ic {
    font-size: 48px;
    opacity: 0.85;
    filter: drop-shadow(0 0 6px var(--accent-glow-soft, rgba(220, 255, 235, 0.6)));
  }
  .placeholder p {
    font-size: 15px;
    font-weight: 500;
    color: var(--ink, #f2f2f5);
    letter-spacing: -0.2px;
  }
  .placeholder small {
    font-size: 10px;
    color: var(--ink-mute, #9a9aa3);
    letter-spacing: 1.5px;
    text-transform: uppercase;
  }

  /* ═══════════════════════════════════════════════════════════
     RESIZE HANDLE · 3 diagonales sutiles en el bisel
     ═══════════════════════════════════════════════════════════ */
  .resize-handle {
    position: absolute;
    bottom: 0;
    right: 0;
    width: 22px;
    height: 22px;
    cursor: nwse-resize;
    z-index: 10;
  }
  /* 3 líneas diagonales paralelas al bisel · escalera técnica */
  .resize-handle::before {
    content: '';
    position: absolute;
    right: 4px;
    bottom: 4px;
    width: 10px;
    height: 1px;
    background: var(--line-bright, rgba(255, 255, 255, 0.14));
    transform: rotate(-45deg);
    transform-origin: right center;
  }
  .resize-handle::after {
    content: '';
    position: absolute;
    right: 7px;
    bottom: 7px;
    width: 6px;
    height: 1px;
    background: var(--line-bright, rgba(255, 255, 255, 0.14));
    transform: rotate(-45deg);
    transform-origin: right center;
  }
  .resize-handle:hover::before,
  .resize-handle:hover::after {
    background: var(--ink, #f2f2f5);
  }

  @keyframes win-in {
    from { opacity: 0; transform: scale(0.98) translateY(6px); }
    to   { opacity: 1; transform: scale(1) translateY(0); }
  }
</style>
