<script>
  /**
   * NimTorrent · Cliente de torrents (daemon C++/libtorrent)
   * ────────────────────────────────────────────────────────
   * Reconstruido sobre el lenguaje visual v3 a partir del mockup
   * nimtorrent-mockup.html. Layout:
   *   · Sidebar (AppShell) → filtros por estado con dot + count
   *   · Header → pool selector + pausar todos + añadir torrent
   *   · Split vertical → lista de torrents (arriba) + detalle (abajo)
   *   · Footer del main → stats globales DL/UL
   *
   * NOTA: los datos son de ejemplo (igual que el mockup). El backend
   * (daemon Go/C++) expone las APIs; falta cablear fetch + acciones.
   * Puntos de integración marcados con TODO(api).
   */
  import AppShell from '$lib/components/AppShell.svelte';

  let active = 'all';
  let selectedId = 't1';

  // ─── Datos de ejemplo (TODO(api): sustituir por fetch al daemon) ───
  const torrents = [
    { id: 't1', name: 'Ubuntu.24.10.desktop-amd64.iso', state: 'dl',
      size: '4.8 GB', dl: '3.2 MB/s', ul: '142 KB/s', peers: '38', seeds: '124',
      eta: '8m 12s', progress: 68 },
    { id: 't2', name: 'Debian-13.0.0-amd64-DVD-1.iso', state: 'dl',
      size: '3.7 GB', dl: '820 KB/s', ul: '—', peers: '12', seeds: '31',
      eta: '52m', progress: 24 },
    { id: 't3', name: 'Fedora-Workstation-Live-x86_64-41-1.4.iso', state: 'dl',
      size: '2.1 GB', dl: '240 KB/s', ul: '—', peers: '4', seeds: '18',
      eta: '1m 35s', progress: 91 },
    { id: 't4', name: 'archlinux-2025.05.01-x86_64.iso', state: 'seeding',
      size: '1.2 GB', dl: '—', ul: '512 KB/s', peers: '21', seeds: '—',
      eta: '∞', progress: 100 },
    { id: 't5', name: 'openSUSE-Leap-15.6-DVD-x86_64-Media.iso', state: 'seeding',
      size: '4.5 GB', dl: '—', ul: '98 KB/s', peers: '8', seeds: '—',
      eta: '∞', progress: 100 },
    { id: 't6', name: 'linuxmint-22-cinnamon-64bit.iso', state: 'paused',
      size: '2.8 GB', dl: '—', ul: '—', peers: '—', seeds: '—',
      eta: '—', progress: 42 },
    { id: 't7', name: 'manjaro-kde-24.0-stable-x86_64.iso', state: 'error',
      size: '3.4 GB', dl: '—', ul: '—', peers: '0', seeds: '0',
      eta: 'error', progress: 14 },
    { id: 't8', name: 'popos_22.04_amd64_intel_22.iso', state: 'paused',
      size: '2.6 GB', dl: '—', ul: '—', peers: '—', seeds: '—',
      eta: '—', progress: 8 },
  ];

  // ─── Filtros por estado ───
  const stateMatch = {
    all:     () => true,
    active:  t => t.state === 'dl' || t.state === 'seeding',
    dl:      t => t.state === 'dl',
    seeding: t => t.state === 'seeding',
    paused:  t => t.state === 'paused',
    error:   t => t.state === 'error',
  };
  $: filtered = torrents.filter(stateMatch[active] || (() => true));
  $: selected = torrents.find(t => t.id === selectedId) || null;

  // counts por filtro
  $: counts = {
    all:     torrents.length,
    active:  torrents.filter(stateMatch.active).length,
    dl:      torrents.filter(stateMatch.dl).length,
    seeding: torrents.filter(stateMatch.seeding).length,
    paused:  torrents.filter(stateMatch.paused).length,
    error:   torrents.filter(stateMatch.error).length,
  };

  // dot de color por filtro (inline span, va como item.icon en AppShell)
  const dot = (cls) => `<span class="nt-dot nt-dot-${cls}"></span>`;

  $: sections = [
    {
      label: 'Estado',
      items: [
        { id: 'all',     label: 'Todos',         icon: dot('all'),     badge: counts.all },
        { id: 'active',  label: 'Activos',        icon: dot('active'),  badge: counts.active },
        { id: 'dl',      label: 'Descargando',    icon: dot('dl'),      badge: counts.dl },
        { id: 'seeding', label: 'Compartiendo',   icon: dot('seeding'), badge: counts.seeding },
        { id: 'paused',  label: 'Pausados',       icon: dot('paused'),  badge: counts.paused },
        { id: 'error',   label: 'Con error',      icon: dot('error'),   badge: counts.error },
      ],
    },
  ];

  const stateLabel = { dl: 'Descargando', seeding: 'Compartiendo', paused: 'Pausado', error: 'Error', checking: 'Verificando' };

  function selectTorrent(id) { selectedId = id; }

  // ─── Acciones (TODO(api): cablear al daemon) ───
  function pauseAll()    { /* TODO(api): POST /api/torrents/pause-all */ }
  function addTorrent()  { /* TODO(api): abrir modal magnet/file */ }
  function pauseOne()    { /* TODO(api): POST /api/torrents/:id/pause */ }
  function removeOne()   { /* TODO(api): DELETE /api/torrents/:id */ }
