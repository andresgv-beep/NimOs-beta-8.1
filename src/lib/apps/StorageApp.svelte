<script>
  /**
   * StorageApp · Gestión de almacenamiento (v3 · Fase A MVP)
   * ────────────────────────────────────────────────────────────
   * Migración desde Beta 7 siguiendo mockup "nimos-storage-retro.html".
   *
   * Scope Fase A:
   *   - Listar pools (ZFS + BTRFS) con info rica
   *   - Pantalla inteligente: banner si hay pools restaurables sin montar
   *   - Restaurar pool existente (flujo crítico · tu caso)
   *   - Ver discos con SMART status
   *   - Snapshots (listar)
   *   - Scrub manual
   *   - Escaneo de discos
   *
   * Fase B (pendiente):
   *   - Crear pool nuevo (+ wizard selector vdev)
   *   - Add/remove/replace disco
   *   - Exportar/destruir pool
   *   - Snapshots (crear/rollback/borrar)
   *   - Datasets
   *
   * TODO backend (gaps actuales):
   *   - Temperatura disco y horas operación (no se exponen en JSON)
   *   - Breakdown por categoría para donut (solo total used/available)
   *   - Rol disco (data/parity) en RAIDZ (inferible en frontend)
   *
   * Backend endpoints (Beta 8 v2):
   *   GET  /api/storage/v2/pools
   *   GET  /api/storage/v2/status
   *   GET  /api/storage/v2/disks
   *   GET  /api/storage/v2/alerts
   *   GET  /api/storage/v2/capabilities
   *   GET  /api/storage/v2/observed
   *   GET  /api/storage/v2/snapshots?pool=X
   *   POST /api/storage/v2/scan
   *   POST /api/storage/v2/scrub
   *   POST /api/storage/v2/wipe
   *   POST /api/storage/v2/pools/import
   *   POST /api/storage/v2/pool/export
   *   POST /api/storage/v2/pool/destroy
   */
  import { onMount, onDestroy } from 'svelte';
  import { token } from '$lib/stores/auth.js';
  import AppShell from '$lib/components/AppShell.svelte';
  import {
    KPICard, SectionHead, BevelButton, IconButton,
    LED, EmptyState, Spinner, Badge, StripeProgressBar
  } from '$lib/ui';
  import ExportPoolWizard from './storage/ExportPoolWizard.svelte';
  import DestroyPoolWizard from './storage/DestroyPoolWizard.svelte';
  import CreatePoolWizard from './storage/CreatePoolWizard.svelte';
  import ImportOrphanModal from './storage/ImportOrphanModal.svelte';
  import DestroyOrphanModal from './storage/DestroyOrphanModal.svelte';
  import { ConfirmDialog } from '$lib/ui';
  import {
    fmtBytes, fmtDate, inferDiskRole,
    healthLabel, healthVariant,
    usageVariant, ledVariantForHealth, smartVariant,
  } from './storage/formatters.js';
  import * as api from './storage/api.js';

  // ─── State ───
  let active = 'overview'; // 'overview' | 'disks' | 'snapshots' | 'scrub' | 'smart'

  // Export pool wizard state (UI lo llama "Desmontar", backend lo llama "export")
  let exportPoolName = null;   // nombre del pool a desmontar (null = wizard cerrado)

  // Destroy pool wizard state (destrucción definitiva · 3 pasos)
  let destroyPool = null;  // objeto pool a destruir (null = wizard cerrado)

  // Create pool wizard state
  let creatingPool = false;  // true = wizard abierto

  // Formatear disco (wipe)
  let wipeDisk = null;         // path del disco a formatear (null = dialog cerrado)
  let wipeProcessing = false;
  let wipeError = '';

  let pools = [];
  let disks = {};
  let alerts = [];
  let capabilities = {};
  let status = {};

  // Fase 7 Bloque C3.2: Observed state
  //
  // observedSnapshot: el snapshot completo del observer (con generation, etc.)
  // orphanFilesystems: filesystems BTRFS detectados que NO pertenecen a un pool
  //                    managed. Estos son los candidatos a "Importar" o "Destruir".
  // divergences: lista pre-computada de problemas (pool_missing_device, io_errors...)
  let observedSnapshot = {};
  let orphanFilesystems = [];
  let divergences = [];

  // Modales (cada componente gestiona su propio estado interno).
  // El padre solo guarda QUÉ modal está abierto y CON QUÉ datos.
  let importingFS = null;       // ObservedBtrfs a importar (null = cerrado)
  let destroyingOrphan = null;  // ObservedBtrfs a destruir (null = cerrado)

  // Expanded pools en vista overview
  let expandedPools = new Set();
  // Menu kebab abierto para un pool (solo uno a la vez)
  let kebabOpenFor = null;

  // Scrub
  let scrubbing = {};
  let scrubMsg = '';

  // Scan
  let scanning = false;

  // Snapshots (por pool)
  let snapshots = {};

  let loading = true;
  let pollInterval;

  // ─── Derived ───
  $: hasPools = pools.length > 0;

  // Metadata de cada vista para el page-header
  const VIEW_META = {
    overview:  { title: 'Resumen',    desc: 'volúmenes activos del sistema' },
    disks:     { title: 'Discos',     desc: 'dispositivos físicos del sistema' },
    snapshots: { title: 'Snapshots',  desc: 'puntos de restauración por pool' },
    scrub:     { title: 'Scrub',      desc: 'chequeo de integridad manual' },
    smart:     { title: 'SMART',      desc: 'diagnóstico de discos' },
  };
  $: viewMeta = VIEW_META[active] || VIEW_META.overview;
  $: totalDisksAssigned = pools.reduce((s, p) => s + (p.devices?.length || 0), 0);
  $: totalDisksFree = (disks.eligible?.length || 0);
  $: totalCapacity = pools.reduce((s, p) => s + (p.usage?.total_bytes || 0), 0);
  $: totalUsed = pools.reduce((s, p) => s + (p.usage?.used_bytes || 0), 0);
  $: totalFree = totalCapacity - totalUsed;
  // Salud agregada — v2 usa pool.mounted + pool.health.status
  // healthy + mounted → ok
  // degraded/at_risk/unstable → warn
  // critical o !mounted → crit
  $: overallHealth = pools.every(p => p.mounted && p.health?.status === 'healthy') && alerts.length === 0 ? 'ok'
                   : pools.some(p => !p.mounted || p.health?.status === 'critical') ? 'crit'
                   : 'warn';
  $: overallUsagePct = totalCapacity > 0 ? Math.round((totalUsed / totalCapacity) * 100) : 0;

  // ─── API ───
  // Todas las llamadas HTTP viven en ./storage/api.js (importado como `api`).
  // El componente solo orquesta: pide datos, gestiona estado, renderiza.

  async function loadAll() {
    try {
      // Fase 7 Bloque C3.2: añadido /observed para detectar filesystems
      // huérfanos (BTRFS no gestionados por NimOS) y mostrar divergencias.
      // Cada llamada aislada: si una falla, las demás siguen sirviendo
      // datos al usuario (degradación graceful).
      const [poolsData, statusData, disksData, alertsData, capsData, observedData] = await Promise.all([
        api.getPools().catch(() => []),
        api.getStatus().catch(() => ({})),
        api.getDisks().catch(() => ({})),
        api.getAlerts().catch(() => ({ alerts: [] })),
        api.getCapabilities().catch(() => ({})),
        api.getObserved().catch(() => ({ filesystems: [], divergences: [] })),
      ]);

      pools = Array.isArray(poolsData) ? poolsData : [];
      status = statusData || {};
      // /v2/disks devuelve {eligible, nvme, usb, provisioned} igual que legacy.
      disks = disksData || {};
      alerts = alertsData?.alerts || [];
      capabilities = capsData || {};

      // Observed state: filesystems detectados + divergencias pre-computadas.
      // Filtramos a los que NO están gestionados (orphans → importables).
      observedSnapshot = observedData || {};
      orphanFilesystems = (observedData?.filesystems || []).filter(fs => !fs.is_managed);
      divergences = observedData?.divergences || [];
    } catch (e) {
      console.error('[StorageApp] loadAll failed', e);
    }
    loading = false;
  }

  // ─── Refresh observed (escape hatch para el usuario) ────────────────────
  //
  // Fuerza un scan inmediato del observer y recarga datos. Útil cuando se
  // ha cambiado algo fuera de NimOS (cable conectado, disco USB, etc.).
  let refreshing = false;
  async function refreshObserved() {
    refreshing = true;
    try {
      // Llamada con ?refresh=true fuerza re-scan en el backend
      await api.getObserved({ refresh: true });
      // Recargar todos los datos para reflejar el nuevo estado
      await loadAll();
    } catch (e) {
      console.error('[StorageApp] refreshObserved failed', e);
    }
    refreshing = false;
  }

  // ─── Importar filesystem huérfano como pool managed ────────────────────

  let suggestedImportName = '';

  function openImportModal(fs) {
    importingFS = fs;
    // Sugerir un nombre razonable: usar el label si existe, lowercased
    suggestedImportName = (fs.label || '').toLowerCase().replace(/[^a-z0-9-]/g, '-').slice(0, 32);
    if (!suggestedImportName) suggestedImportName = 'imported-pool';
  }

  // ─── Bloque C3.4: Bridge wizard create → modal import ──────────────────
  //
  // Cuando el wizard de crear pool detecta DISK_HAS_FILESYSTEM y el usuario
  // elige "Importar pool existente", el wizard se cierra y emite este evento.
  // Nosotros buscamos el FS observado correspondiente y abrimos el modal.
  function handleWizardImportRequest(ev) {
    const uuid = ev.detail?.uuid;
    if (!uuid) return;
    creatingPool = false;
    // Buscar el FS en el observed state. Si no está como orphan (porque era
    // managed por otro pool), reconstruimos un objeto mínimo desde los detalles.
    let fs = orphanFilesystems.find(f => f.uuid === uuid);
    if (!fs) {
      const det = ev.detail.details || {};
      fs = {
        uuid: det.fs_uuid,
        label: det.fs_label,
        profile: det.fs_profile,
        size_bytes: det.size_bytes,
        used_bytes: det.used_bytes,
        observation_health: det.observation_health,
        is_mounted: false,
        devices: [{ path: det.disk }],
        devices_online: 1,
        devices_expected: 1,
      };
    }
    openImportModal(fs);
  }

  function closeImportModal() {
    importingFS = null;
  }

  // Tras importar con éxito: cerrar modal + refrescar observer + reload UI
  async function handleImportDone() {
    closeImportModal();
    await api.getObserved({ refresh: true });
    await loadAll();
  }

  // ─── Destruir filesystem huérfano (wipe disks) ─────────────────────────

  function openDestroyOrphanModal(fs) {
    destroyingOrphan = fs;
  }

  function closeDestroyOrphanModal() {
    destroyingOrphan = null;
  }

  // Tras destruir con éxito: cerrar modal + refrescar observer + reload UI
  async function handleDestroyOrphanDone() {
    closeDestroyOrphanModal();
    await api.getObserved({ refresh: true });
    await loadAll();
  }

  // ─── Helper: estado real de un disco (Bloque C3.3) ─────────────────────
  //
  // Cruza el path del disco con managed pools y observed orphans para
  // determinar el estado real. Esto previene el escenario donde el usuario
  // formatea un disco con un BTRFS huérfano valioso sin saberlo.
  //
  // Estados posibles:
  //   'managed'    → disco en uso por un pool gestionado por NimOS
  //   'orphan'     → disco con BTRFS no gestionado (puede importarse)
  //   'free'       → disco completamente limpio, listo para usar
  //
  // Devuelve un objeto con info estructurada para el render.
  function diskStatus(diskPath) {
    if (!diskPath) return { kind: 'free', label: 'disponible', variant: 'accent' };

    // ¿Pertenece a un pool managed?
    for (const pool of pools) {
      const poolDevices = pool.devices || [];
      for (const d of poolDevices) {
        const dPath = typeof d === 'string' ? d : (d.current_path || '');
        if (dPath === diskPath) {
          return {
            kind: 'managed',
            label: `pool ${pool.name}`,
            variant: 'success',
            poolName: pool.name,
            tooltip: `Disco en uso por el pool gestionado "${pool.name}"`,
          };
        }
      }
    }

    // ¿Tiene un BTRFS huérfano?
    for (const fs of orphanFilesystems) {
      for (const dev of (fs.devices || [])) {
        if (dev.path === diskPath) {
          return {
            kind: 'orphan',
            label: 'BTRFS huérfano',
            variant: 'warn',
            fsUuid: fs.uuid,
            fsLabel: fs.label,
            tooltip: `Tiene un filesystem BTRFS no gestionado ` +
                     `(label: ${fs.label || 'sin label'}, UUID: ${fs.uuid}). ` +
                     `Importable desde sección Observados.`,
          };
        }
      }
    }

    // Disco limpio
    return {
      kind: 'free',
      label: 'disponible',
      variant: 'accent',
      tooltip: 'Disco limpio, listo para crear un nuevo pool',
    };
  }

  async function loadSnapshots(poolName) {
    if (!poolName) return;
    try {
      const data = await api.getSnapshots(poolName);
      snapshots[poolName] = data?.snapshots || [];
      snapshots = snapshots;
    } catch (e) {
      console.warn('[StorageApp] loadSnapshots failed:', e.message);
    }
  }

  // ─── Scan ───
  async function rescanDisks() {
    scanning = true;
    try {
      // Llamada que swallows error: no usamos el payload, solo log si falla.
      await api.scanDisks().catch(e => {
        console.warn('[StorageApp] scan failed:', e.message);
      });
      await loadAll();
    } catch (e) {
      console.error('[StorageApp] scan unexpected:', e);
    }
    scanning = false;
  }

  // ─── Pool expand/collapse ───
  function togglePoolExpand(poolName) {
    kebabOpenFor = null;
    if (expandedPools.has(poolName)) {
      expandedPools.delete(poolName);
    } else {
      expandedPools.add(poolName);
      loadSnapshots(poolName);
    }
    expandedPools = expandedPools;
  }

  function toggleKebab(poolName, event) {
    if (event) event.stopPropagation();
    kebabOpenFor = kebabOpenFor === poolName ? null : poolName;
  }

  // ─── Export pool (UI: "Desmontar") ───
  function openExportPoolWizard(poolName) {
    kebabOpenFor = null;          // cerrar toolbar
    exportPoolName = poolName;    // abre el wizard
  }

  async function handleExportPoolDone() {
    exportPoolName = null;        // cerrar wizard
    // Forzar re-scan del observer (tras export el FS aparece como huérfano).
    await api.getObserved({ refresh: true });
    await loadAll();              // recargar lista de pools (el pool ya no debería estar)
  }

  // ─── Formatear disco (wipe) ───
  function openWipeDialog(diskPath) {
    wipeDisk = diskPath;
    wipeError = '';
  }

  async function confirmWipe() {
    if (!wipeDisk || wipeProcessing) return;
    wipeProcessing = true;
    wipeError = '';
    try {
      await api.wipeDisk(wipeDisk);
      // Éxito
      wipeProcessing = false;
      wipeDisk = null;
      await loadAll();
    } catch (err) {
      console.error('wipe error:', err);
      wipeError = err.message || 'Error al formatear';
      wipeProcessing = false;
    }
  }

  // ─── Destruir pool (wizard 3 pasos · solo pools desmontados) ───
  function openDestroyPoolWizard(poolObj) {
    destroyPool = poolObj;
  }

  async function handleDestroyPoolDone() {
    destroyPool = null;
    // Forzar re-scan del observer (tras destroy los discos quedan libres).
    await api.getObserved({ refresh: true });
    await loadAll();
  }

  // ─── Crear pool (wizard 4 pasos · desde discos libres) ───
  function openCreatePoolWizard() {
    creatingPool = true;
  }

  async function handleCreatePoolDone() {
    creatingPool = false;
    // Forzar re-scan del observer para reflejar el nuevo pool managed.
    await api.getObserved({ refresh: true });
    await loadAll();
    active = 'overview'; // salta a resumen para ver el pool recién creado
  }

  // ─── Scrub ───
  async function startScrub(poolName) {
    if (!confirm(`¿Iniciar scrub del pool "${poolName}"? El sistema puede ir más lento mientras corre.`)) return;
    scrubbing[poolName] = true;
    scrubbing = scrubbing;
    scrubMsg = '';
    try {
      try {
        await api.startScrub(poolName);
        scrubMsg = `Scrub iniciado en "${poolName}"`;
      } catch (e) {
        scrubMsg = e.message || 'Error al iniciar scrub';
      }
      await loadAll();
    } catch {
      scrubMsg = 'Error de conexión';
    }
    scrubbing[poolName] = false;
    scrubbing = scrubbing;
    kebabOpenFor = null;
  }

  // ─── Click-outside listener para kebab ───
  function onDocClick() {
    kebabOpenFor = null;
  }

  // ─── Lifecycle ───
  onMount(async () => {
    let attempts = 0;
    while (!$token && attempts < 10) { await new Promise(r => setTimeout(r, 200)); attempts++; }
    await loadAll();
    pollInterval = setInterval(loadAll, 20000); // 20s · storage es lento de cambiar
    document.addEventListener('click', onDocClick);
  });

  onDestroy(() => {
    if (pollInterval) clearInterval(pollInterval);
    document.removeEventListener('click', onDocClick);
  });
