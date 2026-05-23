<script>
  /**
   * StorageSnapshots · Vista de snapshots por pool.
   * ────────────────────────────────────────────────
   * Lista snapshots de pools (actualmente sólo ZFS — código vestigial: el
   * proyecto es BTRFS-only desde mayo 2026 y el soporte BTRFS de snapshots
   * está marcado como "Fase B"). Sin acciones activas hoy: crear/rollback/
   * eliminar están deshabilitados.
   *
   * Props:
   *   · pools     — array de pools del backend
   *   · snapshots — { [poolName]: [snapshot, ...] } cargados lazily
   *
   * Eventos:
   *   · load — { detail: { poolName } } — solicitar carga de snapshots
   */
  import { createEventDispatcher } from 'svelte';
  import { SectionHead, BevelButton, IconButton, EmptyState } from '$lib/ui';
  import { fmtBytes, fmtDate } from './formatters.js';
  import './views-styles.css';

  export let pools = [];
  export let snapshots = {};

  const dispatch = createEventDispatcher();

  $: zfsPools = pools.filter(p => p.type === 'zfs' || p.filesystem === 'zfs');
</script>

<div class="st-section">
  <SectionHead>Snapshots</SectionHead>

  {#if pools.length === 0}
    <EmptyState icon="◇" title="Sin pools configurados" hint="Crea o restaura un pool ZFS para gestionar snapshots" />
  {:else}
    {#each zfsPools as pool}
      <div class="snap-block">
        <div class="snap-block-head">
          <div class="pool-head-icon sm">◆</div>
          <span class="mono">{pool.name}</span>
          {#if !snapshots[pool.name]}
            <BevelButton size="sm" onClick={() => dispatch('load', { poolName: pool.name })}>Cargar</BevelButton>
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

    {#if zfsPools.length === 0}
      <EmptyState icon="!" title="Sin pools ZFS" hint="Los snapshots solo están disponibles en pools ZFS. Tus pools son BTRFS." />
    {/if}
  {/if}
</div>

<style>
  /* Los siguientes selectores son específicos de esta vista (no se usan en
     otras), por eso van scoped aquí en vez de en views-styles.css. */
  .snap-block {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-bottom: 16px;
  }

  .snap-block-head {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 0;
  }

  .pool-head-icon {
    color: var(--accent);
  }
</style>
