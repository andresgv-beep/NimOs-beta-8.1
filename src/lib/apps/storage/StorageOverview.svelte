<script>
  /**
   * StorageOverview · Vista principal de almacenamiento.
   * ─────────────────────────────────────────────────────
   * Tres secciones verticales:
   *   1. Lista de pools (expandibles, con kebab → toolbar inline)
   *   2. Observados — filesystems BTRFS huérfanos (si hay)
   *   3. Alertas del sistema (si hay)
   *
   * Estado UI propio (no leak al padre):
   *   · expandedPools — Set de pool names expandidos
   *   · kebabOpenFor  — pool name con kebab abierto (uno solo a la vez)
   *
   * Click-outside listener registrado en onMount/onDestroy cierra el kebab
   * al pulsar fuera.
   *
   * Props (datos del padre):
   *   · pools, disks, alerts, orphanFilesystems, divergences, snapshots
   *   · scanning, refreshing, scrubbing, scrubMsg
   *
   * Eventos (acciones que requieren orquestación del padre):
   *   · rescan            — re-escanea buses
   *   · create-pool       — abrir wizard
   *   · refresh-observed  — forzar re-scan del observer
   *   · scrub             { poolName } — disparar scrub
   *   · export-pool       { poolName } — abrir export wizard
   *   · import-orphan     { fs } — abrir import modal
   *   · destroy-orphan    { fs } — abrir destroy modal
   *   · load-snapshots    { poolName } — cargar snapshots lazy al expandir
   */
  import { createEventDispatcher, onMount, onDestroy } from 'svelte';
  import {
    SectionHead, Badge, LED, EmptyState, StripeProgressBar,
  } from '$lib/ui';
  import {
    fmtBytes, fmtDate, inferDiskRole,
    healthLabel, healthVariant,
    usageVariant, ledVariantForHealth, smartVariant,
  } from './formatters.js';
  import './views-styles.css';

  export let pools = [];
  export let disks = {};
  export let alerts = [];
  export let orphanFilesystems = [];
  export let divergences = [];
  export let snapshots = {};
  export let scanning = false;
  export let refreshing = false;
  export let scrubbing = {};
  export let scrubMsg = '';

  const dispatch = createEventDispatcher();

  // ─── UI state interno ────────────────────────────────────────────
  let expandedPools = new Set();
  let kebabOpenFor = null;

  function togglePoolExpand(poolName) {
    kebabOpenFor = null;
    if (expandedPools.has(poolName)) {
      expandedPools.delete(poolName);
    } else {
      expandedPools.add(poolName);
      dispatch('load-snapshots', { poolName });
    }
    expandedPools = expandedPools; // reactivity trigger
  }

  function toggleKebab(poolName, event) {
    event.stopPropagation();
    kebabOpenFor = kebabOpenFor === poolName ? null : poolName;
  }

  // Click outside → cerrar kebab
  function onDocClick() {
    kebabOpenFor = null;
  }

  onMount(() => {
    window.addEventListener('click', onDocClick);
  });
  onDestroy(() => {
    window.removeEventListener('click', onDocClick);
  });
</script>

