<script>
  /**
   * NimHealth · Task Manager + System Health (v3)
   * ─────────────────────────────────────────────────
   * Primera migración real Beta 7 → Beta 8 (preservada en Beta 8.1).
   *
   * Cambios respecto a Beta 7:
   *   - Usa AppShell (titlebar + sidebar con secciones)
   *   - KPIs arriba con primitiva KPICard (corner brackets + sparkline embebida)
   *   - Tabla con DenseTable (antes: tabla custom)
   *   - Filtros con Tab (antes: chips custom)
   *   - Detalle con CmdOutputLog para logs estilo journalctl
   *   - Acciones con BevelButton (antes: botones ad-hoc)
   *   - LED para estados de servicio
   *   - Todo el CSS de Beta 7 eliminado (~180 líneas menos)
   *
   * Backend: sin cambios. Mismos endpoints:
   *   GET  /api/services
   *   GET  /api/hardware/stats
   *   GET  /api/services/{id}/logs?n=50
   *   POST /api/services/{id}/start|stop|restart
   */
  import { onMount, onDestroy } from 'svelte';
  import { token, hdrs } from '$lib/stores/auth.js';
  import { APP_META } from '$lib/apps.js';
  import AppShell from '$lib/components/AppShell.svelte';
  import {
    KPICard, DenseTable, LED, SectionHead, BevelButton,
    IconButton, TextInput, Tab, Badge, CmdOutputLog, EmptyState
  } from '$lib/ui';

  // ─── State ───
  let active = 'tasks';      // 'tasks' | 'system'
  let view = 'dashboard';     // 'dashboard' | 'detail'
  let services = [];
  let selectedService = null;
  let filter = 'all';
  let search = '';
  let stopping = {};

  // Hardware metrics
  let cpu = { percent: 0, cores: 0, load: 0 };
  let ram = { used: 0, total: 0, percent: 0 };
  let diskIO = { read: 0, write: 0 };
  let netIO = { rx: 0, tx: 0 };
  let cpuHistory = Array(16).fill(0);
  let ramHistory = Array(16).fill(0);
  let diskHistory = Array(16).fill(0);
  let netHistory = Array(16).fill(0);

  let detailLogs = [];
  let pollInterval;

  // ─── API ───
  async function loadServices() {
    try {
      const r = await fetch('/api/services', { headers: hdrs() });
      const d = await r.json();
      const raw = d.services || [];
      let flat = [];
      for (const svc of raw) {
        flat.push(svc);
        if (svc.children && svc.children.length > 0) {
          for (const child of svc.children) {
            flat.push({
              ...child,
              _isChild: true,
              _parentId: svc.id,
              poolName: svc.poolName,
              owner: svc.owner,
            });
          }
        }
      }
      services = flat;
    } catch { services = []; }
  }

  async function loadMetrics() {
    try {
      const r = await fetch('/api/hardware/stats', { headers: hdrs() });
      const d = await r.json();
      if (d.cpu) {
        cpu = {
          percent: Math.round(d.cpu.percent || 0),
          cores: d.cpu.cores || 0,
          load: (d.cpu.load1 || 0).toFixed(2),
        };
        cpuHistory = [...cpuHistory.slice(1), cpu.percent];
      }
      if (d.memory) {
        ram = {
          used: d.memory.used || 0,
          total: d.memory.total || 0,
          percent: Math.round((d.memory.used / d.memory.total) * 100) || 0,
        };
        ramHistory = [...ramHistory.slice(1), ram.percent];
      }
      if (d.disk) {
        diskIO = { read: d.disk.readSpeed || 0, write: d.disk.writeSpeed || 0 };
        diskHistory = [...diskHistory.slice(1), Math.min(100, Math.round((diskIO.read + diskIO.write) / 1048576))];
      }
      if (d.network) {
        netIO = { rx: d.network.rxSpeed || 0, tx: d.network.txSpeed || 0 };
        netHistory = [...netHistory.slice(1), Math.min(100, Math.round((netIO.rx + netIO.tx) / 1048576))];
      }
    } catch {}
  }

  async function loadLogs(svc) {
    try {
      const r = await fetch(`/api/services/${svc.id}/logs?n=50`, { headers: hdrs() });
      const d = await r.json();
      detailLogs = (d.logs || []).map(l => ({
        ts: (l.timestamp || '').slice(0, 19).replace('T', ' '),
        level: inferLogLevel(l.message),
        msg: l.message,
      }));
    } catch { detailLogs = []; }
  }

  function inferLogLevel(msg) {
    if (!msg) return 'info';
    const m = msg.toLowerCase();
    if (m.includes('error') || m.includes('fail') || m.includes('exit')) return 'err';
    if (m.includes('warn') || m.includes('warning')) return 'warn';
    if (m.includes('started') || m.includes('ready') || m.includes('listening')) return 'ok';
    return 'info';
  }

  async function loadDetail(svc) {
    selectedService = svc;
    view = 'detail';
    detailLogs = [];
    await loadLogs(svc);
  }

  function goBack() {
    view = 'dashboard';
    selectedService = null;
    detailLogs = [];
  }

  async function doAction(svc, action) {
    const key = svc.id + ':' + action;
    stopping = { ...stopping, [key]: true };
    try {
      await fetch(`/api/services/${svc.id}/${action}`, { method: 'POST', headers: hdrs() });
      await loadServices();
      if (selectedService?.id === svc.id) {
        selectedService = services.find(s => s.id === svc.id) || selectedService;
        await loadLogs(selectedService);
      }
    } catch {}
    stopping = { ...stopping, [key]: false };
  }

  // ─── Helpers (heredados de Beta 7) ───
  function fmtBytes(b) {
    if (!b || b === 0) return '0 B';
    if (b >= 1e12) return (b / 1e12).toFixed(1) + ' TB';
    if (b >= 1e9)  return (b / 1e9).toFixed(1)  + ' GB';
    if (b >= 1e6)  return (b / 1e6).toFixed(1)  + ' MB';
    if (b >= 1e3)  return (b / 1e3).toFixed(0)  + ' KB';
    return b + ' B';
  }

  function fmtSpeed(b) {
    if (!b) return '0';
    if (b >= 1e6) return (b / 1e6).toFixed(1);
    if (b >= 1e3) return (b / 1e3).toFixed(1);
    return '0';
  }

  function fmtUptime(svc) {
    if (svc.status !== 'running') return '—';
    if (svc.uptime) return svc.uptime;
    if (!svc.startedAt) return '—';
    const ms = Date.now() - new Date(svc.startedAt).getTime();
    const h = Math.floor(ms / 3600000);
    if (h >= 24) return Math.floor(h / 24) + 'd ' + (h % 24).toString().padStart(2, '0') + 'h';
    return h + 'h ' + Math.floor((ms % 3600000) / 60000).toString().padStart(2, '0') + 'm';
  }

  function svcDisplayName(svc) {
    return svc.name || svc.appName || svc.appId || '?';
  }

  function svcIcon(svc) {
    if (svc.icon) return svc.icon;
    const appId = svc.appId || svc.id?.split('@')[0] || '';
    if (APP_META[appId]?.icon) return APP_META[appId].icon;
    if (appId === 'containers') return '/icons/containers.png';
    return '';
  }

  function svcVersion(svc) {
    if (svc.image) { const parts = svc.image.split(':'); return parts[1] || ''; }
    if (svc.containerImage) { const parts = svc.containerImage.split(':'); return parts[1] || ''; }
    return '';
  }

  function statusLedVariant(status) {
    switch (status) {
      case 'running':  return 'ok';
      case 'error':
      case 'failed':   return 'crit';
      case 'stopped':  return 'off';
      case 'starting':
      case 'stopping': return 'warn';
      default:         return 'off';
    }
  }

  // ─── Derived ───
  $: filteredServices = services.filter(s => {
    const mf = filter === 'all'
      ? true
      : filter === 'running'  ? s.status === 'running'
      : filter === 'stopped'  ? s.status === 'stopped'
      : filter === 'error'    ? (s.status === 'error' || s.status === 'failed')
      : true;
    const name = (s.name || s.appName || s.appId || '').toLowerCase();
    const matchSearch = !search || name.includes(search.toLowerCase());
    return mf && matchSearch;
  });

  $: runningCount  = services.filter(s => s.status === 'running').length;
  $: stoppedCount  = services.filter(s => s.status === 'stopped').length;
  $: errorCount    = services.filter(s => s.status === 'error' || s.status === 'failed').length;

  $: cpuVariant  = cpu.percent > 80 ? 'crit' : cpu.percent > 50 ? 'warn' : 'ok';
  $: ramVariant  = ram.percent > 85 ? 'crit' : ram.percent > 65 ? 'warn' : 'ok';

  // ─── Lifecycle ───
  onMount(async () => {
    let attempts = 0;
    while (!$token && attempts < 10) { await new Promise(r => setTimeout(r, 200)); attempts++; }
    await loadServices();
    await loadMetrics();
    pollInterval = setInterval(() => { loadServices(); loadMetrics(); }, 5000);
  });

  onDestroy(() => {
    if (pollInterval) clearInterval(pollInterval);
  });
