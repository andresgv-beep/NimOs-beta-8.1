<script>
  /**
   * Storage · Widget de pools · NimOS Beta 8.1 (rediseño jun 2026)
   * ──────────────────────────────────────────────────────────────
   * Ficha por pool estilo "panel": nombre + barra de capacidad ancha
   * + 4 cards-caja (Usado / Disponible / Tipo / Salud). Sin riel
   * lateral. Números grandes; el % va en ámbar como acento.
   *
   * config.pools (de WidgetConfig):
   *   - 1×1 / 2×1 → UN pool (primero de config.pools, o el primero
   *     disponible si está vacío).
   *   - 2×2 → varios (config.pools o todos); pensado para ≥2 pools.
   *     [El layout fino del 2×2 se ajustará con su diseño propio.]
   *
   * Datos: /api/storage/v2/pools vía topic 'storage' (15s).
   * Forma ENVUELTA: { data: [Pool] }. Pool: { name, profile, mounted,
   *   usage:{usage_percent,used_bytes,available_bytes,total_bytes},
   *   health:{status} }.
   * health.status ∈ healthy | at_risk | unstable | degraded | critical
   */
  import { onMount } from 'svelte';
  import { topicStore, acquire } from '$lib/stores/widgetData.js';

  export const w = 2; // ambas tallas comparten anchura · contrato
  export let h = 1;
  export let config = {}; // { pools: [name,...] } · [] o ausente = auto

  const data = topicStore('storage');
  onMount(() => acquire('storage'));

  $: allPools = Array.isArray($data?.data) ? $data.data : null;
  $: multi = h >= 2;

  $: wanted = Array.isArray(config?.pools) ? config.pools : [];
  $: shown = (() => {
    if (!allPools) return null;
    let sel = wanted.length
      ? allPools.filter(p => wanted.includes(p.name))
      : allPools;
    if (!multi) sel = sel.slice(0, 1);
    return sel;
  })();

  $: anyBad = (allPools || []).some(p => healthClass(p) !== 'ok');

  function healthClass(p) {
    const s = p?.health?.status;
    if (!p?.mounted || s === 'critical') return 'crit';
    if (s === 'at_risk' || s === 'unstable' || s === 'degraded') return 'warn';
    return 'ok';
  }
  function barClass(pct) {
    if (pct >= 90) return 'crit';
    if (pct >= 80) return 'hot';
    return '';
  }
  // "54 GB" / "3.6 TB" → { n, u } para número grande + unidad pequeña
  function split(b) {
    if (b == null) return { n: '—', u: '' };
    const TB = 1099511627776, GB = 1073741824;
    if (b >= TB) return { n: (b / TB).toFixed(1), u: 'TB' };
    return { n: (b / GB).toFixed(0), u: 'GB' };
  }
  function fmtBytes(b) {
    const s = split(b);
    return s.u ? `${s.n} ${s.u}` : s.n;
  }
</script>