<!-- ══ Sección: Volúmenes (pools) ══ -->
<div class="st-section">
  <div class="section-row">
    <SectionHead count={pools.length > 0 ? `· ${pools.length} activos` : ''}>
      Volúmenes
    </SectionHead>
    <div class="section-actions">
      <button class="btn-secondary" on:click={() => dispatch('rescan')} disabled={scanning}>
        {scanning ? '▸ Escaneando...' : '↻ Escanear'}
      </button>
      <button
        class="btn-primary"
        on:click={() => dispatch('create-pool')}
        disabled={!(disks.eligible?.length > 0)}
        title={disks.eligible?.length > 0
          ? 'Crear un nuevo pool de almacenamiento'
          : 'No hay discos libres para crear un pool'}
      >
        + Nuevo volumen
      </button>
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

          <!-- Toolbar inline de acciones -->
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
                on:click={() => { dispatch('scrub', { poolName: pool.name }); kebabOpenFor = null; }}
                disabled={scrubbing[pool.name]}
              >
                <span class="pa-num">02</span>
                <span>{scrubbing[pool.name] ? 'Iniciando...' : 'Verificar integridad'}</span>
              </button>
              <button
                class="pa-btn"
                on:click={() => { dispatch('export-pool', { poolName: pool.name }); kebabOpenFor = null; }}
              >
                <span class="pa-num">03</span>
                <span>Desmontar</span>
              </button>
            </div>
          {/if}

          <!-- Pool expanded body -->
          {#if expandedPools.has(pool.name)}
            <div class="pool-body">

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

              <!-- Snapshots resumen (top 5) -->
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

<!-- ══ Sección: Observados (orphan BTRFS) ══ -->
{#if orphanFilesystems.length > 0}
  <div class="st-section">
    <div class="section-row">
      <SectionHead count="· {orphanFilesystems.length}">
        Observados · no gestionados
      </SectionHead>
      <div class="section-actions">
        <button class="btn-secondary" on:click={() => dispatch('refresh-observed')} disabled={refreshing}>
          {refreshing ? '▸ Actualizando...' : '↻ Refrescar'}
        </button>
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
            <button
              class="btn-primary"
              on:click={() => dispatch('import-orphan', { fs })}
              disabled={fs.devices_missing > 0}
              title={fs.devices_missing > 0
                ? 'No se puede importar: faltan discos'
                : 'Importar como pool gestionado (preserva datos)'}
            >
              ⬇ Importar como pool
            </button>
            <button
              class="btn-secondary"
              on:click={() => dispatch('destroy-orphan', { fs })}
              title="DESTRUIR — borra todos los datos de los discos"
            >
              ⚠ Destruir
            </button>
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

<!-- ══ Sección: Alertas del sistema ══ -->
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

<style>
  /* CSS específico de esta vista (no usado en otras) */

  /* Pool card ───── */
  .pools {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .pool {
    background: var(--bg-card);
    border: 1px solid var(--line);
    border-radius: 10px;
    font-family: var(--font-mono);
    transition: border-color 0.12s, background 0.12s;
    overflow: hidden;
  }
  .pool.open { border-color: rgba(255, 255, 255, 0.14); }
  .pool.degraded { border-left: 3px solid var(--warn); }
  .pool.crit { border-left: 3px solid var(--crit); }

  .pool-head {
    display: grid;
    grid-template-columns: 24px 1fr 220px 80px 18px 18px 24px;
    gap: 16px;
    align-items: center;
    padding: 12px 16px;
    cursor: pointer;
    user-select: none;
  }
  .pool-head:hover { background: var(--side-hover); }

  .pool-head-icon {
    color: var(--signal);
    font-size: 14px;
    text-align: center;
  }

  .pool-ident {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .pool-name {
    font-size: 13px;
    color: var(--ink);
    font-weight: 600;
    letter-spacing: 0.3px;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .pool-meta {
    font-size: 10px;
    color: var(--ink-mute);
    letter-spacing: 0.3px;
  }

  .pool-bar-wrap { min-width: 0; }
  .pool-size {
    font-size: 11px;
    color: var(--ink);
    text-align: right;
    font-feature-settings: "tnum";
  }
  .pool-status {
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .pool-chev {
    color: var(--ink-mute);
    font-size: 14px;
    transition: transform 0.15s;
    text-align: center;
  }
  .pool-chev.rot { transform: rotate(90deg); color: var(--signal); }

  .pool-kebab {
    width: 24px;
    height: 24px;
    background: transparent;
    border: none;
    color: var(--ink-mute);
    cursor: pointer;
    font-size: 14px;
    font-family: var(--font-mono);
    transition: color 0.12s;
  }
  .pool-kebab:hover { color: var(--signal); }
  .pool-kebab.active {
    color: var(--signal);
    background: var(--side-hover);
  }

  /* Toolbar inline ───── */
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

  /* Disk table header ───── */
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

  /* Observed list ───── */
  .observed-list {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .observed-card {
    background: var(--bg-card);
    border: 1px solid var(--line);
    border-left: 3px solid var(--warn);
    border-radius: 10px;
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
    color: var(--ink);
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
    background: var(--bg-inner);
    padding: 2px 8px;
    border: 1px solid var(--line);
    border-radius: 3px;
    font-size: 12px;
    color: var(--ink-dim);
  }

  .obs-actions {
    display: flex;
    gap: 8px;
    padding-top: 12px;
    border-top: 1px solid var(--line);
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

  /* ─── Botones (Design System Beta 8.1) ─── */
  .btn-secondary {
    padding: 5px 12px;
    border-radius: 5px;
    border: 1px solid var(--line);
    background: var(--bg-card);
    color: var(--ink-dim);
    font-size: 10px;
    font-weight: 500;
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.4px;
    cursor: pointer;
    transition: background 0.12s, color 0.12s, border-color 0.12s;
  }
  .btn-secondary:hover:not(:disabled) {
    color: var(--ink);
    background: var(--side-hover);
  }
  .btn-secondary:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .btn-primary {
    padding: 5px 12px;
    border-radius: 5px;
    border: 1px solid rgba(0, 255, 159, 0.3);
    background: rgba(0, 255, 159, 0.06);
    color: var(--signal);
    font-size: 10px;
    font-weight: 600;
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.4px;
    cursor: pointer;
    transition: background 0.12s, border-color 0.12s;
  }
  .btn-primary:hover:not(:disabled) {
    border-color: var(--signal);
    background: rgba(0, 255, 159, 0.12);
  }
  .btn-primary:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
</style>