</script>

<AppShell
  appId="nimtorrent"
  title="Torrent"
  headerIcon="↓"
  {sections}
  bind:active
>
  <!-- ═══ HEADER · pool selector + pausar todos + añadir ═══ -->
  <svelte:fragment slot="page-header">
    <b>Descargas</b>
    <span class="ph-desc">· {torrents.length} torrents</span>

    <div class="nt-head-actions">
      <div class="pool-select" title="Pool de destino por defecto">
        <span class="pool-select-lbl">Pool</span>
        <span class="pool-cube"></span>
        <span class="pool-select-name">multimedia</span>
        <svg class="pool-select-chev" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
          <polyline points="3 4.5 6 7.5 9 4.5"/>
        </svg>
      </div>

      <button class="icon-btn" title="Pausar todos" on:click={pauseAll}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <rect x="6" y="4" width="4" height="16"/>
          <rect x="14" y="4" width="4" height="16"/>
        </svg>
      </button>

      <button class="btn-add" on:click={addTorrent}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round">
          <line x1="12" y1="5" x2="12" y2="19"/>
          <line x1="5" y1="12" x2="19" y2="12"/>
        </svg>
        Añadir torrent
      </button>
    </div>
  </svelte:fragment>

  <!-- ═══ SPLIT · lista (arriba) + detalle (abajo) ═══ -->
  <div class="nt-split">

    <!-- ─── LISTA ─── -->
    <div class="list-wrap">
      <div class="list-head">
        <span></span>
        <span>Nombre · progreso</span>
        <span>Tamaño</span>
        <span>↓ DL</span>
        <span>↑ UL</span>
        <span>Peers</span>
        <span>Seeds</span>
        <span>ETA</span>
      </div>
      <div class="list-body">
        {#each filtered as t (t.id)}
          <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
          <div class="row" class:selected={t.id === selectedId} on:click={() => selectTorrent(t.id)}>
            <span class="row-led {t.state}"></span>
            <div class="row-name">
              <span class="row-name-text">{t.name}</span>
              <div class="row-bar"><div class="row-bar-fill {t.state}" style="width:{t.progress}%"></div></div>
            </div>
            <span class="row-cell" class:dim={t.state === 'paused' || t.state === 'error'}>{t.size}</span>
            <span class="row-cell" class:dl={t.dl !== '—'} class:dim={t.dl === '—'}>{t.dl}</span>
            <span class="row-cell" class:ul={t.ul !== '—'} class:dim={t.ul === '—'}>{t.ul}</span>
            <span class="row-cell" class:dim={t.peers === '—' || t.state === 'error'}>{t.peers}</span>
            <span class="row-cell" class:dim={t.seeds === '—' || t.state === 'error'}>{t.seeds}</span>
            <span class="row-cell eta" class:dim={t.eta === '—' || t.eta === 'error'}>{t.eta}</span>
          </div>
        {/each}
      </div>
    </div>

    <!-- ─── DETALLE ─── -->
    <div class="detail-wrap">
      {#if selected}
        <div class="detail-head">
          <div class="detail-head-info">
            <div class="detail-name">{selected.name}</div>
            <div class="detail-meta">
              <span class="detail-state {selected.state}">{stateLabel[selected.state] || selected.state}</span>
              <span>2 archivos</span>
              <span class="sep">·</span>
              <span>guardado en /multimedia/downloads/</span>
            </div>
          </div>
          <div class="detail-actions">
            <button class="detail-btn" on:click={pauseOne}>
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <rect x="6" y="4" width="4" height="16"/>
                <rect x="14" y="4" width="4" height="16"/>
              </svg>
              Pausar
            </button>
            <button class="detail-btn danger" on:click={removeOne}>
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <polyline points="3 6 5 6 21 6"/>
                <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>
              </svg>
              Quitar
            </button>
          </div>
        </div>

        <div class="detail-body">
          <div class="detail-progress">
            <div class="detail-progress-head">
              <span class="detail-progress-pct">{selected.progress}%</span>
              <span class="detail-progress-bytes">3.26 GB <span class="of">/</span> {selected.size}</span>
            </div>
            <div class="detail-bar">
              <div class="detail-bar-fill {selected.state}" style="width:{selected.progress}%"></div>
            </div>
          </div>

          <div class="detail-stats">
            <div class="detail-stat">
              <div class="detail-stat-lbl">Velocidad ↓</div>
              <div class="detail-stat-val dl">{selected.dl}</div>
            </div>
            <div class="detail-stat">
              <div class="detail-stat-lbl">Velocidad ↑</div>
              <div class="detail-stat-val ul">{selected.ul}</div>
            </div>
            <div class="detail-stat">
              <div class="detail-stat-lbl">Peers</div>
              <div class="detail-stat-val">{selected.peers}<span class="unit">/ {selected.seeds} seeds</span></div>
            </div>
            <div class="detail-stat">
              <div class="detail-stat-lbl">Tiempo restante</div>
              <div class="detail-stat-val">{selected.eta}</div>
            </div>
          </div>

          <div class="detail-info">
            <div class="detail-info-row">
              <span class="k">Ruta</span>
              <span class="v">/nimos/pools/multimedia/downloads/</span>
            </div>
            <div class="detail-info-row">
              <span class="k">Hash</span>
              <span class="v">a47b9c2e8f5d3a1c9b8e7f2d4a6c1e3b8d5f9a2c</span>
            </div>
            <div class="detail-info-row">
              <span class="k">Añadido</span>
              <span class="v">hace 12 minutos</span>
            </div>
            <div class="detail-info-row">
              <span class="k">Ratio</span>
              <span class="v">0.04 (43 MB / 3.26 GB)</span>
            </div>
          </div>
        </div>
      {:else}
        <div class="detail-empty">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
            <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
            <polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/>
          </svg>
          <span>Selecciona un torrent para ver detalles</span>
        </div>
      {/if}
    </div>
  </div>

  <!-- ═══ FOOTER · stats globales ═══ -->
  <svelte:fragment slot="footer">
    <span class="nt-foot-k">DL</span> <span class="nt-foot-v dl">↓ 4.2 MB/s</span>
    <span class="nt-foot-sep">·</span>
    <span class="nt-foot-k">UL</span> <span class="nt-foot-v ul">↑ 1.1 MB/s</span>
  </svelte:fragment>
  <svelte:fragment slot="footer-right">
    <span class="nt-foot-k">activos</span> <span class="nt-foot-v">{counts.active} / {torrents.length}</span>
  </svelte:fragment>
</AppShell>

<style>
  :global(.nt-dot) {
    width: 7px; height: 7px;
    border-radius: 1.5px;
    flex-shrink: 0;
    display: inline-block;
  }
  :global(.nt-dot-all)     { background: var(--fg-4, #7a7a82); }
  :global(.nt-dot-active)  { background: var(--st-info, #4db8ff); }
  :global(.nt-dot-dl)      { background: var(--st-info, #4db8ff); }
  :global(.nt-dot-seeding) { background: var(--st-ok, #00ff9f); }
  :global(.nt-dot-paused)  { background: var(--fg-4, #7a7a82); }
  :global(.nt-dot-error)   { background: var(--st-crit, #ff5a5a); }

  .ph-desc { color: var(--fg-4, #7a7a82); font-size: 12px; font-weight: 400; }

  /* ═══ HEADER ACTIONS ═══ */
  .nt-head-actions {
    margin-left: auto;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .pool-select {
    display: flex; align-items: center; gap: 8px;
    padding: 5px 10px 5px 9px;
    border-radius: 5px;
    background: var(--bg-card, #15151a);
    border: 1px solid var(--bd-2, #20202a);
    font-size: 11px; cursor: pointer;
    transition: border-color 0.12s;
  }
  .pool-select:hover { border-color: var(--bd-3, #2a2a32); }
  .pool-select-lbl { color: var(--fg-4, #7a7a82); text-transform: uppercase; letter-spacing: 0.5px; font-size: 9px; font-weight: 600; }
  .pool-cube { width: 8px; height: 8px; border-radius: 2px; background: #ff9c5a; flex-shrink: 0; }
  .pool-select-name { color: var(--fg, #f0f0f0); font-family: var(--font-mono); font-size: 11px; }
  .pool-select-chev { width: 9px; height: 9px; color: var(--fg-4, #7a7a82); }

  .icon-btn {
    width: 28px; height: 28px;
    background: var(--bg-card, #15151a);
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 5px;
    color: var(--fg-3, #9c9ca4);
    cursor: pointer; display: flex; align-items: center; justify-content: center; padding: 0;
    transition: background 0.12s, color 0.12s, border-color 0.12s;
  }
  .icon-btn svg { width: 12px; height: 12px; }
  .icon-btn:hover { color: var(--fg, #f0f0f0); border-color: var(--bd-3, #2a2a32); }

  .btn-add {
    display: inline-flex; align-items: center; gap: 6px;
    padding: 6px 12px; border: none; border-radius: 5px;
    background: var(--nim-green, #00ff9f); color: var(--bg-window, #16161a);
    font-size: 11px; font-weight: 600; cursor: pointer; font-family: inherit;
    transition: filter 0.12s;
  }
  .btn-add:hover { filter: brightness(1.08); }
  .btn-add svg { width: 12px; height: 12px; }

  /* ═══ SPLIT ═══ */
  .nt-split {
    height: 100%;
    display: grid;
    grid-template-rows: 1.2fr 1fr;
    overflow: hidden;
    min-height: 0;
  }

  /* ─── LISTA ─── */
  .list-wrap { display: flex; flex-direction: column; overflow: hidden; border-bottom: 1px solid var(--bd-2, #20202a); min-height: 0; }
  .list-head {
    display: grid;
    grid-template-columns: 14px 2.5fr 90px 80px 80px 70px 60px 70px;
    gap: 10px; padding: 8px 24px;
    background: var(--bg-inner, #101015);
    border-bottom: 1px solid var(--bd-2, #20202a);
    font-size: 9px; color: var(--fg-5, #5a5a62);
    text-transform: uppercase; letter-spacing: 0.7px; font-weight: 500;
    flex-shrink: 0;
  }
  .list-body { flex: 1; overflow-y: auto; padding: 4px 0; min-height: 0; }
  .row {
    display: grid;
    grid-template-columns: 14px 2.5fr 90px 80px 80px 70px 60px 70px;
    gap: 10px; padding: 9px 24px; align-items: center;
    font-size: 11px; cursor: pointer;
    border-left: 2px solid transparent;
    transition: background 0.1s;
  }
  .row:hover { background: rgba(255,255,255,0.015); }
  .row.selected { background: var(--ui-select-bg, rgba(122,158,177,0.10)); border-left-color: var(--ui-select, #7a9eb1); }

  .row-led { width: 8px; height: 8px; border-radius: 1.5px; }
  .row-led.dl { background: var(--st-info, #4db8ff); }
  .row-led.seeding { background: var(--st-ok, #00ff9f); }
  .row-led.paused { background: var(--fg-4, #7a7a82); }
  .row-led.error { background: var(--st-crit, #ff5a5a); }

  .row-name { color: var(--fg, #f0f0f0); font-size: 12px; font-weight: 500; display: flex; flex-direction: column; gap: 3px; min-width: 0; }
  .row-name-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

  .row-bar { height: 2px; background: var(--bd-2, #20202a); border-radius: 1px; overflow: hidden; position: relative; }
  .row-bar-fill { position: absolute; top: 0; left: 0; height: 100%; background: var(--st-info, #4db8ff); border-radius: 1px; }
  .row-bar-fill.seeding { background: var(--st-ok, #00ff9f); }
  .row-bar-fill.paused { background: var(--fg-4, #7a7a82); opacity: 0.6; }
  .row-bar-fill.error { background: var(--st-crit, #ff5a5a); }

  .row-cell { font-family: var(--font-mono); color: var(--fg-2, #d0d0d4); font-variant-numeric: tabular-nums; font-size: 11px; }
  .row-cell.dim { color: var(--fg-4, #7a7a82); }
  .row-cell.dl { color: var(--st-info, #4db8ff); }
  .row-cell.ul { color: var(--st-ok, #00ff9f); }
  .row-cell.eta { font-size: 10px; }

  /* ─── DETALLE ─── */
  .detail-wrap { display: flex; flex-direction: column; overflow: hidden; background: var(--bg-main, #1a1a1f); min-height: 0; }
  .detail-empty { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 6px; color: var(--fg-5, #5a5a62); font-size: 12px; }
  .detail-empty svg { width: 32px; height: 32px; opacity: 0.4; }

  .detail-head { padding: 12px 24px 10px; border-bottom: 1px solid var(--bd-2, #20202a); display: flex; align-items: flex-start; gap: 12px; }
  .detail-head-info { flex: 1; min-width: 0; }
  .detail-name { font-size: 13px; color: var(--fg, #f0f0f0); font-weight: 600; letter-spacing: -0.1px; margin-bottom: 4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .detail-meta { font-size: 10px; color: var(--fg-4, #7a7a82); font-family: var(--font-mono); letter-spacing: 0.3px; display: flex; gap: 10px; }
  .detail-meta .sep { color: var(--fg-5, #5a5a62); }
  .detail-state { display: inline-flex; align-items: center; gap: 5px; font-size: 9px; text-transform: uppercase; letter-spacing: 0.7px; font-weight: 600; padding: 2px 7px; border-radius: 3px; background: rgba(77,184,255,0.10); color: var(--st-info, #4db8ff); }
  .detail-state.seeding { background: rgba(0,255,159,0.10); color: var(--st-ok, #00ff9f); }
  .detail-state.paused { background: rgba(255,255,255,0.05); color: var(--fg-4, #7a7a82); }
  .detail-state.error { background: rgba(255,90,90,0.10); color: var(--st-crit, #ff5a5a); }

  .detail-actions { display: flex; gap: 4px; }
  .detail-btn { padding: 5px 10px; border: 1px solid var(--bd-2, #20202a); background: transparent; border-radius: 4px; color: var(--fg-3, #9c9ca4); font-size: 10px; font-family: var(--font-mono); letter-spacing: 0.4px; text-transform: uppercase; cursor: pointer; display: inline-flex; align-items: center; gap: 5px; transition: color 0.12s, border-color 0.12s; }
  .detail-btn svg { width: 10px; height: 10px; }
  .detail-btn:hover { color: var(--fg, #f0f0f0); border-color: var(--bd-3, #2a2a32); }
  .detail-btn.danger:hover { color: var(--st-crit, #ff5a5a); border-color: rgba(255,90,90,0.3); }

  .detail-body { flex: 1; overflow-y: auto; padding: 14px 24px 18px; min-height: 0; }
  .detail-progress { margin-bottom: 16px; }
  .detail-progress-head { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 6px; }
  .detail-progress-pct { font-family: var(--font-mono); font-size: 16px; font-weight: 600; color: var(--st-info, #4db8ff); letter-spacing: -0.3px; }
  .detail-progress-bytes { font-family: var(--font-mono); font-size: 11px; color: var(--fg-3, #9c9ca4); }
  .detail-progress-bytes .of { color: var(--fg-5, #5a5a62); }
  .detail-bar { height: 4px; background: var(--bd-2, #20202a); border-radius: 2px; overflow: hidden; position: relative; }
  .detail-bar-fill { position: absolute; top: 0; left: 0; height: 100%; background: var(--st-info, #4db8ff); border-radius: 2px; }
  .detail-bar-fill.seeding { background: var(--st-ok, #00ff9f); }
  .detail-bar-fill.paused { background: var(--fg-4, #7a7a82); }
  .detail-bar-fill.error { background: var(--st-crit, #ff5a5a); }

  .detail-stats { display: grid; grid-template-columns: repeat(4, 1fr); gap: 0; background: var(--bg-inner, #101015); border-radius: 6px; overflow: hidden; margin-bottom: 14px; }
  .detail-stat { padding: 10px 12px; border-right: 1px solid #1a1a20; }
  .detail-stat:last-child { border-right: none; }
  .detail-stat-lbl { font-size: 9px; color: var(--fg-4, #7a7a82); font-weight: 500; letter-spacing: 0.6px; text-transform: uppercase; margin-bottom: 5px; }
  .detail-stat-val { font-family: var(--font-mono); font-size: 13px; color: var(--fg, #f0f0f0); font-weight: 500; letter-spacing: -0.2px; }
  .detail-stat-val .unit { font-size: 10px; color: var(--fg-4, #7a7a82); margin-left: 3px; font-weight: 400; }
  .detail-stat-val.dl { color: var(--st-info, #4db8ff); }
  .detail-stat-val.ul { color: var(--st-ok, #00ff9f); }

  .detail-info { display: flex; flex-direction: column; gap: 2px; background: var(--bg-inner, #101015); border-radius: 6px; padding: 4px; }
  .detail-info-row { display: grid; grid-template-columns: 90px 1fr; gap: 10px; padding: 7px 10px; align-items: center; font-size: 10px; }
  .detail-info-row + .detail-info-row { border-top: 1px solid #1a1a20; }
  .detail-info-row .k { color: var(--fg-4, #7a7a82); text-transform: uppercase; letter-spacing: 0.6px; font-weight: 500; }
  .detail-info-row .v { font-family: var(--font-mono); color: var(--fg-2, #d0d0d4); word-break: break-all; font-size: 10px; }

  /* ═══ FOOTER stats ═══ */
  .nt-foot-k { color: var(--fg-5, #5a5a62); text-transform: uppercase; letter-spacing: 0.6px; font-weight: 500; font-size: 10px; }
  .nt-foot-v { font-family: var(--font-mono); font-variant-numeric: tabular-nums; color: var(--fg-2, #d0d0d4); font-size: 10px; margin-left: 4px; }
  .nt-foot-v.dl { color: var(--st-info, #4db8ff); }
  .nt-foot-v.ul { color: var(--st-ok, #00ff9f); }
  .nt-foot-sep { color: var(--fg-5, #5a5a62); margin: 0 8px; }
</style>
