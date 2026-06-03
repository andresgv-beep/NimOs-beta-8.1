<script>
  /**
   * NimTorrent · Cliente de torrents (daemon C++/libtorrent, puerto 9091)
   * ────────────────────────────────────────────────────────────────────
   * Reconstruido sobre el lenguaje visual v3 a partir del mockup
   * nimtorrent-mockup.html, cableado al daemon REAL vía el proxy Go:
   *
   *   GET  /api/torrent/torrents   → [{hash,name,state,progress,download_rate,
   *                                    upload_rate,total_done,total_wanted,
   *                                    peers,seeds,save_path,paused}]
   *   GET  /api/torrent/stats      → {total,active,seeding,paused,
   *                                    download_rate,upload_rate}
   *   POST /api/torrent/add        → {magnet,save_path} | {file,save_path}
   *   POST /api/torrent/pause      → {hash}
   *   POST /api/torrent/resume     → {hash}
   *   POST /api/torrent/remove     → {hash,delete_files}
   *   POST /api/torrent/upload     → multipart {torrent, save_path}
   *
   * progress llega 0..1 (float). rates en bytes/s. tamaños en bytes.
   */
  import { onMount, onDestroy } from 'svelte';
  import AppShell from '$lib/components/AppShell.svelte';
  import { getToken, jsonHdrs as hdrs } from '$lib/stores/auth.js';

  let active = 'all';
  let selectedHash = null;
  let torrents = [];
  let stats = { total: 0, active: 0, seeding: 0, paused: 0, download_rate: 0, upload_rate: 0 };
  let loading = true;
  let error = null;
  let pollInterval = null;
  let busy = new Set();   // hashes con acción en curso

  // ─── Formateadores ───
  function fmtBytes(b) {
    if (b === null || b === undefined || b < 0) return '—';
    if (b === 0) return '0 B';
    if (b >= 1e12) return (b / 1e12).toFixed(1) + ' TB';
    if (b >= 1e9)  return (b / 1e9).toFixed(1) + ' GB';
    if (b >= 1e6)  return (b / 1e6).toFixed(1) + ' MB';
    if (b >= 1e3)  return (b / 1e3).toFixed(1) + ' KB';
    return b + ' B';
  }
  function fmtRate(b) {
    if (!b || b < 1) return '—';
    if (b >= 1e9) return (b / 1e9).toFixed(1) + ' GB/s';
    if (b >= 1e6) return (b / 1e6).toFixed(1) + ' MB/s';
    if (b >= 1e3) return (b / 1e3).toFixed(0) + ' KB/s';
    return b.toFixed(0) + ' B/s';
  }
  function fmtETA(t) {
    // ETA estimada: (wanted - done) / download_rate
    if (t.state === 'seeding') return '∞';
    if (t.state === 'paused') return '—';
    if (t.state === 'error') return 'error';
    const remaining = (t.total_wanted || 0) - (t.total_done || 0);
    if (remaining <= 0) return '—';
    if (!t.download_rate || t.download_rate < 1) return '∞';
    let secs = Math.round(remaining / t.download_rate);
    if (secs >= 86400) return Math.floor(secs / 86400) + 'd ' + Math.floor((secs % 86400) / 3600) + 'h';
    if (secs >= 3600)  return Math.floor(secs / 3600) + 'h ' + Math.floor((secs % 3600) / 60) + 'm';
    if (secs >= 60)    return Math.floor(secs / 60) + 'm ' + (secs % 60) + 's';
    return secs + 's';
  }
  function pct(p) { return Math.round((p || 0) * 100); }

  // ─── Carga de datos ───
  async function loadTorrents() {
    try {
      const r = await fetch('/api/torrent/torrents', { headers: hdrs() });
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const data = await r.json();
      torrents = Array.isArray(data) ? data : [];
      error = null;
      // mantener selección válida; si no hay, seleccionar el primero
      if (selectedHash && !torrents.some(t => t.hash === selectedHash)) selectedHash = null;
      if (!selectedHash && torrents.length) selectedHash = torrents[0].hash;
    } catch (e) {
      error = 'Daemon de torrents no disponible';
      torrents = [];
    } finally {
      loading = false;
    }
  }
  async function loadStats() {
    try {
      const r = await fetch('/api/torrent/stats', { headers: hdrs() });
      if (r.ok) stats = await r.json();
    } catch { /* stats no crítico */ }
  }
  async function refresh() { await Promise.all([loadTorrents(), loadStats()]); }

  // ─── Acciones reales ───
  async function post(path, body) {
    const r = await fetch('/api/torrent/' + path, {
      method: 'POST', headers: hdrs(), body: JSON.stringify(body),
    });
    return r.ok;
  }
  async function togglePause(t) {
    if (busy.has(t.hash)) return;
    busy = new Set(busy).add(t.hash);
    await post(t.paused ? 'resume' : 'pause', { hash: t.hash });
    busy.delete(t.hash); busy = new Set(busy);
    await refresh();
  }
  async function removeTorrent(t, deleteFiles = false) {
    if (busy.has(t.hash)) return;
    busy = new Set(busy).add(t.hash);
    await post('remove', { hash: t.hash, delete_files: deleteFiles });
    busy.delete(t.hash); busy = new Set(busy);
    if (selectedHash === t.hash) selectedHash = null;
    await refresh();
  }
  async function pauseAll() {
    await Promise.all(torrents.filter(t => !t.paused).map(t => post('pause', { hash: t.hash })));
    await refresh();
  }

  // ─── Añadir torrent (magnet por ahora; .torrent via upload luego) ───
  let showAdd = false;
  let magnetInput = '';
  let savePathInput = '';
  async function submitAdd() {
    const magnet = magnetInput.trim();
    if (!magnet) return;
    await post('add', { magnet, save_path: savePathInput.trim() });
    magnetInput = ''; savePathInput = ''; showAdd = false;
    await refresh();
  }

  // ─── Filtros ───
  const stateMatch = {
    all:     () => true,
    active:  t => !t.paused && (t.state === 'downloading' || t.state === 'seeding' || t.state === 'metadata' || t.state === 'checking'),
    dl:      t => !t.paused && (t.state === 'downloading' || t.state === 'metadata'),
    seeding: t => !t.paused && t.state === 'seeding',
    paused:  t => t.paused || t.state === 'paused',
    error:   t => t.state === 'error',
  };
  $: filtered = torrents.filter(stateMatch[active] || (() => true));
  $: selected = torrents.find(t => t.hash === selectedHash) || null;

  $: counts = {
    all:     torrents.length,
    active:  torrents.filter(stateMatch.active).length,
    dl:      torrents.filter(stateMatch.dl).length,
    seeding: torrents.filter(stateMatch.seeding).length,
    paused:  torrents.filter(stateMatch.paused).length,
    error:   torrents.filter(stateMatch.error).length,
  };

  // dot de color por filtro (va como item.icon en AppShell)
  const dot = (cls) => `<span class="nt-dot nt-dot-${cls}"></span>`;
  $: sections = [
    {
      label: 'Estado',
      items: [
        { id: 'all',     label: 'Todos',        icon: dot('all'),     badge: counts.all },
        { id: 'active',  label: 'Activos',       icon: dot('active'),  badge: counts.active },
        { id: 'dl',      label: 'Descargando',   icon: dot('dl'),      badge: counts.dl },
        { id: 'seeding', label: 'Compartiendo',  icon: dot('seeding'), badge: counts.seeding },
        { id: 'paused',  label: 'Pausados',      icon: dot('paused'),  badge: counts.paused },
        { id: 'error',   label: 'Con error',     icon: dot('error'),   badge: counts.error },
      ],
    },
  ];

  // normaliza el state del daemon → clase visual (led/bar)
  function visState(t) {
    if (t.paused || t.state === 'paused') return 'paused';
    if (t.state === 'error') return 'error';
    if (t.state === 'seeding') return 'seeding';
    if (t.state === 'checking') return 'checking';
    return 'dl'; // downloading / metadata / queued
  }
  const stateLabel = {
    downloading: 'Descargando', metadata: 'Obteniendo metadatos', seeding: 'Compartiendo',
    paused: 'Pausado', error: 'Error', checking: 'Verificando', queued: 'En cola',
  };

  function selectTorrent(hash) { selectedHash = hash; }

  // ─── Lifecycle: poll cada 2s ───
  onMount(async () => {
    let attempts = 0;
    while (!getToken() && attempts < 10) { await new Promise(r => setTimeout(r, 200)); attempts++; }
    await refresh();
    pollInterval = setInterval(refresh, 2000);
  });
  onDestroy(() => { if (pollInterval) clearInterval(pollInterval); });
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

      <button class="btn-add" on:click={() => showAdd = !showAdd}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round">
          <line x1="12" y1="5" x2="12" y2="19"/>
          <line x1="5" y1="12" x2="19" y2="12"/>
        </svg>
        Añadir torrent
      </button>
    </div>
  </svelte:fragment>

  <!-- ═══ SPLIT · lista (arriba) + detalle (abajo) ═══ -->
  <!-- Contenedor del cuerpo · barra opcional (auto) + split (flex:1) -->
  <div class="nt-body">
  <!-- Barra de añadir (magnet) · desplegable, fuera del grid split -->
  {#if showAdd}
    <div class="nt-add-bar">
      <input
        class="nt-add-input"
        placeholder="magnet:?xt=urn:btih:…  o  pega un enlace magnet"
        bind:value={magnetInput}
        on:keydown={(e) => e.key === 'Enter' && submitAdd()}
      />
      <input
        class="nt-add-input nt-add-path"
        placeholder="ruta destino (opcional)"
        bind:value={savePathInput}
        on:keydown={(e) => e.key === 'Enter' && submitAdd()}
      />
      <button class="nt-add-go" on:click={submitAdd}>Añadir</button>
      <button class="nt-add-x" on:click={() => showAdd = false} title="Cerrar">✕</button>
    </div>
  {/if}

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
        {#if loading}
          <div class="nt-msg">Cargando torrents…</div>
        {:else if error}
          <div class="nt-msg nt-msg-err">{error}</div>
        {:else if filtered.length === 0}
          <div class="nt-msg">{torrents.length === 0 ? 'No hay torrents. Añade uno con el botón de arriba.' : 'Ningún torrent en este filtro.'}</div>
        {:else}
          {#each filtered as t (t.hash)}
            {@const vs = visState(t)}
            <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
            <div class="row" class:selected={t.hash === selectedHash} on:click={() => selectTorrent(t.hash)}>
              <span class="row-led {vs}"></span>
              <div class="row-name">
                <span class="row-name-text">{t.name}</span>
                <div class="row-bar"><div class="row-bar-fill {vs}" style="width:{pct(t.progress)}%"></div></div>
              </div>
              <span class="row-cell" class:dim={vs === 'paused' || vs === 'error'}>{fmtBytes(t.total_wanted)}</span>
              <span class="row-cell" class:dl={t.download_rate > 0} class:dim={!(t.download_rate > 0)}>{fmtRate(t.download_rate)}</span>
              <span class="row-cell" class:ul={t.upload_rate > 0} class:dim={!(t.upload_rate > 0)}>{fmtRate(t.upload_rate)}</span>
              <span class="row-cell" class:dim={vs === 'error'}>{t.peers ?? '—'}</span>
              <span class="row-cell" class:dim={vs === 'error'}>{t.seeds ?? '—'}</span>
              <span class="row-cell eta" class:dim={vs === 'paused' || vs === 'error'}>{fmtETA(t)}</span>
            </div>
          {/each}
        {/if}
      </div>
    </div>

    <!-- ─── DETALLE ─── -->
    <div class="detail-wrap">
      {#if selected}
        <div class="detail-head">
          <div class="detail-head-info">
            <div class="detail-name">{selected.name}</div>
            <div class="detail-meta">
              <span class="detail-state {visState(selected)}">{stateLabel[selected.state] || selected.state}</span>
              <span class="sep">·</span>
              <span>{selected.save_path || '—'}</span>
            </div>
          </div>
          <div class="detail-actions">
            <button class="detail-btn" on:click={() => togglePause(selected)} disabled={busy.has(selected.hash)}>
              {#if selected.paused || selected.state === 'paused'}
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                  <polygon points="5 3 19 12 5 21 5 3"/>
                </svg>
                Reanudar
              {:else}
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                  <rect x="6" y="4" width="4" height="16"/>
                  <rect x="14" y="4" width="4" height="16"/>
                </svg>
                Pausar
              {/if}
            </button>
            <button class="detail-btn danger" on:click={() => removeTorrent(selected)} disabled={busy.has(selected.hash)}>
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
              <span class="detail-progress-pct">{pct(selected.progress)}%</span>
              <span class="detail-progress-bytes">{fmtBytes(selected.total_done)} <span class="of">/</span> {fmtBytes(selected.total_wanted)}</span>
            </div>
            <div class="detail-bar">
              <div class="detail-bar-fill {visState(selected)}" style="width:{pct(selected.progress)}%"></div>
            </div>
          </div>

          <div class="detail-stats">
            <div class="detail-stat">
              <div class="detail-stat-lbl">Velocidad ↓</div>
              <div class="detail-stat-val dl">{fmtRate(selected.download_rate)}</div>
            </div>
            <div class="detail-stat">
              <div class="detail-stat-lbl">Velocidad ↑</div>
              <div class="detail-stat-val ul">{fmtRate(selected.upload_rate)}</div>
            </div>
            <div class="detail-stat">
              <div class="detail-stat-lbl">Peers</div>
              <div class="detail-stat-val">{selected.peers ?? 0}<span class="unit">/ {selected.seeds ?? 0} seeds</span></div>
            </div>
            <div class="detail-stat">
              <div class="detail-stat-lbl">Tiempo restante</div>
              <div class="detail-stat-val">{fmtETA(selected)}</div>
            </div>
          </div>

          <div class="detail-info">
            <div class="detail-info-row">
              <span class="k">Ruta</span>
              <span class="v">{selected.save_path || '—'}</span>
            </div>
            <div class="detail-info-row">
              <span class="k">Hash</span>
              <span class="v">{selected.hash}</span>
            </div>
            <div class="detail-info-row">
              <span class="k">Estado</span>
              <span class="v">{stateLabel[selected.state] || selected.state}</span>
            </div>
            <div class="detail-info-row">
              <span class="k">Completado</span>
              <span class="v">{fmtBytes(selected.total_done)} / {fmtBytes(selected.total_wanted)} ({pct(selected.progress)}%)</span>
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
  </div>

  <!-- ═══ FOOTER · stats globales reales ═══ -->
  <svelte:fragment slot="footer">
    <span class="nt-foot-k">DL</span> <span class="nt-foot-v dl">↓ {fmtRate(stats.download_rate)}</span>
    <span class="nt-foot-sep">·</span>
    <span class="nt-foot-k">UL</span> <span class="nt-foot-v ul">↑ {fmtRate(stats.upload_rate)}</span>
  </svelte:fragment>
  <svelte:fragment slot="footer-right">
    <span class="nt-foot-k">activos</span> <span class="nt-foot-v">{stats.active} / {stats.total}</span>
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

  /* ═══ BODY · barra opcional + split ═══ */
  .nt-body {
    height: 100%;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    min-height: 0;
  }

  /* ═══ SPLIT ═══ */
  .nt-split {
    flex: 1;
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

  /* ═══ Barra de añadir torrent (magnet) ═══ */
  .nt-add-bar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 24px;
    background: var(--bg-inner, #101015);
    border-bottom: 1px solid var(--bd-2, #20202a);
    flex-shrink: 0;
  }
  .nt-add-input {
    flex: 1;
    min-width: 0;
    background: var(--bg-card, #15151a);
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 5px;
    padding: 6px 10px;
    color: var(--fg, #f0f0f0);
    font-family: var(--font-mono);
    font-size: 11px;
    outline: none;
  }
  .nt-add-input:focus { border-color: var(--nim-green, #00ff9f); }
  .nt-add-path { flex: 0 0 200px; }
  .nt-add-go {
    padding: 6px 14px;
    border: none;
    border-radius: 5px;
    background: var(--nim-green, #00ff9f);
    color: var(--bg-window, #16161a);
    font-size: 11px;
    font-weight: 600;
    cursor: pointer;
    flex-shrink: 0;
  }
  .nt-add-go:hover { filter: brightness(1.08); }
  .nt-add-x {
    width: 26px; height: 26px;
    border: 1px solid var(--bd-2, #20202a);
    background: transparent;
    border-radius: 5px;
    color: var(--fg-4, #7a7a82);
    cursor: pointer;
    flex-shrink: 0;
  }
  .nt-add-x:hover { color: var(--fg, #f0f0f0); border-color: var(--bd-3, #2a2a32); }

  /* ═══ Mensajes de estado de la lista ═══ */
  .nt-msg {
    padding: 28px 24px;
    text-align: center;
    color: var(--fg-5, #5a5a62);
    font-size: 12px;
    font-family: var(--font-mono);
  }
  .nt-msg-err { color: var(--st-crit, #ff5a5a); }

  .detail-btn:disabled { opacity: 0.5; cursor: default; }
</style>
