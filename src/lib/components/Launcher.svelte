<script>
  /**
   * Launcher · Menú de inicio NimOS Beta 8.1 · Estilo W11
   * ──────────────────────────────────────────────────────
   * Se abre desde el logo NimOS del taskbar.
   *
   * Estética:
   *   - Anclado al taskbar pero separado (bottom: 12px, left: 12px)
   *   - Esquinas redondeadas 14px (no chaflán, no tan agresivo)
   *   - Search arriba con prompt `$` verde
   *   - Apps en grid 6 columnas verticales (icono grande + nombre)
   *   - Agrupadas inline en secciones: "Sistema NimOS" y "Aplicaciones"
   *   - Sin sidebar de categorías (ruido visual innecesario)
   *   - Sin "Recomendado" ni "Anclados" (duplican escritorio y taskbar)
   *   - Footer con usuario + botón power
   *
   * Lógica preservada (sin cambios):
   *   - APP_META + listAllApps de $lib/apps.js
   *   - fetch /api/my-apps · permisos de usuario
   *   - fetch /api/docker/installed-apps · apps Docker
   *   - openWindow + windowList de $lib/stores/windows.js
   *   - Search por nombre/id
   *   - Keyboard: Esc cierra · Enter abre primera
   */
  import { APP_META, listAllApps } from '$lib/apps.js';
  import { openWindow, windowList } from '$lib/stores/windows.js';
  import { getToken, logout, user } from '$lib/stores/auth.js';
  import AppIcon from '$lib/ui/AppIcon.svelte';

  export let visible = false;

  let dockerApps = [];
  let allowedApps = null;

  $: if (visible) {
    loadDockerApps();
    loadMyApps();
  }

  async function loadMyApps() {
    try {
      const res = await fetch('/api/my-apps', {
        headers: { 'Authorization': `Bearer ${getToken()}` },
      });
      const data = await res.json();
      allowedApps = data.apps;
    } catch {
      allowedApps = 'all';
    }
  }

  async function loadDockerApps() {
    try {
      const res = await fetch('/api/docker/installed-apps', {
        headers: { 'Authorization': `Bearer ${getToken()}` },
      });
      const data = await res.json();
      if (data.apps && Array.isArray(data.apps)) {
        dockerApps = data.apps.map(app => ({
          id: app.id,
          name: app.name,
          icon: app.icon || '📦',
          fallback: '📦',
          port: app.port,
          isWebApp: true,
          external: app.external || false,
          category: 'docker',
          running: app.running || false,
          description: app.description || 'app docker',
          // SHIELD-P2 · puerto directo cerrado → se llega vía Caddy
          accessMode: app.accessMode || 'lan',
          externalUrl: app.externalUrl || '',
        }));
      }
    } catch {}
  }

  function canAccess(appId) {
    if (allowedApps === 'all') return true;
    if (Array.isArray(allowedApps)) return allowedApps.includes(appId);
    return true;
  }

  $: systemApps = listAllApps()
    .map(a => ({ ...a, isSystem: true }))
    .filter(a => !a.hidden && canAccess(a.id));

  // Apps del sistema y utilidades en una sola sección "Sistema NimOS"
  $: sysApps = systemApps.filter(a =>
    a.category === 'system' || a.category === 'utilities'
  );

  // Apps Docker en otra sección
  $: dkApps = dockerApps.filter(a => canAccess(a.id));

  // Sin buscador: se muestran todas directamente
  $: filteredSys = sysApps;
  $: filteredDk  = dkApps;

  $: openAppIds = new Set($windowList.map(w => w.appId));

  function launch(app) {
    visible = false;
    if (app.isWebApp) {
      // SHIELD-P2 · candado activo: el puerto directo no existe en la LAN,
      // la única puerta es la URL Caddy.
      const lockedUrl = app.accessMode === 'caddy_only' ? app.externalUrl : '';
      if (app.external) {
        const url = lockedUrl || `${window.location.protocol}//${window.location.hostname}:${app.port}`;
        window.open(url, '_blank');
        return;
      }
      openWindow(app.id, { width: 1100, height: 700 }, {
        isWebApp: true,
        port: app.port,
        appName: app.name,
        externalUrl: lockedUrl,
      });
    } else {
      const meta = APP_META[app.id];
      openWindow(app.id, { width: meta?.width || 800, height: meta?.height || 520 });
    }
  }

  function handleKeydown(e) {
    if (!visible) return;
    if (e.key === 'Escape') {
      visible = false;
    } else if (e.key === 'Enter') {
      const first = filteredSys[0] || filteredDk[0];
      if (first) launch(first);
    }
  }

  function handlePower() {
    visible = false;
    logout();
  }

  $: userName = $user?.username || 'usuario';
  $: userInitial = userName.charAt(0).toUpperCase();