</script>

<AppShell
  appId="nimhealth"
  title="NimHealth"
  headerIcon="♥"
  pathSegments={view === 'detail' && selectedService
    ? ['health', 'services', selectedService.id]
    : ['health', active]}
  sections={[
    {
      label: 'Monitor',
      items: [
        { id: 'tasks',  label: 'Task Manager', keyHint: 'T', badge: services.length, badgeVariant: 'default' },
        { id: 'system', label: 'Sistema',      keyHint: 'S' },
      ],
    },
  ]}
  bind:active
>

  <!-- ═══ DASHBOARD ═══ -->
  {#if view === 'dashboard'}

    <!-- KPIs row -->
    <div class="nh-kpis">
      <KPICard
        label="CPU"
        value={cpu.percent}
        unit="% · {cpu.cores}c · load {cpu.load}"
        state={cpuVariant === 'ok' ? 'nominal' : cpuVariant === 'warn' ? 'high' : 'critical'}
        stateVariant={cpuVariant}
        valueVariant={cpuVariant === 'crit' ? 'crit' : cpuVariant === 'warn' ? 'warn' : 'accent'}
        sparkData={cpuHistory}
        sparkVariant={cpuVariant === 'crit' ? 'crit' : cpuVariant === 'warn' ? 'warn' : 'accent'}
        sparkFilled={true}
        bracketVariant={cpuVariant === 'crit' ? 'crit' : cpuVariant === 'warn' ? 'warn' : 'accent'}
      />
      <KPICard
        label="Memoria"
        value={fmtBytes(ram.used)}
        unit="/ {fmtBytes(ram.total)} · {ram.percent}%"
        state={ramVariant === 'ok' ? 'ok' : ramVariant === 'warn' ? 'high' : 'critical'}
        stateVariant={ramVariant}
        valueVariant={ramVariant === 'crit' ? 'crit' : ramVariant === 'warn' ? 'warn' : 'default'}
        sparkData={ramHistory}
        sparkVariant={ramVariant === 'crit' ? 'crit' : ramVariant === 'warn' ? 'warn' : 'info'}
        bracketVariant={ramVariant === 'crit' ? 'crit' : ramVariant === 'warn' ? 'warn' : 'info'}
      />
      <KPICard
        label="Disco I/O"
        value={fmtSpeed(diskIO.read + diskIO.write)}
        unit="MB/s · ↓{fmtSpeed(diskIO.read)} ↑{fmtSpeed(diskIO.write)}"
        state="active"
        stateVariant="ok"
        valueVariant="info"
        sparkData={diskHistory}
        sparkVariant="info"
        sparkFilled={true}
        bracketVariant="info"
      />
      <KPICard
        label="Red"
        value={fmtSpeed(netIO.rx + netIO.tx)}
        unit="MB/s · ↓{fmtSpeed(netIO.rx)} ↑{fmtSpeed(netIO.tx)}"
        state="online"
        stateVariant="ok"
        sparkData={netHistory}
        sparkVariant="dim"
      />
    </div>

    <!-- Toolbar: tabs de filtro + búsqueda -->
    <div class="nh-toolbar">
      <div class="filter-tabs">
        <Tab active={filter === 'all'}     onClick={() => filter = 'all'}>
          Todos <Badge size="sm">{services.length}</Badge>
        </Tab>
        <Tab active={filter === 'running'} onClick={() => filter = 'running'}>
          Activos <Badge size="sm" variant="accent">{runningCount}</Badge>
        </Tab>
        <Tab active={filter === 'stopped'} onClick={() => filter = 'stopped'}>
          Detenidos <Badge size="sm">{stoppedCount}</Badge>
        </Tab>
        <Tab active={filter === 'error'}   onClick={() => filter = 'error'} hasError={errorCount > 0}>
          Errores
          {#if errorCount > 0}<Badge size="sm" variant="crit">{errorCount}</Badge>{/if}
        </Tab>
      </div>

      <div class="tb-right">
        <div style="width:200px">
          <TextInput
            bind:value={search}
            placeholder="Buscar servicio..."
            icon="⌕"
            keyHint="/"
            size="sm"
          />
        </div>
      </div>
    </div>

    <!-- Tabla de servicios -->
    <div class="nh-table-wrap">
      {#if filteredServices.length === 0}
        <EmptyState
          icon="◌"
          title={search ? 'Sin resultados' : 'Sin servicios'}
          hint={search ? `Nada coincide con "${search}"` : 'No hay servicios registrados en el sistema'}
        />
      {:else}
        <DenseTable
          columns="32px 1fr 110px 90px 110px 110px 78px"
          headers={[
            { label: '#' },
            { label: 'Servicio' },
            { label: 'Estado' },
            { label: 'CPU', align: 'right' },
            { label: 'Mem', align: 'right' },
            { label: 'Uptime' },
            { label: 'Acciones' },
          ]}
        >
          {#each filteredServices as svc, i}
            <div
              class="tr-row"
              class:selected={selectedService?.id === svc.id}
              class:crit-row={svc.status === 'error' || svc.status === 'failed'}
              class:muted={svc.status === 'stopped'}
              on:click={() => loadDetail(svc)}
              on:keydown={(e) => e.key === 'Enter' && loadDetail(svc)}
              role="button"
              tabindex="0"
            >
              <div class="tr-ln">{String(i + 1).padStart(2, '0')}</div>

              <div class="svc-cell">
                {#if svcIcon(svc)}
                  <img class="svc-icon" src={svcIcon(svc)} alt="" on:error={(e) => e.target.style.display = 'none'} />
                {:else}
                  <div class="svc-fallback">{svcDisplayName(svc).slice(0, 2).toUpperCase()}</div>
                {/if}
                <span class="svc-name">{svcDisplayName(svc)}</span>
                {#if svcVersion(svc)}
                  <span class="svc-ver">{svcVersion(svc)}</span>
                {/if}
                {#if svc._isChild}
                  <Badge size="sm" variant="info">docker</Badge>
                {/if}
              </div>

              <div class="svc-state">
                <LED size={6} variant={statusLedVariant(svc.status)} />
                <span>{svc.status}</span>
              </div>

              <div
                class="svc-num"
                class:warn={svc.cpuPercent > 50}
                class:crit={svc.cpuPercent > 80}
              >
                {#if svc.status === 'running'}
                  {(svc.cpuPercent || 0).toFixed(1)}<span class="dim">%</span>
                {:else}
                  <span class="dim">—</span>
                {/if}
              </div>

              <div class="svc-num">
                {#if svc.status === 'running'}
                  {fmtBytes(svc.memoryUsage || 0)}
                {:else}
                  <span class="dim">—</span>
                {/if}
              </div>

              <div class="svc-num">{fmtUptime(svc)}</div>

              <div class="svc-actions" on:click|stopPropagation role="presentation">
                {#if svc.status === 'running' || svc.status === 'starting'}
                  <IconButton
                    variant="danger"
                    size="sm"
                    title="Detener"
                    disabled={stopping[svc.id + ':stop']}
                    onClick={() => doAction(svc, 'stop')}
                  >■</IconButton>
                  <IconButton
                    size="sm"
                    title="Reiniciar"
                    disabled={stopping[svc.id + ':restart']}
                    onClick={() => doAction(svc, 'restart')}
                  >↻</IconButton>
                {:else}
                  <IconButton
                    size="sm"
                    title="Iniciar"
                    disabled={stopping[svc.id + ':start'] || svc.status === 'error'}
                    onClick={() => doAction(svc, 'start')}
                  >▸</IconButton>
                {/if}
              </div>
            </div>
          {/each}
        </DenseTable>
      {/if}
    </div>

  {/if}

  <!-- ═══ DETAIL ═══ -->
  {#if view === 'detail' && selectedService}

    <div class="nh-detail">

      <div class="detail-head">
        <BevelButton size="sm" iconPrefix="‹" onClick={goBack}>Volver</BevelButton>
        <div class="detail-name">
          {#if svcIcon(selectedService)}
            <img class="svc-icon lg" src={svcIcon(selectedService)} alt="" on:error={(e) => e.target.style.display = 'none'} />
          {:else}
            <div class="svc-fallback lg">{svcDisplayName(selectedService).slice(0, 2).toUpperCase()}</div>
          {/if}
          <div class="detail-meta">
            <span class="dm-name">{svcDisplayName(selectedService)}</span>
            <span class="dm-sub">
              <LED size={6} variant={statusLedVariant(selectedService.status)} />
              <span class="tc-dim">{selectedService.status}</span>
              {#if selectedService._isChild}
                <Badge size="sm" variant="info">docker</Badge>
              {/if}
              {#if svcVersion(selectedService)}
                <span class="tc-mute">{svcVersion(selectedService)}</span>
              {/if}
            </span>
          </div>
        </div>
        <div class="detail-actions">
          {#if selectedService.status === 'running' || selectedService.status === 'starting'}
            <BevelButton
              variant="danger"
              size="sm"
              disabled={stopping[selectedService.id + ':stop']}
              onClick={() => doAction(selectedService, 'stop')}
            >
              {stopping[selectedService.id + ':stop'] ? '▸ Deteniendo...' : '■ Detener'}
            </BevelButton>
            <BevelButton
              size="sm"
              disabled={stopping[selectedService.id + ':restart']}
              onClick={() => doAction(selectedService, 'restart')}
            >
              {stopping[selectedService.id + ':restart'] ? '▸ Reiniciando...' : '↻ Reiniciar'}
            </BevelButton>
          {:else}
            <BevelButton
              variant="primary"
              size="sm"
              disabled={stopping[selectedService.id + ':start'] || selectedService.status === 'error'}
              onClick={() => doAction(selectedService, 'start')}
            >
              {stopping[selectedService.id + ':start'] ? '▸ Iniciando...' : '▸ Iniciar'}
            </BevelButton>
          {/if}
        </div>
      </div>

      <div class="detail-section">
        <SectionHead>Información</SectionHead>
        <div class="info-grid">
          <div class="info-row"><span class="k">id</span>    <span class="v">{selectedService.id}</span></div>
          <div class="info-row"><span class="k">pool</span>  <span class="v">{selectedService.poolName || '—'}</span></div>
          <div class="info-row"><span class="k">path</span>  <span class="v path">{selectedService.path || '—'}</span></div>
          <div class="info-row"><span class="k">owner</span> <span class="v">{selectedService.owner || 'system'}</span></div>
          <div class="info-row"><span class="k">health</span><span class="v">{selectedService.health || 'unknown'}</span></div>
          {#if selectedService.status === 'running'}
            <div class="info-row"><span class="k">cpu</span> <span class="v">{(selectedService.cpuPercent || 0).toFixed(1)}%</span></div>
            <div class="info-row"><span class="k">mem</span> <span class="v">{fmtBytes(selectedService.memoryUsage || 0)}</span></div>
            <div class="info-row"><span class="k">uptime</span><span class="v">{fmtUptime(selectedService)}</span></div>
          {/if}
        </div>
      </div>

      {#if selectedService.dependencies?.length > 0}
        <div class="detail-section">
          <SectionHead count="· {selectedService.dependencies.length}">Dependencias</SectionHead>
          <div class="deps">
            {#each selectedService.dependencies as dep}
              <div class="dep-row">
                <Badge size="sm" variant="info">{dep.depType}</Badge>
                <span class="dep-target">{dep.target}</span>
                <Badge size="sm" variant={dep.required === 'required' ? 'warn' : 'default'}>
                  {dep.required}
                </Badge>
              </div>
            {/each}
          </div>
        </div>
      {/if}

      <div class="detail-section logs-section">
        <SectionHead count={detailLogs.length > 0 ? `· ${detailLogs.length} líneas` : ''}>
          Logs recientes
        </SectionHead>
        <CmdOutputLog lines={detailLogs} follow={true} height={260} />
      </div>

    </div>

  {/if}

  <!-- Footer con métricas globales -->
  <svelte:fragment slot="footer">
    <span><span class="k">services</span> <span class="v">{services.length}</span></span>
    <span class="sep">·</span>
    <span><span class="k">running</span> <span class="v tc-accent">{runningCount}</span></span>
    <span class="sep">·</span>
    <span><span class="k">errors</span> <span class="v" class:tc-crit={errorCount > 0}>{errorCount}</span></span>
    <span class="sep">·</span>
    <span><span class="k">refresh</span> <span class="v">5s</span></span>
  </svelte:fragment>

  <svelte:fragment slot="footer-right">
    <span><span class="k">cpu</span> <span class="v">{cpu.percent}%</span></span>
    <span><span class="k">mem</span> <span class="v">{ram.percent}%</span></span>
  </svelte:fragment>

</AppShell>

<style>
  .nh-kpis {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    border-bottom: 1px solid var(--border);
    background: var(--bg-1);
  }
  .nh-kpis :global(.kpi) {
    border-right: 1px solid var(--border);
  }
  .nh-kpis :global(.kpi:last-child) {
    border-right: none;
  }

  .nh-toolbar {
    display: flex;
    align-items: center;
    padding: 0 4px 0 14px;
    background: var(--bg-1);
    border-bottom: 1px solid var(--border);
    gap: 20px;
    flex-shrink: 0;
  }
  .filter-tabs {
    display: flex;
    gap: 4px;
    flex: 1;
  }
  .tb-right {
    display: flex;
    align-items: center;
    padding: 8px 10px;
  }

  .nh-table-wrap {
    padding: 14px 18px 18px;
    font-family: var(--font-mono);
  }

  .tr-ln {
    color: var(--fg-faint);
    font-size: 9px;
    text-align: right;
    font-feature-settings: "tnum";
  }

  .svc-cell {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
  }
  .svc-icon {
    width: 20px;
    height: 20px;
    object-fit: contain;
    flex-shrink: 0;
  }
  .svc-icon.lg {
    width: 40px;
    height: 40px;
  }
  .svc-fallback {
    width: 20px;
    height: 20px;
    background: var(--bg-2);
    border: 1px solid var(--border);
    color: var(--fg-dim);
    font-size: 9px;
    font-weight: 700;
    display: flex;
    align-items: center;
    justify-content: center;
    letter-spacing: 0.5px;
    flex-shrink: 0;
  }
  .svc-fallback.lg {
    width: 40px;
    height: 40px;
    font-size: 12px;
  }
  .svc-name {
    color: var(--fg);
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .svc-ver {
    color: var(--fg-mute);
    font-size: 9px;
    letter-spacing: 0.5px;
    padding: 1px 4px;
    border: 1px solid var(--border);
  }

  .svc-state {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--fg-dim);
    font-size: 10px;
  }

  .svc-num {
    text-align: right;
    color: var(--fg);
    font-feature-settings: "tnum";
    font-size: 11px;
  }
  .svc-num.warn { color: var(--warn); }
  .svc-num.crit { color: var(--crit); }
  .svc-num .dim { color: var(--fg-faint); }

  .svc-actions {
    display: flex;
    gap: 4px;
    justify-content: flex-end;
  }

  .nh-detail {
    padding: 18px 24px 24px;
    display: flex;
    flex-direction: column;
    gap: 22px;
    font-family: var(--font-mono);
  }

  .detail-head {
    display: flex;
    align-items: center;
    gap: 16px;
    padding-bottom: 18px;
    border-bottom: 1px solid var(--border);
  }
  .detail-name {
    display: flex;
    align-items: center;
    gap: 14px;
    flex: 1;
  }
  .detail-meta {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .dm-name {
    font-size: 15px;
    color: var(--fg);
    font-weight: 600;
    letter-spacing: 0.3px;
  }
  .dm-sub {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 10px;
    letter-spacing: 0.5px;
  }
  .detail-actions {
    display: flex;
    gap: 6px;
  }

  .detail-section {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .logs-section {
    flex: 1;
    min-height: 0;
  }

  .info-grid {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 4px 24px;
    background: var(--bg-1);
    border: 1px solid var(--border);
    padding: 12px 16px;
  }
  .info-row {
    display: grid;
    grid-template-columns: 80px 1fr;
    gap: 10px;
    font-size: 11px;
    padding: 3px 0;
  }
  .info-row .k {
    color: var(--fg-mute);
    text-transform: uppercase;
    letter-spacing: 1px;
    font-size: 9px;
  }
  .info-row .v {
    color: var(--fg);
    font-feature-settings: "tnum";
  }
  .info-row .v.path {
    font-size: 10px;
    word-break: break-all;
    color: var(--fg-dim);
  }

  .deps {
    display: flex;
    flex-direction: column;
    gap: 4px;
    background: var(--bg-1);
    border: 1px solid var(--border);
    padding: 10px 14px;
  }
  .dep-row {
    display: flex;
    align-items: center;
    gap: 10px;
    font-size: 10px;
  }
  .dep-target {
    flex: 1;
    color: var(--fg-dim);
    font-family: var(--font-mono);
  }

  .k { color: var(--fg-faint); }
  .v { color: var(--fg-dim); font-feature-settings: "tnum"; }
  .sep { color: var(--fg-faint); }
</style>
