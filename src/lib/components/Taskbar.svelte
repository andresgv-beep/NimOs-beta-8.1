<script>
  /**
   * Taskbar · Barra de tareas NimOS Beta 8.1
   * ──────────────────────────────────────────
   * - Zona izquierda: logo NimOS · botón MENÚ · apps ancladas · apps abiertas
   * - Zona centro:    vacío (deja respirar el escritorio)
   * - Zona derecha:   sysled CPU/RAM/NET · transferencias · notificaciones · reloj · power
   *
   * Estética técnica retro NimOS:
   *   - Sin glass · sin border-radius · gradient sutil + border-top duro
   *   - LED barrita 16×2px verde luminoso bajo apps abiertas
   *   - Botón MENÚ con chaflán inferior-derecho 8px (firma NimOS)
   *   - Tooltips con chaflán técnico
   *   - Mini sysled CPU/RAM/NET segmentado estilo LED retro
   *
   * Mantenido de Beta 8:
   *   - Logo NimOS pixelado (3 cubos blancos)
   *   - Toda la lógica de stores (windowList, pinnedApps, notifications, uploadTasks)
   *   - Anclar/desanclar via contextmenu
   *   - Restore/minimize/focus de ventanas
   */
  import { onMount, onDestroy } from 'svelte';
  import { pinnedApps, setPref, prefs } from '$lib/stores/theme.js';
  import {
    windowList, openWindow, focusWindow,
    restoreWindow, minimizeWindow, closeWindow
  } from '$lib/stores/windows.js';
  import { logout } from '$lib/stores/auth.js';
  import { APP_META } from '$lib/apps.js';
  import { unreadCount } from '$lib/stores/notifications.js';
  import { activeTasks } from '$lib/stores/uploadTasks.js';
  import Launcher from './Launcher.svelte';
  import NotificationPanel from './NotificationPanel.svelte';
  import TransferPanel from './TransferPanel.svelte';
  import AppIcon from '$lib/ui/AppIcon.svelte';

  let showLauncher = false;
  let showNotif = false;
  let showTransfers = false;

  // ─── Clock ───
  let now = new Date();
  let clockInterval;

  // Canvas refs para el LCD
  let lcdHoursCanvas;
  let lcdMinutesCanvas;

  // ─── LCD digit segments matrix (7-segment classic) ───
  const LCD_DIGITS = [
    [1,1,1,1,1,1,0], // 0
    [0,1,1,0,0,0,0], // 1
    [1,1,0,1,1,0,1], // 2
    [1,1,1,1,0,0,1], // 3
    [0,1,1,0,0,1,1], // 4
    [1,0,1,1,0,1,1], // 5
    [1,0,1,1,1,1,1], // 6
    [1,1,1,0,0,0,0], // 7
    [1,1,1,1,1,1,1], // 8
    [1,1,1,1,0,1,1], // 9
  ];

  /**
   * Dibuja una pareja de dígitos en un canvas (HH o MM)
   * Mismo patrón que el widget del dashboard pero blanco plano sin gradiente
   */
  function drawLcdPair(canvas, val) {
    if (!canvas) return;
    const dpr = window.devicePixelRatio || 1;
    // Tamaño compacto para taskbar (vs widget que es más grande)
    const DW = 14, DH = 24, S = 2, GAP_D = 4, PAD = 2;
    const cw = PAD * 2 + DW * 2 + GAP_D;
    const ch = PAD * 2 + DH;
    canvas.width = cw * dpr;
    canvas.height = ch * dpr;
    canvas.style.width = cw + 'px';
    canvas.style.height = ch + 'px';
    const ctx = canvas.getContext('2d');
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, cw, ch);

    // Color blanco plano · sin gradiente · sin drop-shadow
    const ON  = 'rgba(255, 255, 255, 0.95)';
    const OFF = 'rgba(255, 255, 255, 0.05)';

    function seg(x, y, isOn, horiz) {
      ctx.fillStyle = isOn ? ON : OFF;
      const r = 1;
      const rw = horiz ? DW - S * 2 : S;
      const rh = horiz ? S : (DH - S * 3) / 2;
      ctx.beginPath();
      ctx.moveTo(x + r, y);
      ctx.lineTo(x + rw - r, y);
      ctx.quadraticCurveTo(x + rw, y, x + rw, y + r);
      ctx.lineTo(x + rw, y + rh - r);
      ctx.quadraticCurveTo(x + rw, y + rh, x + rw - r, y + rh);
      ctx.lineTo(x + r, y + rh);
      ctx.quadraticCurveTo(x, y + rh, x, y + rh - r);
      ctx.lineTo(x, y + r);
      ctx.quadraticCurveTo(x, y, x + r, y);
      ctx.closePath();
      ctx.fill();
    }

    function digit(n, ox, oy) {
      const d = LCD_DIGITS[n] || LCD_DIGITS[0];
      const hh = (DH - S * 3) / 2;
      seg(ox + S,    oy,            d[0], true);  // top
      seg(ox + DW-S, oy + S,        d[1], false); // top-right
      seg(ox + DW-S, oy + S*2 + hh, d[2], false); // bot-right
      seg(ox + S,    oy + DH - S,   d[3], true);  // bottom
      seg(ox,        oy + S*2 + hh, d[4], false); // bot-left
      seg(ox,        oy + S,        d[5], false); // top-left
      seg(ox + S,    oy + S + hh,   d[6], true);  // middle
    }

    digit(Math.floor(val / 10), PAD, PAD);
    digit(val % 10, PAD + DW + GAP_D, PAD);
  }

  function updateClock() {
    now = new Date();
    drawLcdPair(lcdHoursCanvas, now.getHours());
    drawLcdPair(lcdMinutesCanvas, now.getMinutes());
  }

  onMount(() => {
    updateClock();
    clockInterval = setInterval(updateClock, 1000);
    return () => clearInterval(clockInterval);
  });
  onDestroy(() => {
    if (clockInterval) clearInterval(clockInterval);
  });

  $: dd = String(now.getDate()).padStart(2, '0');
  $: MON = now.toLocaleDateString('es-ES', { month: 'short' }).toUpperCase().replace('.', '');
  $: DOW = now.toLocaleDateString('es-ES', { weekday: 'short' }).toUpperCase().replace('.', '');

  // ─── Context menu (pin/unpin) ───
  let ctxMenu = null;

  function openCtxMenu(e, appId, win = null) {
    e.preventDefault();
    e.stopPropagation();
    ctxMenu = {
      appId,
      win,
      x: Math.min(e.clientX, window.innerWidth - 220),
      bottom: window.innerHeight - e.clientY + 8,
    };
  }
  function closeCtxMenu() { ctxMenu = null; }
  function isPinned(appId) { return $pinnedApps.includes(appId); }
  function togglePin(appId) {
    if (isPinned(appId)) setPref('pinnedApps', $pinnedApps.filter(id => id !== appId));
    else setPref('pinnedApps', [...$pinnedApps, appId]);
    closeCtxMenu();
  }

  // ─── App launch ───
  function handleAppClick(appId) {
    const meta = APP_META[appId];
    const existing = $windowList.find(w => w.appId === appId);
    if (existing) {
      if (existing.minimized) restoreWindow(existing.id);
      else focusWindow(existing.id);
    } else {
      openWindow(appId, { width: meta?.width || 800, height: meta?.height || 520 });
    }
  }
  function toggleMinimize(win) {
    if (win.minimized) restoreWindow(win.id);
    else minimizeWindow(win.id);
  }

  function isIconUrl(icon) { return icon && (icon.startsWith('/') || icon.startsWith('http')); }

  // ─── Apps open not pinned ───
  $: openUnpinned = $windowList.filter(w => !$pinnedApps.includes(w.appId));

  // ─── Transfers activity ───
  $: transferCount = $activeTasks.length;

  // ─── Mini sysled · TODO: conectar al daemon ───
  let cpuLoad = 28;
  let ramLoad = 52;
  let netLoad = 14;
