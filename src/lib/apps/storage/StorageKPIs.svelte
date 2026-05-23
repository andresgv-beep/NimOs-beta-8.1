<script>
  /**
   * StorageKPIs · Banner de 4 KPIs encima del scroll de Overview.
   * ──────────────────────────────────────────────────────────────
   * Volúmenes · Discos · Capacidad · Salud
   *
   * Calcula los valores derivados internamente desde pools+disks+alerts.
   *
   * Props:
   *   · pools  — array de pools
   *   · disks  — { eligible, ... }
   *   · alerts — array de alertas
   */
  import { KPICard } from '$lib/ui';
  import { fmtBytes, usageVariant } from './formatters.js';

  export let pools = [];
  export let disks = {};
  export let alerts = [];

  $: totalDisksAssigned = pools.reduce((s, p) => s + (p.devices?.length || 0), 0);
  $: totalDisksFree = (disks.eligible?.length || 0);
  $: totalCapacity = pools.reduce((s, p) => s + (p.usage?.total_bytes || 0), 0);
  $: totalUsed = pools.reduce((s, p) => s + (p.usage?.used_bytes || 0), 0);
  $: totalFree = totalCapacity - totalUsed;
  $: overallUsagePct = totalCapacity > 0 ? Math.round((totalUsed / totalCapacity) * 100) : 0;
  $: overallHealth = pools.every(p => p.mounted && p.health?.status === 'healthy') && alerts.length === 0 ? 'ok'
                   : pools.some(p => !p.mounted || p.health?.status === 'critical') ? 'crit'
                   : 'warn';
</script>

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

<style>
  .st-kpis {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    border-bottom: 1px solid var(--border);
    background: var(--bg-1);
    flex-shrink: 0;
  }
  .st-kpis :global(.kpi) { border-right: 1px solid var(--border); }
  .st-kpis :global(.kpi:last-child) { border-right: none; }
</style>