</script>

<AppShell
  appId="storage"
  title="Storage"
  headerIcon="S"
  pathSegments={['storage', active]}
  sections={[
    {
      label: 'Volúmenes',
      items: [
        { id: 'overview',  label: 'Resumen',    keyHint: '1', badge: pools.length },
        { id: 'disks',     label: 'Discos',     keyHint: '2', badge: totalDisksAssigned + totalDisksFree },
        { id: 'snapshots', label: 'Snapshots',  keyHint: '3' },
      ],
    },
    {
      label: 'Herramientas',
      items: [
        { id: 'scrub', label: 'Scrub', keyHint: 'S' },
        { id: 'smart', label: 'SMART', keyHint: 'M' },
      ],
    },
  ]}
  bind:active
>

  <!-- Page header: cambia según vista activa (Resumen, Discos, etc.) -->
  <svelte:fragment slot="page-header">
    <b>{viewMeta.title}</b>
    <span class="ph-desc">· {viewMeta.desc}</span>
  </svelte:fragment>

  {#if loading}
    <div class="storage-loading">
      <Spinner label="Cargando volúmenes y discos..." />
    </div>
  {:else}

  <!-- Summary bar · SOLO en vista Resumen -->
  {#if active === 'overview'}
  <div class="st-kpis">
    <KPICard
      label="Volúmenes"
      value={String(pools.length)}
      unit=""
      state={pools.length > 0 ? 'online' : 'vacío'}
      stateVariant={pools.length > 0 ? 'ok' : 'warn'}
      valueVariant={pools.length > 0 ? 'accent' : 'default'}
      bracketVariant={pools.length > 0 ? 'accent' : 'warn'}
    />
    <KPICard
      label="Discos"
      value={String(totalDisksAssigned + totalDisksFree)}
      unit=""
      state={`${totalDisksAssigned} asignados · ${totalDisksFree} libres`}
      stateVariant="ok"
      valueVariant="default"
    />
    <KPICard
      label="Capacidad"
      value={fmtBytes(totalCapacity)}
      unit=""
      state={totalCapacity > 0 ? `${fmtBytes(totalFree)} libres · ${overallUsagePct}%` : '—'}
      stateVariant={usageVariant(overallUsagePct)}
      valueVariant={usageVariant(overallUsagePct) === 'crit' ? 'crit' : usageVariant(overallUsagePct) === 'warn' ? 'warn' : 'default'}
      bracketVariant={usageVariant(overallUsagePct) === 'crit' ? 'crit' : 'accent'}
    />
    <KPICard
      label="Salud"
      value={overallHealth === 'ok' ? 'OK' : overallHealth === 'warn' ? 'WARN' : 'CRIT'}
      unit=""
      state={alerts.length === 0 ? 'sin incidencias' : `${alerts.length} alerta${alerts.length > 1 ? 's' : ''}`}
      stateVariant={overallHealth}
      valueVariant={overallHealth === 'crit' ? 'crit' : overallHealth === 'warn' ? 'warn' : 'accent'}
      bracketVariant={overallHealth === 'crit' ? 'crit' : overallHealth === 'warn' ? 'warn' : 'accent'}
    />
  </div>
  {/if}

  <!-- ═══════ CONTENT PRINCIPAL ═══════ -->
  <div class="st-scroll">

    <!-- ══ RESUMEN (OVERVIEW) ══ -->
    {#if active === 'overview'}
      <div class="st-section">
        <div class="section-row">
          <SectionHead count={pools.length > 0 ? `· ${pools.length} activos` : ''}>
            Volúmenes
          </SectionHead>
          <div class="section-actions">
            <BevelButton size="sm" onClick={rescanDisks} disabled={scanning}>
              {scanning ? '▸ Escaneando...' : '↻ Escanear'}
            </BevelButton>
            <BevelButton
              variant="primary"
              size="sm"
              onClick={openCreatePoolWizard}
              disabled={!(disks.eligible?.length > 0)}
              title={disks.eligible?.length > 0
                ? 'Crear un nuevo pool de almacenamiento'
                : 'No hay discos libres para crear un pool'}
            >
              + Nuevo volumen
            </BevelButton>
          </div>
        </div>

        {#if pools.length === 0}
          <EmptyState
            icon="◇"
            title="Sin volúmenes configurados"
            hint={orphanFilesystems.length > 0
              ? `Se detectaron ${orphanFilesystems.length} filesystem(s) huérfano(s). Puedes importarlos como pool.`
              : 'Crea un volumen nuevo para empezar.'}
          />
        {:else}
          <!-- Lista de pools -->
          <div class="pools">
            {#each pools as pool (pool.name)}
              <div
                class="pool"
                class:open={expandedPools.has(pool.name)}
                class:degraded={pool.health?.status === 'degraded' || pool.health?.status === 'at_risk' || pool.health?.status === 'unstable'}
                class:crit={!pool.mounted || pool.health?.status === 'critical'}
              >
                <!-- Pool header -->
                <div class="pool-head" on:click={() => togglePoolExpand(pool.name)}
                     on:keydown={(e) => e.key === 'Enter' && togglePoolExpand(pool.name)}
                     role="button" tabindex="0">
                  <div class="pool-head-icon">◆</div>
                  <div class="pool-ident">
                    <div class="pool-name">
                      {pool.name}
                      {#if pool.is_primary}
                        <Badge size="sm" variant="accent">primary</Badge>
                      {/if}
                    </div>
                    <div class="pool-meta">
                      BTRFS · {pool.profile || 'single'} ·
                      {pool.devices?.length || 0} disco{pool.devices?.length === 1 ? '' : 's'} ·
                      {fmtBytes(pool.usage?.used_bytes)} usados
                    </div>
                  </div>
                  <div class="pool-bar-wrap">
                    <StripeProgressBar
                      value={pool.usage?.usage_percent || 0}
                      variant={usageVariant(pool.usage?.usage_percent || 0)}
                      showLabel={true}
                    />
                  </div>
                  <div class="pool-size">{fmtBytes(pool.usage?.total_bytes)}</div>
                  <div class="pool-status">
                    <LED size={8} variant={ledVariantForHealth(pool.health?.status)} />
                  </div>
                  <div class="pool-chev" class:rot={expandedPools.has(pool.name)}>›</div>

                  <button
                    class="pool-kebab"
                    class:active={kebabOpenFor === pool.name}
                    on:click={(e) => toggleKebab(pool.name, e)}
                    title="Acciones"
                  >⋮</button>
                </div>

                <!-- Toolbar inline de acciones (3 acciones no-destructivas) -->
                {#if kebabOpenFor === pool.name}
                  <div
                    class="pool-actions-bar"
                    on:click|stopPropagation
                    on:keydown
                    role="toolbar"
                    aria-label="Acciones del pool {pool.name}"
                    tabindex="-1"
                  >
                    <button class="pa-btn" disabled title="Disponible en Fase B">
                      <span class="pa-num">01</span>
                      <span>Snapshot</span>
                      <span class="pa-tag">Fase B</span>
                    </button>
                    <button
                      class="pa-btn"
                      on:click={() => startScrub(pool.name)}
                      disabled={scrubbing[pool.name]}
                    >
                      <span class="pa-num">02</span>
                      <span>{scrubbing[pool.name] ? 'Iniciando...' : 'Verificar integridad'}</span>
                    </button>
                    <button
                      class="pa-btn"
                      on:click={() => openExportPoolWizard(pool.name)}
                    >
                      <span class="pa-num">03</span>
                      <span>Desmontar</span>
                    </button>
                  </div>
                {/if}

                <!-- Pool expanded body -->
                {#if expandedPools.has(pool.name)}
                  <div class="pool-body">

                    <!-- Info grid (reemplaza donut hasta tener backend) -->
                    <div class="pool-info-grid">
                      <div class="pig-col">
                        <div class="pig-label">Total</div>
                        <div class="pig-value">{fmtBytes(pool.usage?.total_bytes)}</div>
                      </div>
                      <div class="pig-col">
                        <div class="pig-label">Usado</div>
                        <div class="pig-value tc-accent">{fmtBytes(pool.usage?.used_bytes)}</div>
                      </div>
                      <div class="pig-col">
                        <div class="pig-label">Libre</div>
                        <div class="pig-value">{fmtBytes(pool.usage?.available_bytes)}</div>
                      </div>
                      <div class="pig-col">
                        <div class="pig-label">Uso</div>
                        <div class="pig-value" class:warn={pool.usage?.usage_percent > 75} class:crit={pool.usage?.usage_percent > 90}>
                          {pool.usage?.usage_percent || 0}%
                        </div>
                      </div>
                      <div class="pig-col">
                        <div class="pig-label">Health</div>
                        <div class="pig-value">
                          <LED size={7} variant={ledVariantForHealth(pool.health?.status)} />
                          <span>{pool.health?.status || '—'}</span>
                        </div>
                      </div>
                      <div class="pig-col">
                        <div class="pig-label">Mount</div>
                        <div class="pig-value mono sm">{pool.mount_point || '—'}</div>
                      </div>
                    </div>

                    <!-- Disk table -->
                    <div class="pool-disks">
                      <div class="pd-head">
                        Discos del volumen · {pool.devices?.length || 0}
                        <span class="tc-mute todo">
                          (temp y horas pendiente backend)
                        </span>
                      </div>
                      <div class="disk-table cols-6-pool">
                        <div class="disk-thead">
                          <div></div>
                          <div>Modelo</div>
                          <div>Dispositivo</div>
                          <div>Capacidad</div>
                          <div>Rol</div>
                          <div>SMART</div>
                        </div>
                        {#each (pool.devices || []) as disk, i}
                          <div class="disk-row">
                            <div class="disk-idx">D{i + 1}</div>
                            <div class="disk-cell mono">{disk.model || '—'}</div>
                            <div class="disk-cell mono">{disk.current_path || '—'}</div>
                            <div class="disk-cell">{fmtBytes(disk.size_bytes) || '—'}</div>
                            <div class="disk-cell">
                              <Badge size="sm" variant={inferDiskRole(pool.devices, i, pool.profile) === 'parity' ? 'warn' : 'default'}>
                                {inferDiskRole(pool.devices, i, pool.profile)}
                              </Badge>
                            </div>
                            <div class="disk-cell">
                              <LED size={7} variant={smartVariant(disk.smart_status)} />
                              <span class="tc-dim sm">{disk.smart_status || 'unknown'}</span>
                            </div>
                          </div>
                        {/each}
                      </div>
                    </div>

                    <!-- Snapshots (BTRFS soporta nativamente) -->
                    {#if snapshots[pool.name]?.length > 0}
                      <div class="pool-snapshots">
                        <div class="pd-head">
                          Snapshots · {snapshots[pool.name].length}
                        </div>
                        <div class="snap-list">
                          {#each snapshots[pool.name].slice(0, 5) as snap}
                            <div class="snap-row">
                              <span class="mono">{snap.name || snap}</span>
                              {#if snap.used}
                                <span class="tc-mute">{fmtBytes(snap.used)}</span>
                              {/if}
                              {#if snap.created}
                                <span class="tc-mute">{fmtDate(snap.created)}</span>
                              {/if}
                            </div>
                          {/each}
                          {#if snapshots[pool.name].length > 5}
                            <div class="snap-more">
                              <span class="tc-mute">+ {snapshots[pool.name].length - 5} más · ver pestaña Snapshots</span>
                            </div>
                          {/if}
                        </div>
                      </div>
                    {/if}

                  </div>
                {/if}
              </div>
            {/each}
          </div>
        {/if}

        {#if scrubMsg}
          <div class="msg">{scrubMsg}</div>
        {/if}
      </div>

      <!--
        ══════════════════════════════════════════════════════════════
         OBSERVADOS · Filesystems BTRFS detectados sin gestionar
        ══════════════════════════════════════════════════════════════
        Fase 7 Bloque C3.2:
          Modelo Managed/Observed. Aquí mostramos BTRFS detectados
          físicamente que NO están registrados en SQLite como pools
          gestionados. Para cada uno:
            · Importar como pool managed (preserva datos)
            · Destruir (wipefs todos los discos)
          Esta sección solo aparece si hay orphans → no contamina la
          UI cuando todo está coherente.
      -->
      {#if orphanFilesystems.length > 0}
        <div class="st-section">
          <div class="section-row">
            <SectionHead count="· {orphanFilesystems.length}">
              Observados · no gestionados
            </SectionHead>
            <div class="section-actions">
              <BevelButton size="sm" onClick={refreshObserved} disabled={refreshing}>
                {refreshing ? '▸ Actualizando...' : '↻ Refrescar'}
              </BevelButton>
            </div>
          </div>

          <div class="observed-list">
            {#each orphanFilesystems as fs (fs.uuid)}
              <div class="observed-card">
                <div class="obs-head">
                  <div class="obs-title">
                    <span class="obs-label">{fs.label || '(sin label)'}</span>
                    <Badge size="sm" variant={healthVariant(fs.observation_health)}>
                      {healthLabel(fs.observation_health)}
                    </Badge>
                  </div>
                  <div class="obs-uuid mono tc-mute">
                    UUID: {fs.uuid}
                  </div>
                </div>

                <div class="obs-info">
                  <div class="obs-row">
                    <span class="tc-mute">Tipo:</span>
                    <span class="mono">BTRFS · {fs.profile || 'single'}</span>
                  </div>
                  <div class="obs-row">
                    <span class="tc-mute">Discos:</span>
                    <span class="mono">
                      {fs.devices_online}/{fs.devices_expected} online
                      {#if fs.devices_missing > 0}
                        · <span class="tc-warn">faltan {fs.devices_missing}</span>
                      {/if}
                    </span>
                  </div>
                  {#if fs.size_bytes > 0}
                    <div class="obs-row">
                      <span class="tc-mute">Capacidad:</span>
                      <span class="mono">{fmtBytes(fs.size_bytes)} · {fmtBytes(fs.used_bytes)} usados</span>
                    </div>
                  {/if}
                  {#if fs.is_mounted}
                    <div class="obs-row">
                      <span class="tc-mute">Montado:</span>
                      <span class="mono">{fs.mount_point}</span>
                    </div>
                  {:else}
                    <div class="obs-row">
                      <span class="tc-mute">Estado:</span>
                      <span class="mono">desmontado</span>
                    </div>
                  {/if}
                </div>

                <div class="obs-devices">
                  <div class="obs-devices-label tc-mute">Discos físicos:</div>
                  <div class="obs-devices-list">
                    {#each (fs.devices || []) as dev}
                      <span class="mono obs-disk-pill">{dev.path}</span>
                    {/each}
                  </div>
                </div>

                <div class="obs-actions">
                  <BevelButton
                    variant="primary"
                    size="sm"
                    onClick={() => openImportModal(fs)}
                    disabled={fs.devices_missing > 0}
                    title={fs.devices_missing > 0
                      ? 'No se puede importar: faltan discos'
                      : 'Importar como pool gestionado (preserva datos)'}
                  >
                    ⬇ Importar como pool
                  </BevelButton>
                  <BevelButton
                    size="sm"
                    onClick={() => openDestroyOrphanModal(fs)}
                    title="DESTRUIR — borra todos los datos de los discos"
                  >
                    ⚠ Destruir
                  </BevelButton>
                </div>
              </div>
            {/each}
          </div>

          {#if divergences.length > 0}
            <div class="divergences">
              {#each divergences.filter(d => d.severity !== 'info') as div}
                <div class="div-row" class:warn={div.severity === 'warning'} class:crit={div.severity === 'critical'}>
                  <LED size={7} variant={div.severity === 'critical' ? 'crit' : 'warn'} />
                  <div>
                    <div>{div.detail}</div>
                    {#if div.hint}
                      <div class="tc-mute sm">{div.hint}</div>
                    {/if}
                  </div>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      {/if}

      <!-- Alertas globales -->
      {#if alerts.length > 0}
        <div class="st-section">
          <SectionHead count="· {alerts.length}">Alertas del sistema</SectionHead>
          <div class="alerts-list">
            {#each alerts as alert}
              <div class="alert-row" class:crit={alert.level === 'critical'} class:warn={alert.level === 'warning'}>
                <LED size={7} variant={alert.level === 'critical' ? 'crit' : 'warn'} />
                <div class="alert-body">
                  <div class="alert-msg">{alert.message}</div>
                  {#if alert.pool}
                    <div class="alert-meta">
                      pool: <span class="mono">{alert.pool}</span> ·
                      {fmtDate(alert.timestamp)}
                    </div>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        </div>
      {/if}
    {/if}

    <!-- ══ DISCOS ══ -->
    {#if active === 'disks'}
      <div class="st-section">
        <div class="section-row">
          <SectionHead count={`· ${totalDisksAssigned + totalDisksFree} detectados`}>
            Discos del sistema
          </SectionHead>
          <div class="section-actions">
            <BevelButton size="sm" onClick={rescanDisks} disabled={scanning}>
              {scanning ? '▸ Escaneando...' : '↻ Rescan buses'}
            </BevelButton>
            <BevelButton
              variant="primary"
              size="sm"
              onClick={openCreatePoolWizard}
              disabled={!(disks.eligible?.length > 0)}
              title={disks.eligible?.length > 0
                ? 'Crear un nuevo pool con los discos libres'
                : 'No hay discos libres para crear un pool'}
            >
              + Crear volumen
            </BevelButton>
          </div>
        </div>

        <!-- Discos asignados a pools -->
        {#if totalDisksAssigned > 0}
          <SectionHead count={`· ${totalDisksAssigned}`}>Asignados a pools</SectionHead>
          {#each pools as pool}
            <div class="pool-group">
              <div class="pool-group-head">
                <div class="pool-group-title">
                  <Badge size="sm" variant="accent">{pool.name}</Badge>
                  <span class="sm tc-dim">· {(pool.devices || []).length} {(pool.devices || []).length === 1 ? 'disco' : 'discos'}</span>
                </div>
                <span class="sm tc-faint mono">montado · para destruir, desmóntalo primero</span>
              </div>
              <div class="disk-table cols-6-assigned">
                <div class="disk-thead">
                  <div>Dispositivo</div>
                  <div>Modelo</div>
                  <div>Capacidad</div>
                  <div>Pool</div>
                  <div>SMART</div>
                  <div>Acción</div>
                </div>
                {#each (pool.devices || []) as disk}
                  <div class="disk-row">
                    <div class="disk-cell mono">{disk.current_path || '—'}</div>
                    <div class="disk-cell mono">{disk.model || '—'}</div>
                    <div class="disk-cell">{fmtBytes(disk.size_bytes) || '—'}</div>
                    <div class="disk-cell">
                      <Badge size="sm" variant="accent">{pool.name}</Badge>
                    </div>
                    <div class="disk-cell">
                      <LED size={7} variant={smartVariant(disk.smart_status)} />
                      <span class="tc-dim sm">{disk.smart_status || 'unknown'}</span>
                    </div>
                    <div class="disk-cell disk-actions">
                      <button class="disk-action-btn" disabled title="Disponible en Fase B7">
                        Desasignar <span class="action-tag">B7</span>
                      </button>
                      <button class="disk-action-btn" disabled title="Disponible en Fase B7">
                        Reemplazar <span class="action-tag">B7</span>
                      </button>
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          {/each}
        {/if}

        <!-- Discos libres -->
        <div style="margin-top:24px">
          <SectionHead count={`· ${disks.eligible?.length || 0}`}>Discos libres (elegibles)</SectionHead>
          {#if !disks.eligible || disks.eligible.length === 0}
            <EmptyState icon="◌" title="Sin discos libres" hint="Todos los discos están asignados a pools" />
          {:else}
            <div class="disk-table cols-6-free">
              <div class="disk-thead">
                <div>Dispositivo</div>
                <div>Modelo</div>
                <div>Capacidad</div>
                <div>Tipo</div>
                <div>Estado</div>
                <div>Acción</div>
              </div>
              {#each disks.eligible as disk}
                {@const dPath = disk.path || '/dev/' + disk.name}
                {@const dStatus = diskStatus(dPath)}
                <div class="disk-row" class:has-orphan={dStatus.kind === 'orphan'}>
                  <div class="disk-cell mono">{dPath}</div>
                  <div class="disk-cell mono">{disk.model || '—'}</div>
                  <div class="disk-cell">{disk.sizeH || fmtBytes(disk.size)}</div>
                  <div class="disk-cell">
                    <Badge size="sm" variant={disk.rotational ? 'default' : 'info'}>
                      {disk.rotational ? 'HDD' : 'SSD'}
                    </Badge>
                  </div>
                  <div class="disk-cell" title={dStatus.tooltip || ''}>
                    <Badge size="sm" variant={dStatus.variant}>
                      {dStatus.label}
                    </Badge>
                    {#if dStatus.kind === 'orphan'}
                      <div class="disk-orphan-hint tc-mute sm">
                        Datos preservables · ver Observados
                      </div>
                    {/if}
                  </div>
                  <div class="disk-cell disk-actions">
                    <button
                      class="disk-action-btn primary"
                      disabled
                      title="Crear un volumen nuevo con este disco · Disponible en Fase B5"
                    >
                      + Usar en volumen <span class="action-tag">B5</span>
                    </button>
                    <button
                      class="disk-action-btn warn"
                      on:click={() => openWipeDialog(dPath)}
                      title={dStatus.kind === 'orphan'
                        ? '⚠ Atención: este disco tiene datos. Formatear los borrará permanentemente.'
                        : 'Formatear disco (borra restos de formatos anteriores)'}
                    >
                      Formatear
                    </button>
                  </div>
                </div>
              {/each}
            </div>
          {/if}
        </div>

        <!-- USB si hay -->
        {#if disks.usb?.length > 0}
          <div style="margin-top:24px">
            <SectionHead count={`· ${disks.usb.length}`}>Dispositivos USB</SectionHead>
            <div class="disk-table cols-5-disk">
              <div class="disk-thead">
                <div>Dispositivo</div>
                <div>Modelo</div>
                <div>Capacidad</div>
                <div>Tipo</div>
                <div>Estado</div>
              </div>
              {#each disks.usb as disk}
                <div class="disk-row">
                  <div class="disk-cell mono">{disk.path || '/dev/' + disk.name}</div>
                  <div class="disk-cell mono">{disk.model || '—'}</div>
                  <div class="disk-cell">{disk.sizeH || fmtBytes(disk.size)}</div>
                  <div class="disk-cell"><Badge size="sm" variant="warn">USB</Badge></div>
                  <div class="disk-cell"><Badge size="sm">externo</Badge></div>
                </div>
              {/each}
            </div>
          </div>
        {/if}
      </div>
    {/if}

    <!-- ══ SNAPSHOTS ══ -->
    {#if active === 'snapshots'}
      <div class="st-section">
        <SectionHead>Snapshots</SectionHead>

        {#if pools.length === 0}
          <EmptyState icon="◇" title="Sin pools configurados" hint="Crea o restaura un pool ZFS para gestionar snapshots" />
        {:else}
          {#each pools.filter(p => p.type === 'zfs' || p.filesystem === 'zfs') as pool}
            <div class="snap-block">
              <div class="snap-block-head">
                <div class="pool-head-icon sm">◆</div>
                <span class="mono">{pool.name}</span>
                {#if !snapshots[pool.name]}
                  <BevelButton size="sm" onClick={() => loadSnapshots(pool.name)}>Cargar</BevelButton>
                {/if}
                <div style="flex:1"></div>
                <BevelButton variant="primary" size="sm" disabled>
                  + Crear snapshot <span class="tc-mute">(Fase B)</span>
                </BevelButton>
              </div>

              {#if snapshots[pool.name]}
                {#if snapshots[pool.name].length === 0}
                  <EmptyState icon="◌" title="Sin snapshots" hint={`No hay snapshots en "${pool.name}"`} />
                {:else}
                  <div class="disk-table cols-4-snap">
                    <div class="disk-thead">
                      <div>Nombre</div>
                      <div>Usado</div>
                      <div>Creado</div>
                      <div>Acciones</div>
                    </div>
                    {#each snapshots[pool.name] as snap}
                      <div class="disk-row">
                        <div class="disk-cell mono">{snap.name || snap}</div>
                        <div class="disk-cell">{snap.used ? fmtBytes(snap.used) : '—'}</div>
                        <div class="disk-cell">{fmtDate(snap.created)}</div>
                        <div class="disk-cell">
                          <IconButton size="sm" title="Rollback" disabled>↺</IconButton>
                          <IconButton size="sm" variant="danger" title="Eliminar" disabled>×</IconButton>
                        </div>
                      </div>
                    {/each}
                  </div>
                {/if}
              {/if}
            </div>
          {/each}

          {#if pools.filter(p => p.type === 'zfs' || p.filesystem === 'zfs').length === 0}
            <EmptyState icon="!" title="Sin pools ZFS" hint="Los snapshots solo están disponibles en pools ZFS. Tus pools son BTRFS." />
          {/if}
        {/if}
      </div>
    {/if}

    <!-- ══ SCRUB ══ -->
    {#if active === 'scrub'}
      <div class="st-section">
        <SectionHead>Scrub manual</SectionHead>

        {#if pools.length === 0}
          <EmptyState icon="◇" title="Sin pools" hint="No hay pools para ejecutar scrub" />
        {:else}
          <div class="hint-box">
            <b>¿Qué es scrub?</b> Es un chequeo de integridad que recorre todos los datos del pool
            y verifica checksums. Útil mensualmente para detectar errores silenciosos.
            Puede tardar horas y el sistema irá más lento mientras corre.
          </div>

          <div class="disk-table cols-5-scrub">
            <div class="disk-thead">
              <div>Pool</div>
              <div>Tipo</div>
              <div>Tamaño</div>
              <div>Último scrub</div>
              <div>Acción</div>
            </div>
            {#each pools as pool}
              <div class="disk-row">
                <div class="disk-cell mono">{pool.name}</div>
                <div class="disk-cell">BTRFS</div>
                <div class="disk-cell">{fmtBytes(pool.usage?.total_bytes)}</div>
                <div class="disk-cell tc-mute">—</div>
                <div class="disk-cell">
                  <BevelButton
                    size="sm"
                    onClick={() => startScrub(pool.name)}
                    disabled={scrubbing[pool.name]}
                  >
                    {scrubbing[pool.name] ? '▸ Iniciando...' : '▸ Scrub ahora'}
                  </BevelButton>
                </div>
              </div>
            {/each}
          </div>

          {#if scrubMsg}
            <div class="msg">{scrubMsg}</div>
          {/if}
        {/if}
      </div>
    {/if}

    <!-- ══ SMART ══ -->
    {#if active === 'smart'}
      <div class="st-section">
        <SectionHead>SMART de discos</SectionHead>

        <div class="hint-box">
          <b>SMART</b> (Self-Monitoring, Analysis and Reporting Technology) es una tecnología
          que permite a los discos auto-diagnosticarse. Un SMART status <span class="tc-accent">ok</span>
          significa que el disco no reporta errores. <span class="tc-warn">warning</span> y
          <span class="tc-crit">critical</span> requieren atención.
        </div>

        {#if pools.length === 0 && (!disks.eligible || disks.eligible.length === 0)}
          <EmptyState icon="◌" title="Sin discos" hint="No hay discos detectados en el sistema" />
        {:else}
          <div class="disk-table cols-6-smart">
            <div class="disk-thead">
              <div>Dispositivo</div>
              <div>Modelo</div>
              <div>Capacidad</div>
              <div>Pool</div>
              <div>SMART</div>
              <div>Notas</div>
            </div>
            {#each pools as pool}
              {#each (pool.devices || []) as disk}
                <div class="disk-row">
                  <div class="disk-cell mono">{disk.current_path || '—'}</div>
                  <div class="disk-cell mono">{disk.model || '—'}</div>
                  <div class="disk-cell">{fmtBytes(disk.size_bytes) || '—'}</div>
                  <div class="disk-cell"><Badge size="sm" variant="accent">{pool.name}</Badge></div>
                  <div class="disk-cell">
                    <LED size={7} variant={smartVariant(disk.smart_status)} />
                    <span class="sm">{disk.smart_status || 'unknown'}</span>
                  </div>
                  <div class="disk-cell tc-mute sm">
                    {#if disk.smart_status === 'critical'}Reemplazar cuanto antes
                    {:else if disk.smart_status === 'warning'}Monitorizar
                    {:else if disk.smart_status === 'missing'}Disco desconectado
                    {:else if disk.smart_status === 'ok'}Sin incidencias
                    {:else}—{/if}
                  </div>
                </div>
              {/each}
            {/each}
          </div>

          <div class="todo-note">
            <b>TODO</b> · temperatura, horas de operación y errores detallados pendientes de añadir al backend.
          </div>
        {/if}
      </div>
    {/if}

  </div>
  {/if}

  <!-- Footer -->
  <svelte:fragment slot="footer">
    <span><span class="k">pools</span> <span class="v">{pools.length}</span></span>
    <span class="sep">·</span>
    <span><span class="k">disks</span> <span class="v">{totalDisksAssigned + totalDisksFree}</span></span>
    <span class="sep">·</span>
    <span><span class="k">btrfs</span> <span class="v" class:tc-accent={capabilities.btrfs}>{capabilities.btrfs ? 'available' : 'n/a'}</span></span>
  </svelte:fragment>

  <svelte:fragment slot="footer-right">
    <span><span class="k">usage</span> <span class="v" class:tc-accent={overallUsagePct < 75}>{overallUsagePct}%</span></span>
  </svelte:fragment>

</AppShell>

<!-- Export pool wizard · se abre desde kebab toolbar Resumen (UI: "Desmontar") -->
{#if exportPoolName}
  <ExportPoolWizard
    poolName={exportPoolName}
    on:done={handleExportPoolDone}
    on:cancel={() => exportPoolName = null}
  />
{/if}

<!-- ConfirmDialog · Formatear disco (wipe) -->
<ConfirmDialog
  open={wipeDisk !== null}
  title="Formatear disco"
  message={`Esta acción borrará todos los datos de ${wipeDisk || ''}. No se puede deshacer.`}
  confirmLabel="Formatear disco"
  inputConfirm="FORMATEAR"
  variant="danger"
  processing={wipeProcessing}
  on:confirm={confirmWipe}
  on:cancel={() => { wipeDisk = null; wipeError = ''; }}
>
  {#if wipeError}
    <div class="dialog-err">{wipeError}</div>
  {/if}
</ConfirmDialog>

<!-- Destroy pool wizard · 4 pasos: detección → servicios → desmontaje → confirmación -->
{#if destroyPool}
  <DestroyPoolWizard
    pool={destroyPool}
    on:done={handleDestroyPoolDone}
    on:cancel={() => destroyPool = null}
  />
{/if}

<!-- Create pool wizard · 4 pasos: tipo → discos → nombre → confirmación -->
{#if creatingPool}
  <CreatePoolWizard
    capabilities={capabilities}
    eligibleDisks={disks.eligible || []}
    pools={pools}
    orphanFilesystems={orphanFilesystems}
    on:done={handleCreatePoolDone}
    on:cancel={() => creatingPool = false}
    on:request-import={handleWizardImportRequest}
  />
{/if}

<!-- Modal: Importar filesystem huérfano como pool managed -->
{#if importingFS}
  <ImportOrphanModal
    fs={importingFS}
    suggestedName={suggestedImportName}
    on:done={handleImportDone}
    on:cancel={closeImportModal}
  />
{/if}

<!-- Modal: Destruir filesystem huérfano (wipe disks) -->
{#if destroyingOrphan}
  <DestroyOrphanModal
    fs={destroyingOrphan}
    on:done={handleDestroyOrphanDone}
    on:cancel={closeDestroyOrphanModal}
  />
{/if}

<style>
  /* Loading ───── */
  .storage-loading {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 280px;
  }

  /* KPIs ───── */
  .st-kpis {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    border-bottom: 1px solid var(--border);
    background: var(--bg-1);
    flex-shrink: 0;
  }
  .st-kpis :global(.kpi) { border-right: 1px solid var(--border); }
  .st-kpis :global(.kpi:last-child) { border-right: none; }

  /* Main scroll ───── */
  .st-scroll {
    flex: 1;
    overflow-y: auto;
    padding: 22px 28px 24px;
    display: flex;
    flex-direction: column;
    gap: 26px;
  }
  .st-section {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .section-row {
    display: flex;
    align-items: center;
    gap: 14px;
  }
  .section-actions {
    display: flex;
    gap: 8px;
    margin-left: auto;
  }

  /* Pool card ───── */
  .pools {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .pool {
    background: var(--bg-1);
    border: 1px solid var(--border);
    font-family: var(--font-mono);
    transition: border-color 0.12s;
  }
  .pool.open { border-color: var(--border-bright); }
  .pool.degraded { border-left: 2px solid var(--warn); }
  .pool.crit { border-left: 2px solid var(--crit); }

  .pool-head {
    display: grid;
    grid-template-columns: 24px 1fr 220px 80px 18px 18px 24px;
    gap: 16px;
    align-items: center;
    padding: 12px 16px;
    cursor: pointer;
    user-select: none;
  }
  .pool-head:hover { background: var(--bg-2); }

  .pool-head-icon {
    color: var(--accent);
    font-size: 14px;
    text-align: center;
  }
  .pool-head-icon.sm { font-size: 11px; }

  .pool-ident {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .pool-name {
    font-size: 13px;
    color: var(--fg);
    font-weight: 600;
    letter-spacing: 0.3px;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .pool-meta {
    font-size: 10px;
    color: var(--fg-mute);
    letter-spacing: 0.3px;
  }

  .pool-bar-wrap { min-width: 0; }
  .pool-size {
    font-size: 11px;
    color: var(--fg);
    text-align: right;
    font-feature-settings: "tnum";
  }
  .pool-status {
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .pool-chev {
    color: var(--fg-mute);
    font-size: 14px;
    transition: transform 0.15s;
    text-align: center;
  }
  .pool-chev.rot { transform: rotate(90deg); color: var(--accent); }

  .pool-kebab {
    width: 24px;
    height: 24px;
    background: transparent;
    border: none;
    color: var(--fg-mute);
    cursor: pointer;
    font-size: 14px;
    font-family: var(--font-mono);
    transition: color 0.12s;
  }
  .pool-kebab:hover { color: var(--accent); }

  /* Kebab button · ahora con estado active cuando se abre la toolbar */
  .pool-kebab.active {
    color: var(--accent);
    background: var(--bg-2);
  }

  /* Toolbar inline de acciones · aparece bajo el pool-head cuando se pulsa kebab */
  .pool-actions-bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    padding: 10px 16px;
    background: var(--bg-2);
    border-top: 1px solid var(--border);
    border-bottom: 1px solid var(--border);
    font-family: var(--font-mono);
    animation: pab-in 0.15s ease-out;
  }
  @keyframes pab-in {
    from { opacity: 0; max-height: 0; padding-top: 0; padding-bottom: 0; }
    to   { opacity: 1; max-height: 60px; padding-top: 10px; padding-bottom: 10px; }
  }

  .pa-btn {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    padding: 6px 10px;
    background: var(--bg);
    border: 1px solid var(--border);
    color: var(--fg-dim);
    font-family: inherit;
    font-size: 10px;
    letter-spacing: 0.3px;
    cursor: pointer;
    transition: all 0.1s;
    clip-path: polygon(
      0 0, calc(100% - 5px) 0, 100% 5px,
      100% 100%, 5px 100%, 0 calc(100% - 5px)
    );
  }
  .pa-btn:not(:disabled):hover {
    border-color: var(--accent);
    color: var(--accent);
    background: var(--bg-1);
  }
  .pa-btn:disabled {
    cursor: not-allowed;
    opacity: 0.5;
  }
  .pa-num {
    color: var(--fg-faint);
    font-size: 9px;
    min-width: 22px;
  }
  .pa-tag {
    color: var(--fg-faint);
    font-size: 8px;
    letter-spacing: 0.8px;
    text-transform: uppercase;
    margin-left: 2px;
  }

  /* Pool body ───── */
  .pool-body {
    border-top: 1px solid var(--border);
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 18px;
    background: var(--bg);
  }

  .pool-info-grid {
    display: grid;
    grid-template-columns: repeat(6, 1fr);
    gap: 1px;
    background: var(--border);
    border: 1px solid var(--border);
  }
  .pig-col {
    background: var(--bg-1);
    padding: 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;
  }
  .pig-label {
    font-size: 9px;
    color: var(--fg-mute);
    text-transform: uppercase;
    letter-spacing: 1.2px;
  }
  .pig-value {
    font-size: 12px;
    color: var(--fg);
    font-weight: 600;
    font-feature-settings: "tnum";
    display: flex;
    align-items: center;
    gap: 6px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .pig-value.mono { font-family: var(--font-mono); }
  .pig-value.sm { font-size: 10px; }
  .pig-value.warn { color: var(--warn); }
  .pig-value.crit { color: var(--crit); }

  /* Disk table ───── */
  .pd-head {
    font-size: 10px;
    color: var(--fg-mute);
    text-transform: uppercase;
    letter-spacing: 1.3px;
    margin-bottom: 8px;
    padding: 0 2px;
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .pd-head .todo {
    font-size: 9px;
    text-transform: none;
    letter-spacing: 0.3px;
  }

  .disk-table {
    display: flex;
    flex-direction: column;
    gap: 1px;
    background: var(--border);
    border: 1px solid var(--border);
  }
  .disk-thead, .disk-row {
    display: grid;
    gap: 10px;
    padding: 8px 12px;
    background: var(--bg-1);
    align-items: center;
  }

  /* Grids por variante de tabla — selectores directos y explícitos */

  /* 6 col · Discos dentro de pool expandido (D1 icon, modelo, dev, cap, rol, SMART) */
  .disk-table.cols-6-pool .disk-thead,
  .disk-table.cols-6-pool .disk-row {
    grid-template-columns: 40px 1fr 110px 80px 80px 140px;
  }

  /* 5 col · Discos asignados / libres / USB (dev, modelo, cap, pool-o-tipo, estado) */
  .disk-table.cols-5-disk .disk-thead,
  .disk-table.cols-5-disk .disk-row {
    grid-template-columns: 130px 1fr 100px 120px 130px;
  }

  /* 6 col · Discos asignados con columna Acción (dev, modelo, cap, pool, smart, accion) */
  .disk-table.cols-6-assigned .disk-thead,
  .disk-table.cols-6-assigned .disk-row {
    grid-template-columns: 130px 1fr 90px 100px 110px 200px;
  }

  /* 6 col · Discos libres con columna Acción (dev, modelo, cap, tipo, estado, accion) */
  .disk-table.cols-6-free .disk-thead,
  .disk-table.cols-6-free .disk-row {
    grid-template-columns: 120px 1fr 90px 70px 100px 230px;
  }

  /* 5 col · Scrub (pool, tipo, tamaño, last scrub, acción) */
  .disk-table.cols-5-scrub .disk-thead,
  .disk-table.cols-5-scrub .disk-row {
    grid-template-columns: 1fr 80px 100px 140px 160px;
  }

  /* 4 col · Snapshots (nombre, usado, creado, acciones) */
  .disk-table.cols-4-snap .disk-thead,
  .disk-table.cols-4-snap .disk-row {
    grid-template-columns: 1fr 90px 160px 90px;
  }

  /* 6 col · SMART (dev, modelo, cap, pool, SMART, notas) */
  .disk-table.cols-6-smart .disk-thead,
  .disk-table.cols-6-smart .disk-row {
    grid-template-columns: 130px 1fr 90px 100px 130px 1fr;
  }

  .disk-thead {
    font-size: 9px;
    color: var(--fg-mute);
    text-transform: uppercase;
    letter-spacing: 1.2px;
    background: var(--bg-2);
  }
  .disk-row {
    font-size: 11px;
    color: var(--fg);
    font-feature-settings: "tnum";
  }
  .disk-idx {
    color: var(--accent);
    font-weight: 700;
    font-size: 11px;
  }
  .disk-cell {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .disk-cell.mono { font-family: var(--font-mono); }

  /* ═══ B4 · Pool group + acciones por disco ═══ */
  .pool-group {
    margin-bottom: 18px;
  }
  .pool-group-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 8px 12px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-bottom: none;
  }
  .pool-group-title {
    display: flex;
    align-items: center;
    gap: 8px;
    font-family: var(--font-mono);
  }
  /* La tabla siguiente al head se pega visualmente */
  .pool-group-head + .disk-table {
    border-top-left-radius: 0;
    border-top-right-radius: 0;
  }

  .disk-actions {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    overflow: visible;
  }
  .disk-action-btn {
    padding: 3px 8px;
    font-family: var(--font-mono);
    font-size: 9px;
    letter-spacing: 0.8px;
    text-transform: uppercase;
    background: var(--bg-2);
    border: 1px solid var(--border-bright);
    color: var(--fg-dim);
    cursor: pointer;
    transition: all 0.12s;
    clip-path: polygon(
      0 0, calc(100% - 4px) 0, 100% 4px,
      100% 100%, 4px 100%, 0 calc(100% - 4px)
    );
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .disk-action-btn:hover:not(:disabled) {
    border-color: var(--accent);
    color: var(--accent);
  }
  .disk-action-btn.primary {
    border-color: var(--accent);
    color: var(--accent);
    background: var(--accent-dim, rgba(255,145,68,0.05));
  }
  .disk-action-btn.primary:hover:not(:disabled) {
    background: rgba(255, 145, 68, 0.12);
  }
  .disk-action-btn.warn {
    border-color: var(--border-bright);
    color: var(--warn);
  }
  .disk-action-btn.warn:hover:not(:disabled) {
    border-color: var(--crit);
    color: var(--crit);
    background: rgba(255, 90, 90, 0.04);
  }
  .disk-action-btn:disabled {
    opacity: 0.35;
    cursor: not-allowed;
  }
  .action-tag {
    font-size: 8px;
    color: var(--fg-faint);
    margin-left: 2px;
  }

  .dialog-err {
    padding: 10px 12px;
    background: rgba(255, 90, 90, 0.08);
    border-left: 3px solid var(--crit);
    font-size: 11px;
    color: var(--crit);
    font-family: var(--font-mono);
    letter-spacing: 0.3px;
    margin-top: 4px;
  }

  /* Snapshots list ───── */
  .snap-list {
    display: flex;
    flex-direction: column;
    gap: 1px;
    background: var(--border);
    border: 1px solid var(--border);
  }
  .snap-row {
    padding: 6px 12px;
    background: var(--bg-1);
    display: flex;
    align-items: center;
    gap: 14px;
    font-size: 10px;
  }
  .snap-more {
    padding: 6px 12px;
    background: var(--bg-2);
    font-size: 10px;
    text-align: center;
  }

  .snap-block {
    margin-bottom: 20px;
  }
  .snap-block-head {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 0;
    border-bottom: 1px solid var(--border);
    margin-bottom: 10px;
    font-family: var(--font-mono);
    font-size: 12px;
  }

  /* Alerts ───── */
  .alerts-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .alert-row {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 10px 14px;
    background: var(--bg-1);
    border: 1px solid var(--border);
    border-left: 2px solid var(--fg-mute);
    font-family: var(--font-mono);
  }
  .alert-row.warn { border-left-color: var(--warn); background: rgba(255,184,0,0.04); }
  .alert-row.crit { border-left-color: var(--crit); background: rgba(255,90,90,0.04); }
  .alert-body {
    display: flex;
    flex-direction: column;
    gap: 3px;
    flex: 1;
    min-width: 0;
  }
  .alert-msg {
    font-size: 11px;
    color: var(--fg);
    letter-spacing: 0.3px;
  }
  .alert-meta {
    font-size: 9px;
    color: var(--fg-mute);
  }

  /* Hint box ───── */
  .hint-box {
    background: var(--bg-1);
    border: 1px solid var(--border);
    border-left: 2px solid var(--info);
    padding: 10px 14px;
    font-family: var(--font-sans);
    font-size: 11px;
    color: var(--fg-dim);
    line-height: 1.5;
    margin-bottom: 14px;
  }
  .hint-box b { color: var(--fg); font-weight: 600; }

  .todo-note {
    margin-top: 14px;
    padding: 8px 12px;
    background: rgba(255,184,0,0.04);
    border: 1px dashed var(--warn);
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--warn);
  }
  .todo-note b { letter-spacing: 1px; }

  /* Messages ───── */
  .msg {
    font-family: var(--font-mono);
    font-size: 10px;
    padding: 8px 12px;
    background: rgba(0, 255, 159, 0.04);
    border-left: 2px solid var(--accent);
    color: var(--accent);
    margin-top: 10px;
  }
  .msg.error {
    background: rgba(255, 90, 90, 0.04);
    border-left-color: var(--crit);
    color: var(--crit);
  }

  /* Utility ───── */
  .mono { font-family: var(--font-mono); }
  .sm { font-size: 10px; }
  .tc-accent { color: var(--accent); }
  .tc-warn { color: var(--warn); }
  .tc-crit { color: var(--crit); }
  .tc-mute { color: var(--fg-mute); }
  .tc-dim { color: var(--fg-dim); }
  .k { color: var(--fg-faint); }
  .v { color: var(--fg-dim); font-feature-settings: "tnum"; }
  .sep { color: var(--fg-faint); }

  /* ─── Observados (Bloque C3.2) ─── */

  .observed-list {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .observed-card {
    background: var(--bg-1);
    border: 1px solid var(--border);
    border-left: 3px solid var(--warn);
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .obs-head {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .obs-title {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .obs-label {
    font-weight: 600;
    color: var(--fg);
  }

  .obs-uuid {
    font-size: 11px;
  }

  .obs-info {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .obs-row {
    display: flex;
    gap: 8px;
    font-size: 13px;
  }

  .obs-row .tc-mute {
    min-width: 90px;
  }

  .obs-devices {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .obs-devices-label {
    font-size: 12px;
  }

  .obs-devices-list {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }

  .obs-disk-pill {
    background: var(--bg-2);
    padding: 2px 8px;
    border: 1px solid var(--border);
    font-size: 12px;
  }

  .obs-actions {
    display: flex;
    gap: 8px;
    padding-top: 8px;
    border-top: 1px solid var(--border);
  }

  .divergences {
    margin-top: 12px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .div-row {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 8px 12px;
    border-left: 2px solid var(--warn);
    background: var(--bg-1);
    font-size: 13px;
  }

  .div-row.crit {
    border-left-color: var(--crit);
  }

  /* ─── Bloque C3.3: indicadores en lista de discos ─── */

  .disk-row.has-orphan {
    border-left: 2px solid var(--warn);
  }

  .disk-orphan-hint {
    margin-top: 2px;
    font-size: 11px;
    line-height: 1.3;
  }

  .sm {
    font-size: 11px;
  }
</style>