<div class="storage">
  <div class="head">
    <span class="title">Almacenamiento</span>
    <span class="aux" class:bad={anyBad}>
      {#if !allPools}—{:else if allPools.length === 0}sin pools{:else if anyBad}atención{:else}OK{/if}
    </span>
  </div>

  {#if !shown}
    <div class="empty">— · —</div>
  {:else if shown.length === 0}
    <div class="empty">sin pool seleccionado</div>
  {:else}
    <div class="list" class:scroll={multi}>
      {#each shown as p (p.id ?? p.name)}
        {@const pct = p.usage?.usage_percent ?? 0}
        {@const hc = healthClass(p)}
        {@const used = split(p.usage?.used_bytes)}
        {@const avail = split(p.usage?.available_bytes)}
        <div class="pool">
          <div class="pool-head">
            <span class="name">{p.name}{#if !p.mounted}<small> · sin montar</small>{/if}</span>
            <span class="cap">{fmtBytes(p.usage?.used_bytes)} / {fmtBytes(p.usage?.total_bytes)}</span>
          </div>

          <div class="bar"><i class={barClass(pct)} style="width:{pct}%"></i></div>

          <div class="cards">
            <div class="c">
              <span class="c-label">Usado</span>
              <span class="c-num">{used.n}<small>{used.u}</small></span>
              <span class="c-sub accent">{pct}%</span>
            </div>
            <div class="c">
              <span class="c-label">Disponible</span>
              <span class="c-num">{avail.n}<small>{avail.u}</small></span>
              <span class="c-sub accent">{100 - pct}%</span>
            </div>
            <div class="c">
              <span class="c-label">Tipo</span>
              <span class="c-num">BTRFS</span>
              <span class="c-sub accent">{p.profile ?? '—'}</span>
            </div>
            <div class="c">
              <span class="c-label">Salud</span>
              <span class="c-shield {hc}" aria-hidden="true">
                {#if hc === 'ok'}
                  <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8">
                    <path d="M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6Z" />
                    <path d="M9 12l2 2 4-4" />
                  </svg>
                {:else}
                  <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8">
                    <path d="M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6Z" />
                    <path d="M12 8v4M12 15h.01" />
                  </svg>
                {/if}
              </span>
            </div>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .storage {
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 15px 17px;
    user-select: none;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 14px;
  }
  .title {
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 0.16em;
    text-transform: uppercase;
    color: var(--ink-faint);
  }
  .aux {
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--signal);
  }
  .aux.bad { color: var(--warn); }

  .list {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 14px;
    min-height: 0;
    justify-content: center;
  }
  .list.scroll { overflow-y: auto; justify-content: flex-start; padding-right: 4px; }
  .list.scroll::-webkit-scrollbar { width: 5px; }
  .list.scroll::-webkit-scrollbar-thumb { background: var(--line-bright); border-radius: 3px; }

  .pool { flex-shrink: 0; }

  .pool-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    margin-bottom: 9px;
  }
  .name {
    font-family: var(--font-mono);
    font-size: 16px;
    color: var(--ink);
  }
  .name small { font-size: 10px; color: var(--warn); }
  .cap {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--ink-mute);
  }

  .bar {
    height: 8px;
    border-radius: 5px;
    background: rgba(255, 255, 255, 0.07);
    overflow: hidden;
    margin-bottom: 14px;
  }
  .bar i {
    display: block;
    height: 100%;
    border-radius: 5px;
    background: linear-gradient(90deg, var(--signal), hsl(155, 100%, 42%));
    box-shadow: 0 0 12px var(--signal-glow);
    transition: width 0.6s cubic-bezier(0.4, 0, 0.2, 1);
  }
  .bar i.hot  { background: linear-gradient(90deg, var(--warn), #f59e0b); box-shadow: 0 0 12px var(--warn); }
  .bar i.crit { background: linear-gradient(90deg, var(--crit), #ef4444); box-shadow: 0 0 12px var(--crit); }

  /* cards-caja independientes */
  .cards {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 9px;
  }
  .c {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 5px;
    padding: 11px 6px;
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: var(--bev-md);
    min-height: 78px;
  }
  .c-label {
    font-family: var(--font-mono);
    font-size: 8.5px;
    letter-spacing: 0.12em;
    text-transform: uppercase;
    color: var(--ink-faint);
  }
  .c-num {
    font-family: var(--font-mono);
    font-size: 19px;
    font-weight: 600;
    color: var(--ink);
    line-height: 1;
  }
  .c-num small {
    font-size: 11px;
    font-weight: 500;
    color: var(--ink-mute);
    margin-left: 2px;
  }
  .c-sub {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--ink-faint);
  }
  .c-sub.accent { color: var(--warn); }
  .c-shield { display: flex; align-items: center; justify-content: center; }
  .c-shield.ok   { color: var(--signal); }
  .c-shield.warn { color: var(--warn); }
  .c-shield.crit { color: var(--crit); }

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