</script>

<svelte:window on:keydown={handleKeydown} />

{#if visible}
  <div class="overlay" on:click={() => visible = false} role="presentation"></div>

  <div class="start-menu" on:click|stopPropagation role="presentation">

    <!-- ─── Scrollable content ─── -->
    <div class="sm-content">

      {#if filteredSys.length > 0}
        <div class="sm-section-head">
          <span>Sistema NimOS</span>
          <span class="count">{filteredSys.length}</span>
        </div>

        <div class="sm-grid">
          {#each filteredSys as app}
            <button
              class="app-tile"
              on:click={() => launch(app)}
              title={app.name}
            >
              <div class="app-tile-ico sys">
                <AppIcon src={app.icon} alt={app.name} fallback={app.fallback || '📦'} size="md" />
              </div>
              <span class="app-tile-name">{app.name}</span>
              {#if openAppIds.has(app.id)}
                <span class="app-tile-running"></span>
              {/if}
            </button>
          {/each}
        </div>
      {/if}

      {#if filteredSys.length > 0 && filteredDk.length > 0}
        <div class="sm-divider"></div>
      {/if}

      {#if filteredDk.length > 0}
        <div class="sm-section-head">
          <span>Aplicaciones</span>
          <span class="count">{filteredDk.length}</span>
        </div>

        <div class="sm-grid">
          {#each filteredDk as app}
            <button
              class="app-tile"
              on:click={() => launch(app)}
              title={app.name}
            >
              <div class="app-tile-ico dk">
                <AppIcon src={app.icon} alt={app.name} fallback={app.fallback || '📦'} size="md" />
              </div>
              <span class="app-tile-name">{app.name}</span>
              {#if openAppIds.has(app.id) || app.running}
                <span class="app-tile-running"></span>
              {/if}
            </button>
          {/each}
        </div>
      {/if}

      {#if filteredSys.length === 0 && filteredDk.length === 0}
        <div class="empty">
          <div class="empty-ic">◌</div>
          <div class="empty-msg">Sin apps disponibles</div>
        </div>
      {/if}

    </div>

    <!-- ─── Bottom · User + Power ─── -->
    <div class="sm-footer">
      <div class="sm-user" role="button" tabindex="0">
        <div class="sm-user-avatar">{userInitial}</div>
        <div class="sm-user-info">
          <span class="sm-user-name">{userName}</span>
          <span class="sm-user-status">online</span>
        </div>
      </div>
      <button
        class="sm-power"
        on:click={handlePower}
        title="Cerrar sesión"
      >⏻</button>
    </div>

  </div>
{/if}

<style>
  /* ═══════════════════════════════════════════════════════════
     OVERLAY · captura click para cerrar
     ═══════════════════════════════════════════════════════════ */
  .overlay {
    position: fixed;
    inset: 0;
    background: transparent;
    z-index: 9100;
  }

  /* ═══════════════════════════════════════════════════════════
     START MENU · estilo W11, anclado al taskbar con separación
     ═══════════════════════════════════════════════════════════ */
  .start-menu {
    position: fixed;
    bottom: calc(var(--taskbar-height, 44px) + 12px);
    left: 12px;
    width: 640px;
    height: 600px;
    max-height: calc(100vh - var(--taskbar-height, 44px) - 24px);
    background: rgba(20, 20, 26, 0.72);
    backdrop-filter: blur(22px) saturate(1.3);
    -webkit-backdrop-filter: blur(22px) saturate(1.3);
    border: 1px solid rgba(255, 255, 255, 0.10);
    border-radius: 14px;
    z-index: 9200;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    font-family: var(--font-sans, ui-sans-serif, system-ui, sans-serif);
    box-shadow:
      0 12px 40px rgba(0, 0, 0, 0.5),
      0 0 0 1px rgba(0, 255, 159, 0.04);
    animation: menu-in 0.2s cubic-bezier(0.2, 0, 0, 1.1);
  }

  @keyframes menu-in {
    from { opacity: 0; transform: translateY(20px); }
    to   { opacity: 1; transform: translateY(0); }
  }

  /* ─── Scrollable content ─── */
  .sm-content {
    flex: 1;
    overflow-y: auto;
    padding: 18px 22px 18px;
  }
  .sm-content::-webkit-scrollbar { width: 5px; }
  .sm-content::-webkit-scrollbar-track { background: transparent; }
  .sm-content::-webkit-scrollbar-thumb {
    background: var(--bd-2, #20202a);
    border-radius: 3px;
  }

  .sm-section-head {
    font-size: 10px;
    color: var(--fg-4, #7a7a82);
    letter-spacing: 1.5px;
    font-weight: 600;
    text-transform: uppercase;
    padding: 10px 4px 12px;
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .sm-section-head .count {
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 10px;
    color: var(--fg-5, #5a5a62);
    font-weight: 500;
    letter-spacing: 0.3px;
    text-transform: none;
  }

  /* App grid · 6 columns */
  .sm-grid {
    display: grid;
    grid-template-columns: repeat(5, 1fr);
    gap: 8px;
    margin-bottom: 10px;
  }

  .app-tile {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    padding: 18px 6px 14px;
    border-radius: 10px;
    cursor: pointer;
    transition: all 0.12s;
    position: relative;
    background: transparent;
    border: none;
    color: inherit;
    font-family: inherit;
  }
  .app-tile:hover {
    background: rgba(255, 255, 255, 0.04);
  }
  .app-tile:hover .app-tile-ico {
    transform: scale(1.05);
  }
  .app-tile:focus-visible {
    outline: none;
    background: var(--ui-select-bg, rgba(122, 158, 177, 0.12));
  }

  .app-tile-ico {
    width: 54px;
    height: 54px;
    border-radius: 12px;
    display: flex;
    align-items: center;
    justify-content: center;
    color: #fff;
    transition: transform 0.15s;
    flex-shrink: 0;
    overflow: hidden;
  }
  /* System apps · sobrio */
  .app-tile-ico.sys {
    background: rgba(0, 255, 159, 0.08);
    border: 1px solid rgba(0, 255, 159, 0.15);
  }
  /* Docker apps · neutro · respeta el icono real de la app */
  .app-tile-ico.dk {
    background: var(--bg-card, #1a1a20);
    border: 1px solid var(--bd-2, #20202a);
  }

  .app-tile-name {
    font-size: 10.5px;
    color: var(--fg-2, #d0d0d4);
    text-align: center;
    font-weight: 400;
    line-height: 1.2;
    letter-spacing: 0.1px;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    width: 100%;
  }

  /* Running indicator dot */
  .app-tile-running {
    position: absolute;
    top: 8px;
    right: 14px;
    width: 6px;
    height: 6px;
    background: var(--st-ok, #00ff9f);
    border-radius: 50%;
    box-shadow: 0 0 4px var(--st-ok, #00ff9f);
  }

  /* Divider between sections */
  .sm-divider {
    height: 1px;
    background: var(--bd, rgba(255, 255, 255, 0.05));
    margin: 8px 4px;
  }

  /* Empty state */
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 40px 20px;
    gap: 12px;
    color: var(--fg-4, #7a7a82);
  }
  .empty-ic {
    font-size: 32px;
    opacity: 0.5;
  }
  .empty-msg {
    font-size: 12px;
    text-align: center;
  }

  /* ─── Bottom · User + Power ─── */
  .sm-footer {
    border-top: 1px solid var(--bd, rgba(255, 255, 255, 0.05));
    padding: 10px 14px;
    display: flex;
    align-items: center;
    gap: 10px;
    background: rgba(0, 0, 0, 0.2);
  }
  .sm-user {
    display: flex;
    align-items: center;
    gap: 10px;
    flex: 1;
    padding: 6px 8px;
    border-radius: 5px;
    cursor: pointer;
    transition: background 0.1s;
  }
  .sm-user:hover {
    background: rgba(255, 255, 255, 0.03);
  }
  .sm-user-avatar {
    width: 28px;
    height: 28px;
    border-radius: 50%;
    background: var(--nim-green, #00ff9f);
    color: var(--bg-window, #0c0c0f);
    display: flex;
    align-items: center;
    justify-content: center;
    font-weight: 700;
    font-size: 12px;
    flex-shrink: 0;
  }
  .sm-user-info {
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .sm-user-name {
    font-size: 12.5px;
    color: var(--fg, #f0f0f0);
    font-weight: 500;
  }
  .sm-user-status {
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 9px;
    color: var(--st-ok, #00ff9f);
    letter-spacing: 0.3px;
    display: flex;
    align-items: center;
    gap: 5px;
  }
  .sm-user-status::before {
    content: '';
    width: 4px;
    height: 4px;
    background: var(--st-ok, #00ff9f);
    border-radius: 50%;
    box-shadow: 0 0 3px var(--st-ok, #00ff9f);
  }

  .sm-power {
    width: 32px;
    height: 32px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 5px;
    cursor: pointer;
    color: var(--fg-3, #9c9ca4);
    font-size: 14px;
    transition: all 0.12s;
    border: 1px solid transparent;
    background: transparent;
  }
  .sm-power:hover {
    color: var(--st-crit, #ff5a5a);
    background: rgba(255, 90, 90, 0.06);
    border-color: rgba(255, 90, 90, 0.2);
  }
</style>
