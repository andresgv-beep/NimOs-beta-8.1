<script>
  /**
   * WidgetPicker · Ventana de añadir widgets · NimOS Beta 8.1
   * ─────────────────────────────────────────────────────────
   * Panel deslizante (patrón overlay+panel del sistema). Una sección
   * por widget del catálogo que no esté ya activo; dentro, una opción
   * por talla soportada con PREVIEW REAL: el propio componente del
   * widget montado a escala reducida (transform: scale), no un dibujo
   * aparte. Así el preview nunca diverge del widget real.
   *
   * Solo AÑADE. Las acciones sobre un widget ya puesto (config, quitar,
   * talla) viven en el hover de la caja (WidgetLayer). Elegir una talla
   * emite `add` con { id, size } y cierra.
   *
   * Props:
   *   open      — visible
   *   catalog   — WIDGET_CATALOG
   *   activeIds — Set de ids ya colocados (no se ofrecen de nuevo)
   * Eventos:
   *   add   { id, size: [w,h] }
   *   close
   */
  import { createEventDispatcher } from 'svelte';
  import WidgetIcon from '$lib/widgets/parts/WidgetIcon.svelte';
  import { GROUP_ORDER } from '$lib/widgets/index.js';

  export let open = false;
  export let catalog = [];
  export let activeIds = new Set();

  const dispatch = createEventDispatcher();

  // Celda base (igual que WidgetLayer) para escalar el preview.
  const CELL = 144, GAP = 14;
  // Ancho disponible para un preview en el panel (px). El preview se
  // escala para caber aquí manteniendo proporción de la talla real.
  const PREVIEW_MAX_W = 150;

  $: available = catalog.filter(w => !activeIds.has(w.id));

  // Agrupa por familia (group) en el orden de GROUP_ORDER, y dentro
  // de cada familia por `order`. Familias sin nombre caen en 'Otros'.
  $: grouped = (() => {
    const byGroup = {};
    for (const w of available) {
      const g = w.group || 'Otros';
      (byGroup[g] ||= []).push(w);
    }
    const order = [...GROUP_ORDER, 'Otros'];
    return order
      .filter(g => byGroup[g]?.length)
      .map(g => ({
        name: g,
        items: byGroup[g].sort((a, b) => (a.order ?? 0) - (b.order ?? 0)),
      }));
  })();

  function cellPx(cw, ch) {
    return {
      w: cw * CELL + (cw - 1) * GAP,
      h: ch * CELL + (ch - 1) * GAP,
    };
  }
  // Escala para que el preview de talla cw×ch quepa en PREVIEW_MAX_W.
  function scaleFor(cw) {
    const realW = cw * CELL + (cw - 1) * GAP;
    return Math.min(1, PREVIEW_MAX_W / realW);
  }

  function choose(id, size) {
    dispatch('add', { id, size });
    dispatch('close');
  }
  function close() { dispatch('close'); }
</script>

