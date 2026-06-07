<script>
  /**
   * Storage · Widget de pools · NimOS Beta 8.1
   * ───────────────────────────────────────────
   * Lista LOS POOLS QUE EXISTEN, nada más ni nada menos (decisión
   * jun 2026). Cada fila: riel de salud + nombre + capacidad + barra.
   *
   * Datos: /api/storage/v2/pools vía topic 'storage' (15s).
   * OJO forma: respuesta ENVUELTA del módulo v2 → { data: [Pool] }.
   * Pool: { name, mounted, usage: {usage_percent, used_bytes,
   *         total_bytes}, health: {status} }.
   * health.status ∈ healthy | at_risk | unstable | degraded | critical
   *
   * Nota: el endpoint es requireAdmin — para un usuario no-admin el
   * fetch falla y el widget queda en skeleton. Comportamiento
   * aceptado: los pools son administración.
   *
   * Tallas: 2×1 → 2 filas visibles · 2×2 → 5. Si hay más pools que
   * filas, indicador "+N más" (el detalle es trabajo de la app
   * Storage, el widget es el vistazo).
   */
  import { onMount } from 'svelte';
  import { topicStore, acquire } from '$lib/stores/widgetData.js';

  export const w = 2; // las dos tallas (2×1/2×2) comparten anchura · contrato
  export let h = 1;

  const data = topicStore('storage');
  onMount(() => acquire('storage'));

  $: pools = Array.isArray($data?.data) ? $data.data : null;
  $: maxRows = h >= 2 ? 5 : 2;
  $: visible = pools ? pools.slice(0, maxRows) : [];
  $: extra = pools ? Math.max(0, pools.length - maxRows) : 0;
  $: anyBad = (pools || []).some(p => railClass(p) !== '');

  // health.status → clase de riel
  function railClass(p) {
    const s = p?.health?.status;
    if (!p?.mounted || s === 'critical') return 'crit';
    if (s === 'at_risk' || s === 'unstable' || s === 'degraded') return 'warn';
    return '';
  }

  function barClass(pct) {
    if (pct >= 90) return 'crit';
    if (pct >= 80) return 'hot';
    return '';
  }

  function fmtBytes(b) {
    if (b == null) return '—';
    const TB = 1099511627776, GB = 1073741824;
    if (b >= TB) return (b / TB).toFixed(1) + ' TB';
    return (b / GB).toFixed(0) + ' GB';
  }
</script>

<div class="storage">
  <div class="head">
    <span class="title">Almacenamiento</span>
    <span class="aux" class:bad={anyBad}>
      {#if !pools}—{:else if pools.length === 0}sin pools{:else if anyBad}atención{:else}OK{/if}
    </span>
  </div>

  {#if !pools}
    <!-- skeleton: sin datos aún (o sin permisos de admin) -->
    <div class="empty">— · —</div>
  {:else if pools.length === 0}
    <div class="empty">no hay pools creados</div>
  {:else}
    <div class="list">
      {#each visible as p (p.id ?? p.name)}
        {@const pct = p.usage?.usage_percent ?? 0}
        <div class="pool {railClass(p)}">
          <div class="top">
            <span class="name">{p.name}{#if !p.mounted}<small> · sin montar</small>{/if}</span>
            <span class="cap">{fmtBytes(p.usage?.used_bytes)} / {fmtBytes(p.usage?.total_bytes)}</span>
          </div>
          <div class="bar"><i class={barClass(pct)} style="width:{pct}%"></i></div>
        </div>
      {/each}
      {#if extra > 0}
        <div class="more">+{extra} más</div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .storage {
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
    margin-bottom: 9px;
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
    text-transform: uppercase;
    color: var(--signal);
  }
  .aux.bad { color: var(--warn); }

  .list {
    flex: 1;
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 9px;
  }

  /* fila de pool · riel de salud lateral */
  .pool {
    position: relative;
    padding-left: 9px;
  }
  .pool::before {
    content: '';
    position: absolute;
    left: 0; top: 1px; bottom: 1px;
    width: 2px;
    border-radius: 2px;
    background: var(--signal);
    box-shadow: 0 0 6px var(--signal-glow);
  }
  .pool.warn::before { background: var(--warn); box-shadow: 0 0 6px var(--warn); }
  .pool.crit::before { background: var(--crit); box-shadow: 0 0 6px var(--crit); }

  .top {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    margin-bottom: 5px;
  }
  .name {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--ink-dim);
  }
  .name small { font-size: 9px; color: var(--warn); }
  .cap {
    font-family: var(--font-mono);
    font-size: 9.5px;
    color: var(--ink-faint);
  }

  .bar {
    height: 6px;
    border-radius: 4px;
    background: rgba(255, 255, 255, 0.06);
    overflow: hidden;
  }
  .bar i {
    display: block;
    height: 100%;
    border-radius: 4px;
    background: linear-gradient(90deg, var(--signal), hsl(155, 100%, 42%));
    box-shadow: 0 0 10px var(--signal-glow);
    transition: width 0.6s cubic-bezier(0.4, 0, 0.2, 1);
  }
  .bar i.hot {
    background: linear-gradient(90deg, var(--warn), #f59e0b);
    box-shadow: 0 0 10px var(--warn);
  }
  .bar i.crit {
    background: linear-gradient(90deg, var(--crit), #ef4444);
    box-shadow: 0 0 10px var(--crit);
  }

  .more {
    font-family: var(--font-mono);
    font-size: 8.5px;
    color: var(--ink-faint);
    text-align: center;
  }
  .empty {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--ink-faint);
  }
</style>
