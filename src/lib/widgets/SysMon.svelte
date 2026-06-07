<script>
  /**
   * SysMon · Widget Sistema (CPU + RAM) · NimOS Beta 8.1
   * ─────────────────────────────────────────────────────
   * Primer widget con datos del daemon — referencia del camino:
   *   topicStore('system') para leer · acquire('system') en onMount
   *   (su retorno es el cleanup) · render digno con null (skeleton).
   *
   * Datos: /api/hardware/stats vía widgetData (3s, compartido por
   * refcount con los widgets CPU/RAM).
   * Forma: { cpu: {percent, cores, load1}, memory: {percent,
   *          usedGB, totalGB} } — respuesta plana, sin wrapper.
   */
  import { onMount } from 'svelte';
  import { topicStore, acquire } from '$lib/stores/widgetData.js';
  import Ring from './parts/Ring.svelte';

  export const w = 2; // talla única · contrato
  export const h = 1;

  const data = topicStore('system');
  onMount(() => acquire('system'));

  $: cpu = $data?.cpu ?? null;
  $: mem = $data?.memory ?? null;
</script>

<div class="sysmon">
  <div class="head">
    <span class="title">Sistema</span>
    <span class="aux">{cpu ? `load ${(cpu.load1 ?? 0).toFixed(2)}` : '—'}</span>
  </div>
  <div class="rings">
    <div class="wrap">
      <Ring pct={cpu?.percent ?? null} label="CPU" />
      <div class="sub">{cpu ? `${cpu.cores} cores` : ' '}</div>
    </div>
    <div class="wrap">
      <Ring pct={mem?.percent ?? null} label="RAM" />
      <div class="sub">{mem ? `${mem.usedGB}/${mem.totalGB}G` : ' '}</div>
    </div>
  </div>
</div>

<style>
  .sysmon {
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 13px 14px;
    user-select: none;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 8px;
  }
  .title {
    font-family: var(--font-mono);
    font-size: 9.5px;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--ink-faint);
  }
  .aux {
    font-family: var(--font-mono);
    font-size: 9px;
    letter-spacing: 0.06em;
    color: var(--signal);
  }
  .rings {
    flex: 1;
    display: flex;
    gap: 26px;
    align-items: center;
    justify-content: center;
  }
  .wrap {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
  }
  .sub {
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--ink-faint);
    letter-spacing: 0.03em;
    min-height: 11px;
  }
</style>