{#if open}
  <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
  <div class="overlay" on:click={close}></div>
  <div class="panel" role="dialog" aria-label="Añadir widget">
    <div class="panel-header">
      <span class="panel-title">Añadir widget</span>
      <button class="x" on:click={close} aria-label="Cerrar">✕</button>
    </div>

    <div class="panel-body">
      {#if available.length === 0}
        <div class="empty">Todos los widgets están en el escritorio.</div>
      {/if}

      {#each grouped as fam (fam.name)}
        <div class="group">
          <div class="group-head">{fam.name}</div>
          {#each fam.items as w (w.id)}
            <section class="sec">
              <div class="sec-head">
                <span class="sec-ic"><WidgetIcon name={w.icon} size={16} /></span>
                <span class="sec-name">{w.name}</span>
                <span class="sec-desc">{w.desc || ''}</span>
              </div>

              <div class="opts">
                {#each (w.sizes || [[w.w, w.h]]) as [cw, ch] (cw + 'x' + ch)}
                  {@const px = cellPx(cw, ch)}
                  {@const sc = scaleFor(cw)}
                  <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
                  <div class="opt" on:click={() => choose(w.id, [cw, ch])}>
                    <div
                      class="frame"
                      style="width:{px.w * sc}px; height:{px.h * sc}px;"
                    >
                      {#if w.component}
                        <div
                          class="scaler"
                          style="
                            width:{px.w}px; height:{px.h}px;
                            transform: scale({sc});
                          "
                        >
                          <svelte:component
                            this={w.component}
                            w={cw} h={ch}
                            {...(w.props || {})}
                            config={{}}
                          />
                        </div>
                      {:else}
                        <div class="ph">{cw}×{ch}</div>
                      {/if}
                    </div>
                    <span class="opt-label">{cw}×{ch}</span>
                  </div>
                {/each}
              </div>
            </section>
          {/each}
        </div>
      {/each}
    </div>
  </div>
{/if}

<style>
  .overlay {
    position: fixed;
    inset: 0;
    z-index: 9700;
    background: rgba(0, 0, 0, 0.45);
    backdrop-filter: blur(2px);
    pointer-events: auto;
  }
  .panel {
    position: fixed;
    top: 0;
    right: 0;
    bottom: 0;
    width: 360px;
    z-index: 9710;
    background: var(--side-bg);
    border-left: 1px solid var(--line);
    box-shadow: -20px 0 50px rgba(0, 0, 0, 0.5);
    display: flex;
    flex-direction: column;
    pointer-events: auto;
    animation: slidein 0.18s cubic-bezier(0.2, 0, 0.2, 1);
  }
  @keyframes slidein {
    from { transform: translateX(100%); }
    to   { transform: translateX(0); }
  }

  .panel-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 16px 18px;
    border-bottom: 1px solid var(--line);
  }
  .panel-title {
    font-family: var(--font-mono);
    font-size: 12px;
    letter-spacing: 0.16em;
    text-transform: uppercase;
    color: var(--ink);
  }
  .x {
    border: none;
    background: transparent;
    color: var(--ink-faint);
    font-size: 14px;
    cursor: pointer;
    padding: 4px 6px;
    border-radius: var(--radius-sm);
  }
  .x:hover { color: var(--ink); background: var(--side-hover); }

  .panel-body {
    flex: 1;
    overflow-y: auto;
    padding: 6px 16px 24px;
  }
  .panel-body::-webkit-scrollbar { width: 6px; }
  .panel-body::-webkit-scrollbar-thumb {
    background: var(--line-bright);
    border-radius: 3px;
  }

  .empty {
    padding: 40px 12px;
    text-align: center;
    font-family: var(--font-sans);
    font-size: 12px;
    color: var(--ink-faint);
  }

  .group { margin-bottom: 4px; }
  .group-head {
    font-family: var(--font-mono);
    font-size: 9.5px;
    letter-spacing: 0.18em;
    text-transform: uppercase;
    color: var(--signal);
    padding: 14px 2px 2px;
  }

  .sec {
    padding: 12px 0;
    border-top: 1px solid var(--line);
  }
  .group .sec:first-of-type { border-top: none; }
  .sec-head {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 12px;
  }
  .sec-ic { color: var(--signal); display: flex; }
  .sec-name {
    font-family: var(--font-sans);
    font-size: 13px;
    font-weight: 600;
    color: var(--ink);
  }
  .sec-desc {
    font-family: var(--font-mono);
    font-size: 9.5px;
    color: var(--ink-faint);
    margin-left: auto;
  }

  .opts {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    align-items: flex-end;
  }
  .opt {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    cursor: pointer;
  }
  .frame {
    border: 1px solid var(--line);
    border-radius: var(--bev-md);
    overflow: hidden;
    background: rgba(20, 20, 26, 0.5);
    position: relative;
    transition: border-color 0.15s ease, box-shadow 0.15s ease;
  }
  .opt:hover .frame {
    border-color: var(--signal);
    box-shadow: 0 0 0 1px var(--signal-dim), 0 6px 18px rgba(0, 0, 0, 0.4);
  }
  .scaler {
    transform-origin: top left;
    pointer-events: none; /* el preview no es interactivo */
  }
  .ph {
    width: 100%;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--ink-faint);
  }
  .opt-label {
    font-family: var(--font-mono);
    font-size: 9.5px;
    color: var(--ink-mute);
  }
  .opt:hover .opt-label { color: var(--signal); }
</style>
