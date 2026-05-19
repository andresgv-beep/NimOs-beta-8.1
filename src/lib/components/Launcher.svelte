<script>
  /**
   * Launcher · Cajón de apps NimOS Beta 8.1
   * ─────────────────────────────────────────
   * Se abre desde el logo NimOS del taskbar.
   *
   * Estética técnica retro:
   *   - Chaflán inferior-derecho 18px · firma NimOS
   *   - Sidebar izquierdo de categorías con barrita verde activa
   *   - Buscador retro con prompt `❯` verde + cursor parpadeando
   *   - Tiles con icono pixel art + nombre + descripción mono
   *   - Apps instaladas tienen barrita sage a la izquierda
   *   - Footer técnico con contadores
   *
   * Lógica preservada (sin cambios):
   *   - APP_META + listAllApps de $lib/apps.js
   *   - fetch /api/my-apps · permisos de usuario
   *   - fetch /api/docker/installed-apps · apps Docker
   *   - openWindow + windowList de $lib/stores/windows.js
   *   - Filter por categoría · Search por nombre/id
   *   - Keyboard: Esc cierra · Enter abre primera
   */
  import { APP_META, listAllApps } from '$lib/apps.js';
  import { openWindow, windowList } from '$lib/stores/windows.js';
  import { getToken } from '$lib/stores/auth.js';

  export let visible = false;

  let filter = 'all'; // 'all' | 'system' | 'utilities' | 'docker'
  let searchTerm = '';
  let dockerApps = [];
  let allowedApps = null;
  let searchEl;

  $: if (visible) {
    filter = 'all';
    searchTerm = '';
    loadDockerApps();
    loadMyApps();
    setTimeout(() => searchEl?.focus(), 50);
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
        }));
      }
    } catch {}
  }

  function canAccess(appId) {
    if (allowedApps === 'all') return true;
    if (Array.isArray(allowedApps)) return allowedApps.includes(appId);
    return true;
  }

  $: systemApps = listAllApps().map(a => ({ ...a, isSystem: true }));

  $: allApps = (() => {
    const seen = new Set();
    return [...systemApps, ...dockerApps].filter(app => {
      if (seen.has(app.id)) return false;
      if (app.hidden) return false;
      if (!canAccess(app.id)) return false;
      seen.add(app.id);
      return true;
    });
  })();

  $: filteredApps = (() => {
    let list = allApps;
    if (filter !== 'all') {
      list = list.filter(a => a.category === filter);
    }
    if (searchTerm) {
      const q = searchTerm.toLowerCase();
      list = list.filter(a =>
        a.name.toLowerCase().includes(q) ||
        a.id.toLowerCase().includes(q)
      );
    }
    return list;
  })();

  $: systemCount    = allApps.filter(a => a.category === 'system').length;
  $: utilitiesCount = allApps.filter(a => a.category === 'utilities').length;
  $: dockerCount    = allApps.filter(a => a.category === 'docker').length;
  $: openAppIds     = new Set($windowList.map(w => w.appId));

  function launch(app) {
    visible = false;
    if (app.isWebApp) {
      if (app.external) {
        window.open(`${window.location.protocol}//${window.location.hostname}:${app.port}`, '_blank');
        return;
      }
      openWindow(app.id, { width: 1100, height: 700 }, {
        isWebApp: true,
        port: app.port,
        appName: app.name,
      });
    } else {
      const meta = APP_META[app.id];
      openWindow(app.id, { width: meta?.width || 800, height: meta?.height || 520 });
    }
  }

  function isIconUrl(icon) {
    return icon && (icon.startsWith('http') || icon.startsWith('/'));
  }

  function handleKeydown(e) {
    if (!visible) return;
    if (e.key === 'Escape') {
      visible = false;
    } else if (e.key === 'Enter' && filteredApps.length > 0) {
      launch(filteredApps[0]);
    }
  }

  // Cats list para el sidebar
  $: cats = [
    { id: 'all',       label: 'TODAS',      count: allApps.length },
    { id: 'system',    label: 'SISTEMA',    count: systemCount },
    { id: 'utilities', label: 'UTILIDADES', count: utilitiesCount },
    ...(dockerCount > 0 ? [{ id: 'docker', label: 'DOCKER', count: dockerCount }] : []),
  ];

  // Helper para mostrar descripción
  function getDesc(app) {
    if (app.description) return app.description;
    if (app.category === 'system') return '· sistema';
    if (app.category === 'utilities') return '· utilidad';
    if (app.category === 'docker') return '· docker';
    return '· app';
  }
