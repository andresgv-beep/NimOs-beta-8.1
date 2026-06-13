<script>
  /**
   * WidgetConfig · Configuración de instancia · NimOS Beta 8.1
   * ──────────────────────────────────────────────────────────
   * Modal pequeño para configurar un widget ya colocado. Hoy cubre
   * `configurable: 'pools'` (Storage): lista los pools disponibles del
   * topic 'storage' y deja elegir cuáles muestra ESA instancia.
   *
   * Contrato de config:
   *   { pools: ['data8', ...] }  — nombres de pools a mostrar.
   *   Lista vacía o ausente = TODOS los pools (comportamiento por
   *   defecto, no rompe instancias viejas sin config).
   *
   * Extensible: `configurable: 'folders'` seguirá el mismo patrón
   * leyendo su propio topic. Por ahora se contempla la rama pero el
   * topic de carpetas se enchufa cuando exista el widget.
   *
   * Props:  def (entrada de catálogo), config (config actual)
   * Eventos: save (nueva config), close
   */
  import { createEventDispatcher, onMount } from 'svelte';
  import { topicStore, acquire } from '$lib/stores/widgetData.js';

  export let def;
  export let config = {};

  const dispatch = createEventDispatcher();

  // Fuente de opciones según el tipo de configurable.
  const topic = def.configurable === 'pools' ? 'storage'
    : def.configurable === 'folders' ? 'folders'
    : null;

  const data = topic ? topicStore(topic) : null;
  onMount(() => { if (topic) return acquire(topic); });

  // Opciones disponibles (pools). Forma envuelta { data: [Pool] }.
  $: options = def.configurable === 'pools'
    ? (Array.isArray($data?.data) ? $data.data.map(p => ({
        key: p.name,
        label: p.name,
        meta: p.usage ? `${(p.usage.usage_percent ?? 0)}%` : '',
        mounted: p.mounted !== false,
      })) : null)
    : [];

  // Selección local (copia editable). [] = todos.
  let selected = Array.isArray(config?.pools) ? [...config.pools] : [];

  $: allMode = selected.length === 0; // vacío = todos

  function toggle(key) {
    if (selected.includes(key)) selected = selected.filter(k => k !== key);
    else selected = [...selected, key];
  }
  function setAll() { selected = []; }

  function apply() {
    // Si selecciona todos los que hay, lo normalizamos a [] (= todos)
    const next = { ...config };
    if (options && selected.length === options.length) next.pools = [];
    else next.pools = [...selected];
    dispatch('save', next);
    dispatch('close');
  }
  function close() { dispatch('close'); }
</script>

<!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
<div class="overlay" on:click={close}></div>
<div class="modal" role="dialog" aria-label="Configurar {def.name}">
  <div class="head">
    <span class="title">Configurar · {def.name}</span>
    <button class="x" on:click={close} aria-label="Cerrar">✕</button>
  </div>

  <div class="body">
    {#if def.configurable === 'pools'}
      <div class="hint">Elige qué pools muestra este widget.</div>

      <button class="row all" class:on={allMode} on:click={setAll}>
        <span class="chk">{allMode ? '✓' : ''}</span>
        <span class="row-label">Todos los pools</span>
        <span class="row-meta">automático</span>
      </button>

      <div class="sep"></div>

      {#if options === null}
        <div class="state">Cargando pools…</div>
      {:else if options.length === 0}
        <div class="state">No hay pools creados.</div>
      {:else}
        {#each options as o (o.key)}
          <button
            class="row"
            class:on={selected.includes(o.key)}
            on:click={() => toggle(o.key)}
          >
            <span class="chk">{selected.includes(o.key) ? '✓' : ''}</span>
            <span class="disk" aria-hidden="true">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6">
                <rect x="3" y="5" width="18" height="14" rx="1" />
                <circle cx="12" cy="12" r="4" />
                <circle cx="12" cy="12" r="1" />
              </svg>
            </span>
            <span class="row-label">
              {o.label}{#if !o.mounted}<small> · sin montar</small>{/if}
            </span>
            <span class="row-meta">{o.meta}</span>
          </button>
        {/each}
      {/if}
    {:else}
      <div class="state">Este widget no tiene opciones configurables.</div>
    {/if}
  </div>

  <div class="foot">
    <button class="btn ghost" on:click={close}>Cancelar</button>
    <button class="btn primary" on:click={apply}>Aplicar</button>
  </div>
</div>

<style>
  .overlay {
    position: fixed;
    inset: 0;
    z-index: 9400;
    background: rgba(0, 0, 0, 0.5);
    backdrop-filter: blur(2px);
  }
  .modal {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    z-index: 9410;
    width: 340px;
    max-height: 78vh;
    display: flex;
    flex-direction: column;
    background: var(--window-bg);
    border: 1px solid var(--window-border);
    border-radius: var(--bev-lg);
    box-shadow: 0 24px 60px rgba(0, 0, 0, 0.55);
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 14px 16px;
    border-bottom: 1px solid var(--line);
  }
  .title {
    font-family: var(--font-mono);
    font-size: 11px;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--ink);
  }
  .x {
    border: none; background: transparent; color: var(--ink-faint);
    font-size: 13px; cursor: pointer; padding: 4px 6px; border-radius: var(--radius-sm);
  }
  .x:hover { color: var(--ink); background: var(--side-hover); }

  .body { padding: 12px 14px; overflow-y: auto; }
  .body::-webkit-scrollbar { width: 5px; }
  .body::-webkit-scrollbar-thumb { background: var(--line-bright); border-radius: 3px; }

  .hint {
    font-family: var(--font-sans);
    font-size: 11px;
    color: var(--ink-mute);
    margin-bottom: 10px;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 9px;
    width: 100%;
    padding: 9px 10px;
    border: 1px solid var(--line);
    border-radius: var(--radius-md);
    background: var(--panel);
    color: var(--ink-dim);
    cursor: pointer;
    margin-bottom: 6px;
    text-align: left;
    transition: border-color 0.12s ease, background 0.12s ease;
  }
  .row:hover { border-color: var(--line-bright); }
  .row.on {
    border-color: var(--signal);
    background: var(--signal-soft);
    color: var(--ink);
  }
  .chk {
    width: 14px;
    color: var(--signal);
    font-size: 11px;
    flex-shrink: 0;
  }
  .disk { color: var(--side-active-fg); display: flex; flex-shrink: 0; }
  .row.on .disk { color: var(--signal); }
  .row-label {
    flex: 1;
    font-family: var(--font-sans);
    font-size: 12.5px;
  }
  .row-label small { color: var(--warn); font-size: 10px; }
  .row-meta {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--ink-faint);
  }
  .all .row-meta { color: var(--ink-faint); }

  .sep { height: 1px; background: var(--line); margin: 4px 2px 10px; }
  .state {
    padding: 24px 8px;
    text-align: center;
    font-family: var(--font-sans);
    font-size: 12px;
    color: var(--ink-faint);
  }

  .foot {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    padding: 12px 14px;
    border-top: 1px solid var(--line);
  }
  .btn {
    font-family: var(--font-sans);
    font-size: 12px;
    padding: 8px 16px;
    border-radius: var(--radius-md);
    cursor: pointer;
    border: 1px solid transparent;
  }
  .btn.ghost {
    background: transparent;
    border-color: var(--line-bright);
    color: var(--ink-dim);
  }
  .btn.ghost:hover { color: var(--ink); border-color: var(--line-strong); }
  .btn.primary {
    background: var(--signal);
    color: #06120d;
    font-weight: 600;
  }
  .btn.primary:hover { background: var(--signal-hover); }
</style>
