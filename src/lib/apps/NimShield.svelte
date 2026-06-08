<script>
  /**
   * NimShield · Motor de defensa (WAF + honeypots + bloqueos)
   * ─────────────────────────────────────────────────────────
   * Reconstruido a partir de nimshield-mockup.html, cableado a la API real:
   *
   *   GET  /api/shield/status            → {enabled, blockedIPs, honeypots, rules}
   *   GET  /api/shield/events?limit=200  → {events:[{id,timestamp,category,
   *                                          severity,sourceIP,endpoint,method,rule}]}
   *   GET  /api/shield/blocks            → {blocks:[{ip,reason,rule,expiresAt,createdAt}]}
   *   POST /api/shield/unblock           → {ip}
   *   POST /api/shield/toggle            → {enabled}
   *   GET  /api/shield/whitelist         → {whitelist:[{ip,note,created_at}]}
   *   POST /api/shield/whitelist         → {ip, note}
   *   POST /api/shield/whitelist/remove  → {ip}
   *
   * Vistas: Resumen · Eventos · Bloqueos · Whitelist.
   * Engine toggle en el pie del sidebar (slot sidebar-foot del AppShell).
   * Severidades: critical/high/medium/low. Categorías: auth/traversal/
   * injection/scan/honeypot/system. Timestamps RFC3339 (UTC).
   */
  import { onMount, onDestroy } from 'svelte';
  import AppShell from '$lib/components/AppShell.svelte';
  import { jsonHdrs as hdrs } from '$lib/stores/auth.js';

  let active = 'overview';
  let status = { enabled: false, blockedIPs: 0, honeypots: 0, rules: 0 };
  let events = [];
  let blocks = [];
  let whitelist = [];
  let adminRequired = false;
  let loading = true;
  let pollInterval = null;
  let tickInterval = null;
  let now = Date.now();          // tick 1s para countdowns
  let busy = new Set();          // IPs con acción en curso

  // ─── Carga ───
  async function get(path) {
    const r = await fetch('/api/shield/' + path, { headers: hdrs() });
    if (r.status === 403) { adminRequired = true; return null; }
    if (!r.ok) return null;
    return r.json();
  }
  async function post(path, body) {
    const r = await fetch('/api/shield/' + path, {
      method: 'POST', headers: hdrs(), body: body ? JSON.stringify(body) : undefined,
    });
    if (!r.ok) {
      let msg = 'Error';
      try { const e = await r.json(); if (e.error) msg = e.error; } catch {}
      throw new Error(msg);
    }
    return r.json();
  }

  async function refresh() {
    const [s, e, b, wl] = await Promise.all([
      get('status'), get('events?limit=200'), get('blocks'), get('whitelist'),
    ]);
    if (s) status = s;
    if (e) events = e.events || [];
    if (b) blocks = (b.blocks || []).sort((x, y) => (x.expiresAt < y.expiresAt ? -1 : 1));
    if (wl) whitelist = wl.whitelist || [];
    loading = false;
  }

  // ─── Engine toggle ───
  let togglingEngine = false;
  async function toggleEngine() {
    if (togglingEngine) return;
    togglingEngine = true;
    try {
      const r = await post('toggle');
      status = { ...status, enabled: r.enabled };
    } catch { /* sin permiso o error */ }
    togglingEngine = false;
  }

  // ─── Acciones bloqueos ───
  async function unblock(ip) {
    if (busy.has(ip)) return;
    busy = new Set(busy).add(ip);
    try { await post('unblock', { ip }); } catch {}
    busy.delete(ip); busy = new Set(busy);
    await refresh();
  }
  async function whitelistFromBlock(ip) {
    if (busy.has(ip)) return;
    busy = new Set(busy).add(ip);
    // El backend des-bloquea automáticamente al meter en whitelist
    try { await post('whitelist', { ip, note: 'añadida desde bloqueos' }); } catch {}
    busy.delete(ip); busy = new Set(busy);
    await refresh();
  }

  // ─── Whitelist form ───
  let wlIP = '';
  let wlNote = '';
  let wlError = '';
  async function addWhitelist() {
    const ip = wlIP.trim();
    if (!ip) return;
    wlError = '';
    try {
      await post('whitelist', { ip, note: wlNote.trim() });
      wlIP = ''; wlNote = '';
      await refresh();
    } catch (e) {
      wlError = e.message || 'No se pudo añadir';
    }
  }
  async function removeWhitelist(ip) {
    if (busy.has(ip)) return;
    busy = new Set(busy).add(ip);
    try { await post('whitelist/remove', { ip }); } catch {}
    busy.delete(ip); busy = new Set(busy);
    await refresh();
  }

  // ─── Filtros (vista eventos) ───
  let sevFilter = 'all';
  let catFilter = 'all';
  let ipSearch = '';
  $: filteredEvents = events.filter(ev =>
    (sevFilter === 'all' || ev.severity === sevFilter) &&
    (catFilter === 'all' || ev.category === catFilter) &&
    (!ipSearch.trim() || (ev.sourceIP || '').includes(ipSearch.trim()))
  );

  // ─── Derivados resumen ───
  // Severidad últimas 24h (med incluye low)
  $: last24h = events.filter(ev => {
    const t = Date.parse(ev.timestamp);
    return !isNaN(t) && (now - t) < 24 * 3600 * 1000;
  });
  $: sevCounts = {
    crit: last24h.filter(e => e.severity === 'critical').length,
    high: last24h.filter(e => e.severity === 'high').length,
    med:  last24h.filter(e => e.severity === 'medium' || e.severity === 'low').length,
  };
  $: sevMax = Math.max(sevCounts.crit, sevCounts.high, sevCounts.med, 1);
  // 5 eventos crit/high más recientes
  $: recentCritical = events.filter(e => e.severity === 'critical' || e.severity === 'high').slice(0, 5);

  // ─── Sidebar sections ───
  $: sections = [
    {
      label: 'Vista',
      items: [
        { id: 'overview',  label: 'Resumen' },
        { id: 'events',    label: 'Eventos',   badge: events.length || null },
        { id: 'blocks',    label: 'Bloqueos',  badge: blocks.length || null, badgeVariant: blocks.length ? 'crit' : 'default' },
        { id: 'whitelist', label: 'Whitelist', badge: whitelist.length || null },
      ],
    },
  ];

  const viewTitles = {
    overview:  { t: 'Resumen',   s: '· estado del motor de defensa' },
    events:    { t: 'Eventos',   s: '· stream del motor' },
    blocks:    { t: 'Bloqueos',  s: '· IPs bloqueadas activas' },
    whitelist: { t: 'Whitelist', s: '· IPs en confianza' },
  };

  // ─── Formateadores ───
  const sevClass = { critical: 'crit', high: 'high', medium: 'med', low: 'med' };
  const catShort = { auth: 'AUTH', traversal: 'TRAV', injection: 'INJ', scan: 'SCAN', honeypot: 'HONEY', system: 'SYS' };

  function fmtTime(ts) {
    const d = new Date(ts);
    if (isNaN(d)) return '—';
    return d.toLocaleTimeString('es-ES', { hour12: false });
  }
  // countdown "23m 14s" / "23h 41m" / "3d 2h"
  function fmtExpires(expiresAt) {
    const ms = Date.parse(expiresAt) - now;
    if (isNaN(ms) || ms <= 0) return 'expirado';
    const s = Math.floor(ms / 1000);
    if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
    if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
    return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
  }
  function expiresSoon(expiresAt) {
    const ms = Date.parse(expiresAt) - now;
    return !isNaN(ms) && ms > 0 && ms < 30 * 60 * 1000;
  }
  // "hace 12m" / "1h 22m" / "hace 12 días"
  function fmtAgo(createdAt, long = false) {
    const ms = now - Date.parse(createdAt);
    if (isNaN(ms) || ms < 0) return '—';
    const s = Math.floor(ms / 1000);
    if (s < 60) return long ? 'hace un momento' : `${s}s`;
    if (s < 3600) return (long ? 'hace ' : '') + `${Math.floor(s / 60)}m`;
    if (s < 86400) return (long ? 'hace ' : '') + `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
    const d = Math.floor(s / 86400);
    return long ? `hace ${d} día${d === 1 ? '' : 's'}` : `${d}d`;
  }

  onMount(async () => {
    await refresh();
    pollInterval = setInterval(refresh, 5000);
    tickInterval = setInterval(() => { now = Date.now(); }, 1000);
  });
  onDestroy(() => {
    if (pollInterval) clearInterval(pollInterval);
    if (tickInterval) clearInterval(tickInterval);
  });
</script>

<AppShell
  appId="nimshield"
  title="NimShield"
  {sections}
  bind:active
>
  <!-- ═══ ENGINE TOGGLE · pie del sidebar ═══ -->
  <svelte:fragment slot="sidebar-foot">
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
    <div
      class="engine-toggle"
      class:off={!status.enabled}
      on:click={toggleEngine}
      role="switch"
      aria-checked={status.enabled}
      tabindex="0"
      on:keydown={(e) => e.key === 'Enter' && toggleEngine()}
      title={status.enabled ? 'Desactivar NimShield' : 'Activar NimShield'}
    >
      <div class="engine-lbl">
        <span class="engine-led" class:off={!status.enabled}></span>
        <span>Engine</span>
      </div>
      <div class="toggle-switch" class:off={!status.enabled}></div>
    </div>
  </svelte:fragment>

  <!-- ═══ HEADER ═══ -->
  <svelte:fragment slot="page-header">
    <b>{viewTitles[active]?.t || 'NimShield'}</b>
    <span class="ns-sub">
      {#if active === 'events'}· stream del motor · {events.length} últimos
      {:else if active === 'blocks'}· {blocks.length} IPs bloqueadas activas
      {:else if active === 'whitelist'}· {whitelist.length + 1} IPs en confianza
      {:else}{viewTitles[active]?.s}
      {/if}
    </span>
  </svelte:fragment>

  <div class="ns-body">
    {#if loading}
      <div class="ns-msg">Cargando…</div>
    {:else if adminRequired}
      <div class="ns-msg">Se requiere rol de administrador para ver NimShield.</div>
    {:else if active === 'overview'}

      <!-- ═══════════════ RESUMEN ═══════════════ -->
      <div class="r-stats">
        <div class="r-stat" class:ok={status.enabled} class:crit={!status.enabled}>
          <div class="r-stat-head">
            <span class="r-stat-lbl">Engine</span>
            <span class="r-stat-tag" class:ok={status.enabled}>
              <span class="d"></span>{status.enabled ? 'activo' : 'parado'}
            </span>
          </div>
          <div class="r-stat-val mono" class:ok={status.enabled} class:crit={!status.enabled}>
            {status.enabled ? 'ON' : 'OFF'}
          </div>
        </div>
        <div class="r-stat" class:crit={blocks.length > 0}>
          <div class="r-stat-head">
            <span class="r-stat-lbl">IPs bloqueadas</span>
            <span class="r-stat-tag">activos</span>
          </div>
          <div class="r-stat-val mono">{status.blockedIPs ?? blocks.length}</div>
        </div>
        <div class="r-stat info">
          <div class="r-stat-head">
            <span class="r-stat-lbl">Honeypots</span>
            <span class="r-stat-tag info"><span class="d"></span>vigilando</span>
          </div>
          <div class="r-stat-val mono">{status.honeypots ?? 0}</div>
        </div>
        <div class="r-stat">
          <div class="r-stat-head">
            <span class="r-stat-lbl">Reglas</span>
            <span class="r-stat-tag">cargadas</span>
          </div>
          <div class="r-stat-val mono">{status.rules ?? 0}</div>
        </div>
      </div>

      <div class="r-sec">
        <span class="r-sec-lbl">eventos por severidad<span class="ac">· últimas 24h</span></span>
      </div>

      <div class="sev-bars">
        <div class="sev-row">
          <div class="sev-name"><span class="sev-led crit"></span>Crítico</div>
          <div class="sev-bar"><div class="sev-fill crit" style="width:{(sevCounts.crit / sevMax) * 100}%"></div></div>
          <div class="sev-count">{sevCounts.crit}</div>
        </div>
        <div class="sev-row">
          <div class="sev-name"><span class="sev-led high"></span>Alto</div>
          <div class="sev-bar"><div class="sev-fill high" style="width:{(sevCounts.high / sevMax) * 100}%"></div></div>
          <div class="sev-count">{sevCounts.high}</div>
        </div>
        <div class="sev-row">
          <div class="sev-name"><span class="sev-led med"></span>Medio</div>
          <div class="sev-bar"><div class="sev-fill med" style="width:{(sevCounts.med / sevMax) * 100}%"></div></div>
          <div class="sev-count">{sevCounts.med}</div>
        </div>
      </div>

      <div class="r-sec">
        <span class="r-sec-lbl">últimos eventos críticos<span class="ac">· {recentCritical.length} más recientes</span></span>
      </div>

      <div class="recent-events">
        {#if recentCritical.length === 0}
          <div class="ns-msg">Sin eventos críticos. Buena señal.</div>
        {:else}
          {#each recentCritical as ev (ev.id)}
            <div class="event-row">
              <span class="event-led {sevClass[ev.severity] || 'med'}"></span>
              <span class="event-time">{fmtTime(ev.timestamp)}</span>
              <span class="event-cat {ev.category}">{catShort[ev.category] || ev.category}</span>
              <span class="event-endpoint">{ev.endpoint || '—'}</span>
              <span class="event-ip">{ev.sourceIP || '—'}</span>
              <span class="event-rule">{ev.rule || '—'}</span>
            </div>
          {/each}
        {/if}
      </div>

    {:else if active === 'events'}

      <!-- ═══════════════ EVENTOS ═══════════════ -->
      <div class="filters">
        <div class="filter-group">
          <span class="filter-lbl">sev</span>
          <button class="pill" class:active={sevFilter === 'all'} on:click={() => sevFilter = 'all'}>Todos</button>
          <button class="pill crit" class:active={sevFilter === 'critical'} on:click={() => sevFilter = 'critical'}>Crítico</button>
          <button class="pill warn" class:active={sevFilter === 'high'} on:click={() => sevFilter = 'high'}>Alto</button>
          <button class="pill" class:active={sevFilter === 'medium'} on:click={() => sevFilter = 'medium'}>Medio</button>
        </div>
        <div class="filter-group">
          <span class="filter-lbl">cat</span>
          <button class="pill" class:active={catFilter === 'all'} on:click={() => catFilter = 'all'}>Todas</button>
          <button class="pill" class:active={catFilter === 'auth'} on:click={() => catFilter = 'auth'}>Auth</button>
          <button class="pill" class:active={catFilter === 'honeypot'} on:click={() => catFilter = 'honeypot'}>Honeypot</button>
          <button class="pill" class:active={catFilter === 'injection'} on:click={() => catFilter = 'injection'}>Injection</button>
          <button class="pill" class:active={catFilter === 'traversal'} on:click={() => catFilter = 'traversal'}>Traversal</button>
          <button class="pill" class:active={catFilter === 'scan'} on:click={() => catFilter = 'scan'}>Scan</button>
        </div>
        <div class="search-box">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
            <circle cx="11" cy="11" r="8"/>
            <line x1="21" y1="21" x2="16.65" y2="16.65"/>
          </svg>
          <input type="text" placeholder="filtrar por IP…" bind:value={ipSearch} />
        </div>
      </div>

      <div class="evt-table">
        <div class="evt-head">
          <span></span>
          <span>Timestamp</span>
          <span>Categoría</span>
          <span>IP origen</span>
          <span>Endpoint</span>
          <span>Method</span>
          <span>Regla</span>
        </div>
        {#if filteredEvents.length === 0}
          <div class="ns-msg">{events.length === 0 ? 'Sin eventos registrados.' : 'Ningún evento coincide con el filtro.'}</div>
        {:else}
          {#each filteredEvents as ev (ev.id)}
            <div class="evt-row">
              <span class="event-led {sevClass[ev.severity] || 'med'}"></span>
              <span class="event-time">{fmtTime(ev.timestamp)}</span>
              <span class="event-cat {ev.category}">{catShort[ev.category] || ev.category}</span>
              <span class="event-ip">{ev.sourceIP || '—'}</span>
              <span class="event-endpoint">{ev.endpoint || '—'}</span>
              <span class="evt-method">{ev.method || '—'}</span>
              <span class="event-rule">{ev.rule || '—'}</span>
            </div>
          {/each}
        {/if}
      </div>

    {:else if active === 'blocks'}

      <!-- ═══════════════ BLOQUEOS ═══════════════ -->
      <div class="block-table">
        <div class="block-head">
          <span>IP origen</span>
          <span>Motivo</span>
          <span>Regla</span>
          <span>Expira en</span>
          <span>Hace</span>
          <span style="text-align:right">Acción</span>
        </div>
        {#if blocks.length === 0}
          <div class="ns-msg">No hay IPs bloqueadas. Todo tranquilo.</div>
        {:else}
          {#each blocks as b (b.ip)}
            <div class="block-row">
              <span class="block-ip">{b.ip}</span>
              <span class="block-reason" title={b.reason}>{b.reason || '—'}</span>
              <span class="block-rule">{b.rule || '—'}</span>
              <span class="block-expires" class:short={expiresSoon(b.expiresAt)}>{fmtExpires(b.expiresAt)}</span>
              <span class="block-created">{fmtAgo(b.createdAt)}</span>
              <div class="block-actions" style="justify-content:flex-end">
                <button class="icon-btn ok" title="Añadir a whitelist (desbloquea)" disabled={busy.has(b.ip)} on:click={() => whitelistFromBlock(b.ip)}>
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <polyline points="20 6 9 17 4 12"/>
                  </svg>
                </button>
                <button class="icon-btn" title="Desbloquear ahora" disabled={busy.has(b.ip)} on:click={() => unblock(b.ip)}>
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <rect x="3" y="11" width="18" height="11" rx="2" ry="2"/>
                    <path d="M7 11V7a5 5 0 0 1 9.9-1"/>
                  </svg>
                </button>
              </div>
            </div>
          {/each}
        {/if}
      </div>

    {:else if active === 'whitelist'}

      <!-- ═══════════════ WHITELIST ═══════════════ -->
      <div class="wl-form">
        <input type="text" class="ip" placeholder="192.168.1.100" bind:value={wlIP} on:keydown={(e) => e.key === 'Enter' && addWhitelist()} />
        <input type="text" class="note" placeholder="Nota (ej: ordenador personal)" bind:value={wlNote} on:keydown={(e) => e.key === 'Enter' && addWhitelist()} />
        <button class="btn-add" on:click={addWhitelist}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round">
            <line x1="12" y1="5" x2="12" y2="19"/>
            <line x1="5" y1="12" x2="19" y2="12"/>
          </svg>
          Añadir
        </button>
      </div>
      {#if wlError}
        <div class="ns-err">{wlError}</div>
      {/if}

      <div class="wl-table">
        <div class="wl-head">
          <span>IP</span>
          <span>Nota</span>
          <span>Añadida</span>
          <span style="text-align:right">Acción</span>
        </div>

        <!-- Loopback fija · el backend rechaza quitarla -->
        <div class="wl-row loopback">
          <span class="block-ip">127.0.0.1</span>
          <span class="wl-note">loopback (no removible)</span>
          <span class="wl-loopback-tag">system</span>
          <div></div>
        </div>

        {#each whitelist as wl (wl.ip)}
          <div class="wl-row">
            <span class="block-ip">{wl.ip}</span>
            <span class="wl-note">{wl.note || '—'}</span>
            <span class="block-created">{fmtAgo(wl.created_at, true)}</span>
            <div class="block-actions" style="justify-content:flex-end">
              <button class="icon-btn danger" title="Quitar de whitelist" disabled={busy.has(wl.ip)} on:click={() => removeWhitelist(wl.ip)}>
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                  <polyline points="3 6 5 6 21 6"/>
                  <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>
                </svg>
              </button>
            </div>
          </div>
        {/each}
      </div>

    {/if}
  </div>
</AppShell>

<style>
  .ns-sub { color: var(--fg-4, #7a7a82); font-size: 12px; font-weight: 400; }
  .ns-msg {
    padding: 24px;
    text-align: center;
    color: var(--fg-5, #5a5a62);
    font-size: 12px;
    font-family: var(--font-mono);
  }
  .ns-err {
    margin: -6px 0 12px;
    font-size: 11px;
    color: var(--st-crit, #ff5a5a);
    font-family: var(--font-mono);
  }
  .mono { font-family: var(--font-mono); }

  /* ═══ ENGINE TOGGLE (sidebar foot) ═══ */
  .engine-toggle {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 8px 10px;
    background: rgba(0, 255, 159, 0.05);
    border: 1px solid rgba(0, 255, 159, 0.18);
    border-radius: 6px;
    cursor: pointer;
    transition: background 0.15s, border-color 0.15s;
  }
  .engine-toggle.off {
    background: rgba(255, 90, 90, 0.04);
    border-color: rgba(255, 90, 90, 0.18);
  }
  .engine-lbl {
    display: flex;
    align-items: center;
    gap: 7px;
    font-size: 11px;
    color: var(--fg-2, #d0d0d4);
    font-weight: 500;
  }
  .engine-led {
    width: 7px;
    height: 7px;
    border-radius: 1.5px;
    background: var(--st-ok, #00ff9f);
    box-shadow: 0 0 5px rgba(0, 255, 159, 0.4);
    animation: pulse 2.5s ease-in-out infinite;
  }
  .engine-led.off {
    background: var(--st-crit, #ff5a5a);
    box-shadow: 0 0 5px rgba(255, 90, 90, 0.4);
    animation: none;
  }
  @keyframes pulse {
    0%, 100% { opacity: 1; }
    50%      { opacity: 0.55; }
  }
  .toggle-switch {
    width: 28px;
    height: 16px;
    background: var(--nim-green, #00ff9f);
    border-radius: 3px;
    position: relative;
    transition: background 0.15s;
    flex-shrink: 0;
  }
  .toggle-switch::after {
    content: '';
    position: absolute;
    top: 2px;
    right: 2px;
    width: 12px;
    height: 12px;
    background: var(--bg-window, #16161a);
    border-radius: 2px;
    transition: right 0.15s, left 0.15s;
  }
  .toggle-switch.off {
    background: var(--bd-3, #2a2a32);
  }
  .toggle-switch.off::after {
    right: 14px;
  }

  /* ═══ STAT CARDS ═══ */
  .r-stats {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 8px;
  }
  .r-stat {
    background: var(--bg-card, #15151a);
    border-radius: 8px;
    padding: 12px 12px 11px;
    display: flex;
    flex-direction: column;
    position: relative;
    overflow: hidden;
  }
  .r-stat::before {
    content: '';
    position: absolute;
    top: 0;
    left: 0;
    width: 2px;
    height: 100%;
    background: var(--stat-edge, transparent);
    opacity: 0.7;
  }
  .r-stat.ok { --stat-edge: var(--st-ok, #00ff9f); }
  .r-stat.info { --stat-edge: var(--st-info, #4db8ff); }
  .r-stat.crit { --stat-edge: var(--st-crit, #ff5a5a); }
  .r-stat-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 8px;
  }
  .r-stat-lbl {
    font-size: 10px;
    color: var(--fg-4, #7a7a82);
    font-weight: 500;
    letter-spacing: 0.6px;
    text-transform: uppercase;
  }
  .r-stat-tag {
    font-size: 9px;
    color: var(--fg-4, #7a7a82);
    display: flex;
    align-items: center;
    gap: 4px;
    font-family: var(--font-mono);
  }
  .r-stat-tag .d {
    width: 5px;
    height: 5px;
    border-radius: 1.5px;
    background: var(--fg-4, #7a7a82);
  }
  .r-stat-tag.ok { color: var(--st-ok, #00ff9f); }
  .r-stat-tag.ok .d { background: var(--st-ok, #00ff9f); }
  .r-stat-tag.info { color: var(--st-info, #4db8ff); }
  .r-stat-tag.info .d { background: var(--st-info, #4db8ff); }
  .r-stat-val {
    font-size: 22px;
    font-weight: 500;
    color: var(--fg, #f0f0f0);
    line-height: 1;
    letter-spacing: -0.4px;
    font-family: var(--font-mono);
  }
  .r-stat-val.ok { color: var(--st-ok, #00ff9f); }
  .r-stat-val.crit { color: var(--st-crit, #ff5a5a); }

  /* ═══ SECTION TITLES ═══ */
  .r-sec {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-top: 24px;
    margin-bottom: 12px;
    flex-wrap: wrap;
    gap: 8px;
  }
  .r-sec-lbl {
    font-size: 11px;
    color: var(--fg-4, #7a7a82);
    font-weight: 500;
    letter-spacing: 0.6px;
    font-family: var(--font-mono);
    text-transform: uppercase;
  }
  .r-sec-lbl .ac {
    color: var(--fg-2, #d0d0d4);
    margin-left: 4px;
  }

  /* ═══ SEVERITY BARS ═══ */
  .sev-bars {
    background: var(--bg-card, #15151a);
    border-radius: 10px;
    padding: 16px 18px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .sev-row {
    display: grid;
    grid-template-columns: 90px 1fr 50px;
    gap: 12px;
    align-items: center;
    font-size: 11px;
  }
  .sev-name {
    display: flex;
    align-items: center;
    gap: 8px;
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.6px;
    color: var(--fg-3, #9c9ca4);
    font-size: 10px;
  }
  .sev-led { width: 8px; height: 8px; border-radius: 1.5px; }
  .sev-led.crit { background: var(--st-crit, #ff5a5a); }
  .sev-led.high { background: var(--st-warn, #ffc857); }
  .sev-led.med  { background: var(--st-info, #4db8ff); }
  .sev-bar {
    height: 6px;
    background: var(--bd-2, #20202a);
    border-radius: 2px;
    overflow: hidden;
  }
  .sev-fill { height: 100%; border-radius: 2px; transition: width 0.3s; }
  .sev-fill.crit { background: var(--st-crit, #ff5a5a); }
  .sev-fill.high { background: var(--st-warn, #ffc857); }
  .sev-fill.med  { background: var(--st-info, #4db8ff); }
  .sev-count {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg, #f0f0f0);
    text-align: right;
    font-variant-numeric: tabular-nums;
  }

  /* ═══ EVENTOS (compartido resumen + tabla) ═══ */
  .recent-events {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .event-row {
    background: var(--bg-card, #15151a);
    border-radius: 7px;
    padding: 10px 14px;
    display: grid;
    grid-template-columns: 8px 80px 80px 1fr auto auto;
    gap: 10px;
    align-items: center;
    font-size: 11px;
  }
  .event-led { width: 8px; height: 8px; border-radius: 1.5px; }
  .event-led.crit { background: var(--st-crit, #ff5a5a); }
  .event-led.high { background: var(--st-warn, #ffc857); }
  .event-led.med  { background: var(--st-info, #4db8ff); }
  .event-time {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-4, #7a7a82);
    font-variant-numeric: tabular-nums;
  }
  .event-cat {
    font-family: var(--font-mono);
    font-size: 9px;
    letter-spacing: 0.5px;
    text-transform: uppercase;
    font-weight: 600;
    padding: 2px 7px;
    border-radius: 3px;
    text-align: center;
  }
  .event-cat.auth      { background: rgba(255,200,87,0.10); color: var(--st-warn, #ffc857); }
  .event-cat.traversal { background: rgba(255,90,90,0.10); color: var(--st-crit, #ff5a5a); }
  .event-cat.injection { background: rgba(255,90,90,0.10); color: var(--st-crit, #ff5a5a); }
  .event-cat.scan      { background: rgba(77,184,255,0.10); color: var(--st-info, #4db8ff); }
  .event-cat.honeypot  { background: rgba(255,156,90,0.10); color: var(--nim-folder, #ff9c5a); }
  .event-cat.system    { background: rgba(122,158,177,0.10); color: var(--ui-select, #7a9eb1); }
  .event-endpoint {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-2, #d0d0d4);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .event-ip {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-3, #9c9ca4);
    font-variant-numeric: tabular-nums;
  }
  .event-rule {
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--fg-4, #7a7a82);
    padding: 1px 6px;
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 3px;
    letter-spacing: 0.4px;
    text-align: center;
  }

  /* ═══ FILTROS ═══ */
  .filters {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 14px;
    flex-wrap: wrap;
  }
  .filter-group { display: flex; gap: 4px; align-items: center; }
  .filter-lbl {
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--fg-5, #5a5a62);
    text-transform: uppercase;
    letter-spacing: 0.8px;
    margin-right: 4px;
  }
  .pill {
    padding: 4px 9px;
    background: transparent;
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 4px;
    color: var(--fg-3, #9c9ca4);
    font-size: 10px;
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.4px;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .pill:hover { color: var(--fg, #f0f0f0); border-color: var(--bd-3, #2a2a32); }
  .pill.active {
    color: var(--nim-green, #00ff9f);
    border-color: rgba(0,255,159,0.35);
    background: rgba(0,255,159,0.06);
  }
  .pill.crit.active { color: var(--st-crit, #ff5a5a); border-color: rgba(255,90,90,0.35); background: rgba(255,90,90,0.06); }
  .pill.warn.active { color: var(--st-warn, #ffc857); border-color: rgba(255,200,87,0.35); background: rgba(255,200,87,0.06); }

  .search-box {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 5px;
    background: var(--bg-inner, #101015);
    margin-left: auto;
    width: 200px;
  }
  .search-box svg { width: 11px; height: 11px; color: var(--fg-4, #7a7a82); flex-shrink: 0; }
  .search-box input {
    flex: 1;
    min-width: 0;
    background: transparent;
    border: none;
    color: var(--fg, #f0f0f0);
    outline: none;
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .search-box input::placeholder { color: var(--fg-5, #5a5a62); }

  /* ═══ TABLA EVENTOS ═══ */
  .evt-table {
    background: var(--bg-card, #15151a);
    border-radius: 8px;
    overflow: hidden;
  }
  .evt-head {
    display: grid;
    grid-template-columns: 14px 95px 90px 130px 1fr 60px 70px;
    gap: 12px;
    padding: 9px 14px;
    background: var(--bg-inner, #101015);
    border-bottom: 1px solid var(--bd-2, #20202a);
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--fg-5, #5a5a62);
    letter-spacing: 0.8px;
    text-transform: uppercase;
    font-weight: 600;
  }
  .evt-row {
    display: grid;
    grid-template-columns: 14px 95px 90px 130px 1fr 60px 70px;
    gap: 12px;
    padding: 8px 14px;
    align-items: center;
    font-size: 11px;
    transition: background 0.1s;
  }
  .evt-row + .evt-row { border-top: 1px solid #1a1a20; }
  .evt-row:hover { background: rgba(255,255,255,0.015); }
  .evt-method {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-4, #7a7a82);
  }

  /* ═══ TABLA BLOQUEOS ═══ */
  .block-table {
    background: var(--bg-card, #15151a);
    border-radius: 8px;
    overflow: hidden;
  }
  .block-head {
    display: grid;
    grid-template-columns: 120px 1fr 80px 90px 90px 100px;
    gap: 12px;
    padding: 9px 14px;
    background: var(--bg-inner, #101015);
    border-bottom: 1px solid var(--bd-2, #20202a);
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--fg-5, #5a5a62);
    letter-spacing: 0.8px;
    text-transform: uppercase;
    font-weight: 600;
  }
  .block-row {
    display: grid;
    grid-template-columns: 120px 1fr 80px 90px 90px 100px;
    gap: 12px;
    padding: 11px 14px;
    align-items: center;
    font-size: 11px;
  }
  .block-row + .block-row { border-top: 1px solid #1a1a20; }
  .block-ip {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg, #f0f0f0);
    font-variant-numeric: tabular-nums;
    font-weight: 500;
  }
  .block-reason {
    font-size: 11px;
    color: var(--fg-3, #9c9ca4);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .block-rule {
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--fg-3, #9c9ca4);
    padding: 1px 6px;
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 3px;
    letter-spacing: 0.4px;
    text-align: center;
  }
  .block-expires {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--st-warn, #ffc857);
    font-variant-numeric: tabular-nums;
  }
  .block-expires.short { color: var(--st-crit, #ff5a5a); }
  .block-created {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-4, #7a7a82);
    font-variant-numeric: tabular-nums;
  }
  .block-actions { display: flex; gap: 4px; }
  .icon-btn {
    width: 26px;
    height: 26px;
    background: transparent;
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 4px;
    color: var(--fg-3, #9c9ca4);
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0;
  }
  .icon-btn:hover:not(:disabled) { color: var(--fg, #f0f0f0); border-color: var(--bd-3, #2a2a32); }
  .icon-btn.ok:hover:not(:disabled) { color: var(--st-ok, #00ff9f); border-color: rgba(0,255,159,0.3); }
  .icon-btn.danger:hover:not(:disabled) { color: var(--st-crit, #ff5a5a); border-color: rgba(255,90,90,0.3); }
  .icon-btn:disabled { opacity: 0.4; cursor: default; }
  .icon-btn svg { width: 11px; height: 11px; pointer-events: none; }

  /* ═══ WHITELIST ═══ */
  .wl-form {
    display: flex;
    gap: 6px;
    margin-bottom: 14px;
    background: var(--bg-card, #15151a);
    padding: 10px;
    border-radius: 8px;
  }
  .wl-form input {
    background: var(--bg-inner, #101015);
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 5px;
    padding: 7px 10px;
    color: var(--fg, #f0f0f0);
    font-family: var(--font-mono);
    font-size: 11px;
    outline: none;
  }
  .wl-form input:focus { border-color: rgba(0,255,159,0.35); }
  .wl-form input.ip { width: 140px; }
  .wl-form input.note { flex: 1; font-family: inherit; }
  .btn-add {
    padding: 0 16px;
    background: var(--nim-green, #00ff9f);
    color: var(--bg-window, #16161a);
    border: none;
    border-radius: 5px;
    font-family: var(--font-mono);
    font-size: 10px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.6px;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 5px;
  }
  .btn-add:hover { filter: brightness(1.08); }
  .btn-add svg { width: 11px; height: 11px; pointer-events: none; }

  .wl-table {
    background: var(--bg-card, #15151a);
    border-radius: 8px;
    overflow: hidden;
  }
  .wl-head {
    display: grid;
    grid-template-columns: 140px 1fr 110px 60px;
    gap: 12px;
    padding: 9px 14px;
    background: var(--bg-inner, #101015);
    border-bottom: 1px solid var(--bd-2, #20202a);
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--fg-5, #5a5a62);
    letter-spacing: 0.8px;
    text-transform: uppercase;
    font-weight: 600;
  }
  .wl-row {
    display: grid;
    grid-template-columns: 140px 1fr 110px 60px;
    gap: 12px;
    padding: 11px 14px;
    align-items: center;
    font-size: 11px;
  }
  .wl-row + .wl-row { border-top: 1px solid #1a1a20; }
  .wl-note {
    font-size: 11px;
    color: var(--fg-3, #9c9ca4);
    font-style: italic;
  }
  .wl-row.loopback { background: rgba(0, 255, 159, 0.03); }
  .wl-loopback-tag {
    font-family: var(--font-mono);
    font-size: 8px;
    color: var(--nim-green, #00ff9f);
    padding: 1px 5px;
    border: 1px solid rgba(0,255,159,0.3);
    border-radius: 3px;
    letter-spacing: 0.5px;
    text-transform: uppercase;
    justify-self: start;
  }
</style>