</script>

<svelte:window on:keydown={handleKeydown} />

{#if visible}
  <div class="overlay" on:click={() => visible = false} role="presentation"></div>

  <div class="drawer" on:click|stopPropagation role="presentation">

    <!-- ─── Header · buscador ─── -->
    <div class="drawer-header">
      <div class="drawer-search" class:focused={searchTerm}>
        <span class="prompt">❯</span>
        <input
          bind:this={searchEl}
          bind:value={searchTerm}
          placeholder="buscar aplicaciones..."
          autocomplete="off"
        />
        <span class="cursor"></span>
      </div>
    </div>

    <!-- ─── Body · sidebar de cats + grid de apps ─── -->
    <div class="drawer-body">

      <!-- Sidebar de categorías -->
      <div class="drawer-cats">
        {#each cats as cat}
          <div
            class="drawer-cat"
            class:active={filter === cat.id}
            on:click={() => filter = cat.id}
            role="button"
            tabindex="0"
            on:keydown={(e) => e.key === 'Enter' && (filter = cat.id)}
          >
            <span class="cat-label">{cat.label}</span>
            <span class="cat-count">{cat.count}</span>
          </div>
        {/each}
      </div>

      <!-- Grid de apps -->
      <div class="drawer-apps">
        {#if filteredApps.length === 0}
          <div class="empty">
            <div class="empty-ic">◌</div>
            <div class="empty-msg">
              {searchTerm
                ? `Sin resultados para "${searchTerm}"`
                : 'Sin apps en esta categoría'}
            </div>
          </div>
        {:else}
          {#each filteredApps as app}
            <div
              class="app-tile"
              class:installed={app.isSystem || true}
              class:running={openAppIds.has(app.id)}
              on:click={() => launch(app)}
              on:keydown={(e) => e.key === 'Enter' && launch(app)}
              role="button"
              tabindex="0"
              title={app.name}
            >
              <div class="app-tile-icon">
                {#if isIconUrl(app.icon)}
                  <img src={app.icon} alt={app.name} on:error={(e) => e.target.style.display = 'none'} />
                {:else}
                  <span class="app-emoji">{app.fallback || app.icon || '📦'}</span>
                {/if}
              </div>
              <div class="app-tile-text">
                <div class="app-tile-name">{app.name}</div>
                <div class="app-tile-desc">▸ {getDesc(app)}</div>
              </div>
              {#if openAppIds.has(app.id)}
                <div class="app-running-led"></div>
              {/if}
            </div>
          {/each}
        {/if}
      </div>
    </div>

    <!-- ─── Footer técnico ─── -->
    <div class="drawer-footer">
      <span class="stat">
        <b>{allApps.length}</b> instaladas
        {#if openAppIds.size > 0}
          · <b class="active">{openAppIds.size}</b> abiertas
        {/if}
      </span>
      <span class="brand">NIMOS · APP DRAWER</span>
    </div>

  </div>
{/if}

<style>
  /* Overlay · captura click para cerrar */
  .overlay {
    position: fixed;
    inset: 0;
    background: transparent;
    z-index: 9100;
  }

  /* ═══════════════════════════════════════════════════════════
     DRAWER · sale arriba del logo del taskbar
     ═══════════════════════════════════════════════════════════ */
  .drawer {
    position: fixed;
    left: 8px;
    bottom: calc(var(--taskbar-height, 44px) + 6px);
    width: 520px;
    height: 480px;
    max-height: calc(100vh - var(--taskbar-height, 44px) - 24px);
    background: linear-gradient(180deg, #161616 0%, #0f0f0f 100%);
    border: 1px solid var(--border-bright, #2a2a2a);
    z-index: 9200;
    display: flex;
    flex-direction: column;
    font-family: var(--font-mono, 'JetBrains Mono', monospace);
    box-shadow:
      0 -10px 40px rgba(0, 0, 0, 0.6),
      0 0 60px rgba(220, 255, 235, 0.03);
    /* Chaflán inferior-derecho · firma NimOS */
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 18px), calc(100% - 18px) 100%, 0 100%);
    animation: drawer-in 0.18s cubic-bezier(0.16, 1, 0.3, 1) both;
  }

  @keyframes drawer-in {
    from { opacity: 0; transform: translateY(8px); }
    to   { opacity: 1; transform: translateY(0); }
  }

  /* ─── Header · buscador con prompt ─── */
  .drawer-header {
    padding: 12px 14px 10px;
    border-bottom: 1px solid var(--border, #1f1f1f);
    flex-shrink: 0;
  }
  .drawer-search {
    display: flex;
    align-items: center;
    gap: 10px;
    background: var(--bg-0, #0a0a0a);
    border: 1px solid var(--border-bright, #2a2a2a);
    padding: 8px 12px;
    font-family: var(--font-mono, monospace);
    font-size: 11px;
    color: var(--fg, #e8e8e8);
    letter-spacing: 0.5px;
    transition: border-color 0.12s;
  }
  .drawer-search:focus-within {
    border-color: var(--accent-color, #00ff9f);
  }
  .prompt {
    color: var(--accent-color, #00ff9f);
    font-weight: 700;
    text-shadow: 0 0 5px rgba(0, 255, 159, 0.4);
    font-size: 13px;
    line-height: 1;
  }
  .drawer-search input {
    flex: 1;
    background: transparent;
    border: none;
    outline: none;
    color: var(--fg, #e8e8e8);
    font-family: inherit;
    font-size: 12px;
    letter-spacing: 0.5px;
  }
  .drawer-search input::placeholder {
    color: var(--fg-mute, #5a5a62);
  }
  .cursor {
    width: 6px;
    height: 13px;
    background: var(--accent-color, #00ff9f);
    box-shadow: 0 0 4px rgba(0, 255, 159, 0.4);
    animation: blink 0.9s steps(1) infinite;
    flex-shrink: 0;
  }
  @keyframes blink {
    0%, 50% { opacity: 1 }
    51%, 100% { opacity: 0 }
  }

  /* ─── Body · sidebar + grid ─── */
  .drawer-body {
    flex: 1;
    display: grid;
    grid-template-columns: 120px 1fr;
    overflow: hidden;
    min-height: 0;
  }

  /* Sidebar de categorías */
  .drawer-cats {
    background: var(--bg-2, #181818);
    border-right: 1px solid var(--border, #1f1f1f);
    padding: 8px 0;
    display: flex;
    flex-direction: column;
    overflow-y: auto;
  }
  .drawer-cat {
    padding: 8px 14px;
    font-family: var(--font-mono, monospace);
    font-size: 10px;
    color: var(--fg-dim, #9a9aa3);
    letter-spacing: 1.5px;
    text-transform: uppercase;
    font-weight: 600;
    cursor: pointer;
    border-left: 2px solid transparent;
    transition: background 0.12s, color 0.12s;
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .drawer-cat:hover {
    background: rgba(255, 255, 255, 0.025);
    color: var(--fg, #e8e8e8);
  }
  .drawer-cat.active {
    background: rgba(0, 255, 159, 0.07);
    color: var(--accent, #ffffff);
    border-left-color: var(--accent-color, #00ff9f);
    text-shadow: 0 0 5px rgba(220, 255, 235, 0.6);
  }
  .cat-count {
    font-size: 9px;
    color: var(--fg-trace, #333339);
    font-weight: 400;
    font-feature-settings: "tnum";
  }
  .drawer-cat.active .cat-count {
    color: var(--accent-color, #00ff9f);
  }

  /* Grid de apps */
  .drawer-apps {
    padding: 10px 12px;
    overflow-y: auto;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 3px;
    align-content: start;
  }

  /* ─── App tile · icono + nombre + descripción ─── */
  .app-tile {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    cursor: pointer;
    transition: background 0.12s;
    position: relative;
  }
  .app-tile:hover {
    background: rgba(255, 255, 255, 0.04);
  }
  .app-tile:focus-visible {
    outline: 1px solid var(--accent-color, #00ff9f);
    outline-offset: -1px;
  }

  /* Barrita izquierda · marca app instalada (system apps siempre, docker variable) */
  .app-tile.installed::before {
    content: '';
    position: absolute;
    left: 0;
    top: 50%;
    transform: translateY(-50%);
    width: 2px;
    height: 60%;
    background: var(--sage, #7dd3a8);
    opacity: 0.5;
  }
  .app-tile:hover.installed::before {
    opacity: 1;
    box-shadow: 0 0 4px rgba(125, 211, 168, 0.4);
  }

  .app-tile-icon {
    width: 26px;
    height: 26px;
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .app-tile-icon img {
    width: 24px;
    height: 24px;
    object-fit: contain;
    filter:
      drop-shadow(0 0 4px rgba(220, 255, 235, 0.35))
      drop-shadow(0 0 1px rgba(255, 255, 255, 0.5));
  }
  .app-emoji {
    font-size: 22px;
    line-height: 1;
    filter: drop-shadow(0 0 4px rgba(220, 255, 235, 0.35));
  }

  .app-tile-text {
    flex: 1;
    min-width: 0;
  }
  .app-tile-name {
    font-family: var(--font-mono, monospace);
    font-size: 11px;
    color: var(--fg, #e8e8e8);
    letter-spacing: 0.5px;
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .app-tile-desc {
    font-family: var(--font-mono, monospace);
    font-size: 8.5px;
    color: var(--fg-trace, #333339);
    letter-spacing: 0.8px;
    margin-top: 2px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  /* LED running · punto verde en la esquina superior-derecha del tile */
  .app-running-led {
    position: absolute;
    top: 6px;
    right: 6px;
    width: 5px;
    height: 5px;
    background: var(--accent-color, #00ff9f);
    box-shadow: 0 0 4px rgba(0, 255, 159, 0.5);
  }

  /* ─── Empty state ─── */
  .empty {
    grid-column: 1 / -1;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 12px;
    padding: 40px 20px;
    color: var(--fg-mute, #5a5a62);
  }
  .empty-ic {
    width: 38px; height: 38px;
    border: 1px solid var(--border-bright, #2a2a2a);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 18px;
    color: var(--fg-trace, #333339);
  }
  .empty-msg {
    font-family: var(--font-mono, monospace);
    font-size: 10px;
    letter-spacing: 1px;
    text-align: center;
  }

  /* ─── Footer técnico ─── */
  .drawer-footer {
    height: 28px;
    background: var(--bg-2, #181818);
    border-top: 1px solid var(--border, #1f1f1f);
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 14px;
    font-family: var(--font-mono, monospace);
    font-size: 8.5px;
    color: var(--fg-trace, #333339);
    letter-spacing: 1.5px;
    flex-shrink: 0;
  }
  .stat {
    color: var(--fg-mute, #5a5a62);
  }
  .stat b {
    color: var(--fg, #e8e8e8);
    font-weight: 400;
  }
  .stat b.active {
    color: var(--accent-color, #00ff9f);
    text-shadow: 0 0 3px rgba(0, 255, 159, 0.4);
  }
  .brand {
    font-weight: 600;
  }

  /* Scrollbar técnico minimal */
  .drawer-cats::-webkit-scrollbar,
  .drawer-apps::-webkit-scrollbar {
    width: 6px;
  }
  .drawer-cats::-webkit-scrollbar-track,
  .drawer-apps::-webkit-scrollbar-track {
    background: var(--bg-0, #0a0a0a);
  }
  .drawer-cats::-webkit-scrollbar-thumb,
  .drawer-apps::-webkit-scrollbar-thumb {
    background: var(--border-bright, #2a2a2a);
  }
  .drawer-cats::-webkit-scrollbar-thumb:hover,
  .drawer-apps::-webkit-scrollbar-thumb:hover {
    background: var(--fg-mute, #5a5a62);
  }
</style>