</script>

<Launcher bind:visible={showLauncher} />
<NotificationPanel bind:visible={showNotif} />
<TransferPanel bind:visible={showTransfers} />

<!-- Context menu click outside -->
{#if ctxMenu}
  <div class="ctx-overlay" on:click={closeCtxMenu} role="presentation"></div>
  <div class="ctx-menu" style="left:{ctxMenu.x}px; bottom:{ctxMenu.bottom}px">
    <div class="ctx-item" on:click={() => togglePin(ctxMenu.appId)} role="button" tabindex="0">
      <span class="ctx-ic">◆</span>
      <span>{isPinned(ctxMenu.appId) ? 'Desanclar del taskbar' : 'Anclar al taskbar'}</span>
    </div>
    {#if ctxMenu.win}
      <div class="ctx-sep"></div>
      <div class="ctx-item" on:click={() => { closeWindow(ctxMenu.win.id); closeCtxMenu(); }} role="button" tabindex="0">
        <span class="ctx-ic">×</span>
        <span>Cerrar ventana</span>
      </div>
    {/if}
  </div>
{/if}

<div class="taskbar">

  <!-- ═══════════════ IZQUIERDA · LAUNCHER ═══════════════ -->
  <div class="tb-left">

    <!-- Logo NimOS · 3 cubos pixel art · ÚNICO punto de entrada al launcher -->
    <button
      class="tb-logo-btn"
      on:click={() => showLauncher = !showLauncher}
      class:active={showLauncher}
      title="Apps · NimOS"
    >
      <svg class="nimos-logo" width="28" height="28" viewBox="-15 0 200 185" fill="none" xmlns="http://www.w3.org/2000/svg">
        <rect x="5" y="45" width="80" height="80" rx="16" transform="rotate(-30 45 85)" fill="#ffffff"/>
        <rect x="108" y="12" width="60" height="60" rx="10" fill="#ffffff"/>
        <rect x="108" y="98" width="60" height="60" rx="10" fill="#ffffff"/>
      </svg>
    </button>

    <div class="tb-sep"></div>

    <!-- Apps ancladas -->
    <div class="app-row">
      {#each $pinnedApps as appId}
        {@const meta = APP_META[appId]}
        {#if meta}
          {@const existing = $windowList.find(w => w.appId === appId)}
          {@const isOpen = !!existing}
          {@const isMin  = existing?.minimized}
          {@const isFocused = isOpen && !isMin && existing?.zIndex === Math.max(...$windowList.map(w => w.zIndex))}
          <button
            class="tb-app"
            class:open={isOpen}
            class:minimized={isMin}
            class:focused={isFocused}
            on:click={() => handleAppClick(appId)}
            on:contextmenu={(e) => openCtxMenu(e, appId, existing)}
          >
            {#if isIconUrl(meta.icon)}
              <AppIcon
                src={meta.icon}
                alt={meta.name}
                size="sm"
                fallback={meta.fallback}
                active={isOpen}
              />
            {:else}
              <span class="tb-emoji">{meta.fallback || meta.icon || '📦'}</span>
            {/if}
            <span class="tb-tooltip">{meta.name}</span>
          </button>
        {/if}
      {/each}
    </div>

    <!-- Apps abiertas no ancladas -->
    {#if openUnpinned.length > 0}
      <div class="tb-sep"></div>
      <div class="app-row">
        {#each openUnpinned as win}
          {@const meta = APP_META[win.appId]}
          {@const isFocused = !win.minimized && win.zIndex === Math.max(...$windowList.map(w => w.zIndex))}
          <button
            class="tb-app open"
            class:minimized={win.minimized}
            class:focused={isFocused}
            on:click={() => toggleMinimize(win)}
            on:contextmenu={(e) => openCtxMenu(e, win.appId, win)}
          >
            {#if isIconUrl(meta?.icon)}
              <AppIcon
                src={meta.icon}
                alt={meta?.name}
                size="sm"
                fallback={meta?.fallback}
                active={!win.minimized}
              />
            {:else}
              <span class="tb-emoji">{meta?.fallback || '📦'}</span>
            {/if}
            <span class="tb-tooltip">{meta?.name || win.appId}</span>
          </button>
        {/each}
      </div>
    {/if}

  </div>

  <!-- ═══════════════ CENTRO · vacío, respira ═══════════════ -->
  <div class="tb-center"></div>

  <!-- ═══════════════ DERECHA · SYSTRAY ═══════════════ -->
  <div class="tb-right">

    <!-- Mini sysled CPU/RAM/NET · estética LED segmentada retro -->
    <div class="tb-sysled">
      <div class="sysled-item" title="CPU · {cpuLoad}%">
        <span class="sysled-lbl">CPU</span>
        <div class="sysled-bar">
          <div class="sysled-fill" class:warn={cpuLoad > 75} style="width:{cpuLoad}%"></div>
        </div>
      </div>
      <div class="sysled-item" title="RAM · {ramLoad}%">
        <span class="sysled-lbl">RAM</span>
        <div class="sysled-bar">
          <div class="sysled-fill" class:warn={ramLoad > 75} style="width:{ramLoad}%"></div>
        </div>
      </div>
      <div class="sysled-item" title="NET · {netLoad}%">
        <span class="sysled-lbl">NET</span>
        <div class="sysled-bar">
          <div class="sysled-fill" class:warn={netLoad > 75} style="width:{netLoad}%"></div>
        </div>
      </div>
    </div>

    <div class="tb-sep"></div>

    <!-- Transferencias -->
    <button
      class="tb-tray"
      class:active={showTransfers}
      class:has-activity={transferCount > 0}
      on:click={() => { showTransfers = !showTransfers; showNotif = false; }}
      title="Transferencias"
    >
      <span class="tray-ic">⇅</span>
      {#if transferCount > 0}
        <span class="tray-badge active">{transferCount}</span>
      {/if}
    </button>

    <!-- Notificaciones -->
    <button
      class="tb-tray"
      class:active={showNotif}
      class:has-unread={$unreadCount > 0}
      on:click={() => { showNotif = !showNotif; showTransfers = false; }}
      title="Notificaciones"
    >
      <span class="tray-ic">◉</span>
      {#if $unreadCount > 0}
        <span class="tray-badge">{$unreadCount}</span>
      {/if}
    </button>

    <div class="tb-sep"></div>

    <!-- Reloj LCD canvas · mismo patrón que widget del dashboard · blanco plano sin gradiente -->
    <div class="tb-clock" title={now.toLocaleString('es-ES')}>
      <div class="lcd-row">
        <canvas bind:this={lcdHoursCanvas} class="lcd-canvas"></canvas>
        <span class="lcd-colon">
          <span class="dot"></span>
          <span class="dot"></span>
        </span>
        <canvas bind:this={lcdMinutesCanvas} class="lcd-canvas"></canvas>
      </div>
      <span class="clock-date">{DOW} · {dd} {MON}</span>
    </div>

    <!-- Power -->
    <button class="tb-power" on:click={logout} title="Cerrar sesión">
      <span class="power-ic">⏻</span>
    </button>

  </div>

</div>

<style>
  /* ═══════════════════════════════════════════════════════════
     TASKBAR · Beta 8.1 · estética técnica retro NimOS
     ═══════════════════════════════════════════════════════════ */
  .taskbar {
    position: fixed;
    left: 0; right: 0; bottom: 0;
    height: var(--taskbar-height, 44px);
    /* Color sólido plano · sin gradient cromado tipo Lubuntu */
    background: var(--taskbar-bg, #191718);
    border-top: 1px solid var(--taskbar-border-top, rgba(255, 255, 255, 0.06));
    display: flex;
    align-items: stretch;
    z-index: 9000;
    font-family: var(--font-mono, 'JetBrains Mono', monospace);
  }

  .tb-left, .tb-right {
    display: flex;
    align-items: center;
    padding: 0 6px;
    gap: 2px;
  }
  .tb-center { flex: 1; }

  .tb-sep {
    width: 1px;
    align-self: center;
    height: 22px;
    background: var(--border, #1f1f1f);
    margin: 0 6px;
  }

  .app-row {
    display: flex;
    gap: 2px;
  }

  /* ─── Logo NimOS · botón sin marco con drop-shadow lechoso ─── */
  .tb-logo-btn {
    width: 44px;
    height: 36px;
    background: transparent;
    border: none;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    transition: background 0.12s;
    padding: 0;
    position: relative;
  }
  .tb-logo-btn:hover {
    background: rgba(255, 255, 255, 0.04);
  }
  /* Cuando el launcher está abierto · sin marco verde, sin línea, solo el logo brilla más */
  .tb-logo-btn.active {
    background: transparent;
  }
  .nimos-logo {
    /* Reposo · blanco normal, sin gradient ni glow */
    filter: none;
    transition: filter 0.18s ease;
  }
  /* Cuando el launcher está abierto · logo se ilumina con drop-shadow lechoso (firma del boot) */
  .tb-logo-btn.active .nimos-logo {
    filter:
      drop-shadow(0 0 6px rgba(220, 255, 235, 0.6))
      drop-shadow(0 0 2px rgba(255, 255, 255, 0.7));
  }
  /* Hover también ilumina sutilmente como preview del estado activo */
  .tb-logo-btn:hover .nimos-logo {
    filter:
      drop-shadow(0 0 4px rgba(220, 255, 235, 0.4))
      drop-shadow(0 0 1px rgba(255, 255, 255, 0.5));
  }

  /* ─── App icon · sin border-radius · LED bajo cuando está abierta ─── */
  .tb-app {
    position: relative;
    width: 38px;
    height: 36px;
    background: transparent;
    border: none;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    transition: background 0.12s;
    padding: 0;
  }
  .tb-app:hover {
    background: rgba(255, 255, 255, 0.05);
  }
  .tb-app :global(img) {
    width: 22px;
    height: 22px;
    object-fit: contain;
    filter: drop-shadow(0 0 3px rgba(220, 255, 235, 0.28));
    transition: filter 0.12s;
  }
  .tb-app.open :global(img) {
    filter:
      drop-shadow(0 0 6px rgba(220, 255, 235, 0.6))
      drop-shadow(0 0 2px rgba(255, 255, 255, 0.85));
  }
  .tb-emoji {
    font-size: 20px;
    filter: drop-shadow(0 0 3px rgba(220, 255, 235, 0.28));
  }
  .tb-app.open .tb-emoji {
    filter:
      drop-shadow(0 0 6px rgba(220, 255, 235, 0.6))
      drop-shadow(0 0 2px rgba(255, 255, 255, 0.85));
  }

  /* LED barrita bajo apps abiertas · 16×2px verde luminoso */
  .tb-app.open::after {
    content: '';
    position: absolute;
    bottom: 2px;
    left: 50%;
    transform: translateX(-50%);
    width: 16px;
    height: 2px;
    background: var(--accent-color, #00ff9f);
    box-shadow: 0 0 5px var(--accent-color, #00ff9f);
  }
  .tb-app.focused::after {
    width: 22px;
    box-shadow: 0 0 7px var(--accent-color, #00ff9f);
  }
  .tb-app.minimized::after {
    width: 8px;
    opacity: 0.4;
  }

  /* Tooltip arriba del icono · chaflán técnico */
  .tb-tooltip {
    position: absolute;
    bottom: calc(100% + 6px);
    left: 50%;
    transform: translateX(-50%);
    background: var(--bg-elev, #242429);
    border: 1px solid var(--border-bright, #2a2a2a);
    padding: 4px 10px;
    font-family: var(--font-mono, monospace);
    font-size: 9px;
    color: var(--fg, #e8e8e8);
    letter-spacing: 1.5px;
    text-transform: uppercase;
    font-weight: 600;
    white-space: nowrap;
    opacity: 0;
    pointer-events: none;
    transition: opacity 0.12s;
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 4px), calc(100% - 4px) 100%, 0 100%);
  }
  .tb-app:hover .tb-tooltip {
    opacity: 1;
  }

  /* ─── Mini sysled CPU/RAM/NET · estética LED segmentada retro ─── */
  .tb-sysled {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 0 10px;
    height: 100%;
  }
  .sysled-item {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
  }
  .sysled-lbl {
    font-family: var(--font-mono, monospace);
    font-size: 7.5px;
    color: var(--ink-faint, #6a6a72);
    letter-spacing: 1px;
    font-weight: 700;
  }
  .sysled-bar {
    width: 32px;
    height: 5px;
    background: rgba(255, 255, 255, 0.05);
    border: 1px solid var(--line-bright, rgba(255, 255, 255, 0.14));
    position: relative;
    overflow: hidden;
    /* Pattern segmentado · líneas verticales sutiles cada 3px */
    background-image:
      repeating-linear-gradient(90deg,
        transparent 0px, transparent 3px,
        rgba(0, 0, 0, 0.3) 3px, rgba(0, 0, 0, 0.3) 4px);
  }
  .sysled-fill {
    position: absolute;
    top: 0; left: 0; bottom: 0;
    background: var(--ink-dim, #c8c8cf);
    box-shadow: 0 0 4px rgba(220, 255, 235, 0.4);
    transition: width 0.5s ease-out;
  }
  .sysled-fill.warn {
    background: var(--warn, #fbbf24);
    box-shadow: 0 0 4px rgba(251, 191, 36, 0.5);
  }

  /* ─── Tray buttons ─── */
  .tb-tray {
    position: relative;
    width: 36px;
    height: 36px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: none;
    color: var(--fg-dim, #9a9aa3);
    font-size: 14px;
    cursor: pointer;
    transition: background 0.12s, color 0.12s;
  }
  .tb-tray:hover {
    background: rgba(255, 255, 255, 0.05);
    color: var(--fg, #e8e8e8);
  }
  .tb-tray.active {
    background: rgba(0, 255, 159, 0.08);
    color: var(--accent-color, #00ff9f);
    text-shadow: 0 0 5px rgba(0, 255, 159, 0.4);
  }
  .tb-tray.has-activity .tray-ic {
    color: var(--accent-color, #00ff9f);
    text-shadow: 0 0 4px rgba(0, 255, 159, 0.4);
  }
  .tray-ic {
    line-height: 1;
    filter: drop-shadow(0 0 3px rgba(220, 255, 235, 0.28));
  }

  .tray-badge {
    position: absolute;
    top: 4px;
    right: 4px;
    min-width: 14px;
    height: 12px;
    padding: 0 3px;
    background: var(--crit, #d76b6b);
    color: #fff;
    font-family: var(--font-mono, monospace);
    font-size: 8.5px;
    font-weight: 700;
    display: flex;
    align-items: center;
    justify-content: center;
    line-height: 1;
    border: 1px solid rgba(0, 0, 0, 0.6);
  }
  .tray-badge.active {
    background: var(--accent-color, #00ff9f);
    color: #0a0a0a;
    box-shadow: 0 0 4px rgba(0, 255, 159, 0.4);
  }

  /* ─── Reloj LCD · mismo patrón que widget del dashboard ─── */
  .tb-clock {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    padding: 0 14px;
    line-height: 1;
    cursor: pointer;
    gap: 3px;
  }
  .lcd-row {
    display: flex;
    align-items: center;
    gap: 2px;
  }
  .lcd-canvas {
    display: block;
    /* Sin filter, sin shadow, sin gradient · blanco plano puro */
  }
  .lcd-colon {
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 4px;
    height: 24px;
    padding: 0 1px;
  }
  .lcd-colon .dot {
    width: 2px;
    height: 2px;
    background: rgba(255, 255, 255, 0.95);
    display: block;
  }
  .clock-date {
    font-family: var(--font-mono, monospace);
    font-size: 8px;
    color: var(--ink-mute, #5a5a62);
    letter-spacing: 1.5px;
    font-weight: 600;
  }

  /* ─── Power ─── */
  .tb-power {
    width: 40px;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: none;
    border-left: 1px solid var(--border, #1f1f1f);
    color: var(--fg-dim, #9a9aa3);
    font-family: var(--font-mono, monospace);
    font-size: 15px;
    cursor: pointer;
    transition: background 0.12s, color 0.12s;
    margin-left: 4px;
  }
  .tb-power:hover {
    background: rgba(215, 107, 107, 0.1);
    color: var(--crit, #d76b6b);
  }
  .power-ic {
    filter: drop-shadow(0 0 3px rgba(220, 255, 235, 0.28));
  }

  /* ═══════════════════════════════════════════════════════════
     CONTEXT MENU · estética técnica retro
     ═══════════════════════════════════════════════════════════ */
  .ctx-overlay {
    position: fixed;
    inset: 0;
    z-index: 9500;
  }
  .ctx-menu {
    position: fixed;
    min-width: 210px;
    background: linear-gradient(180deg, #161616 0%, #0f0f0f 100%);
    border: 1px solid var(--border-bright, #2a2a2a);
    box-shadow:
      0 -8px 30px rgba(0, 0, 0, 0.6),
      0 0 40px rgba(220, 255, 235, 0.03);
    z-index: 9510;
    font-family: var(--font-mono, monospace);
    font-size: 11px;
    padding: 4px;
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 10px), calc(100% - 10px) 100%, 0 100%);
  }
  .ctx-item {
    padding: 8px 12px;
    color: var(--fg, #e8e8e8);
    display: flex;
    align-items: center;
    gap: 10px;
    cursor: pointer;
    transition: background 0.08s, color 0.08s;
    letter-spacing: 0.5px;
  }
  .ctx-item:hover {
    background: rgba(0, 255, 159, 0.07);
    color: var(--accent-color, #00ff9f);
  }
  .ctx-ic {
    color: var(--fg-mute, #5a5a62);
    width: 14px;
    text-align: center;
    font-size: 11px;
  }
  .ctx-item:hover .ctx-ic { color: var(--accent-color, #00ff9f); }
  .ctx-sep {
    height: 1px;
    background: var(--border, #1f1f1f);
    margin: 4px 2px;
  }
</style>
