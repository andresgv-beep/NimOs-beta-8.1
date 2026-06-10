<script>
  /**
   * WidgetLayer · Capa de widgets de escritorio · NimOS Beta 8.1
   * ═════════════════════════════════════════════════════════════
   * Contenedor estructural. SOLO sabe de:
   *   - Grid de celdas fijas (CELL × CELL, gap GAP)
   *   - Colocar cajas según el catálogo (tamaños fijos w×h)
   *   - Drag con snap a celda y detección de colisión
   *   - Menú contextual: activar/desactivar widgets, restablecer
   *   - Persistencia en prefs.widgetLayout
   *
   * NO sabe qué pinta cada widget. El contenido viene del catálogo
   * (src/lib/widgets/index.js): si `component` es null se renderiza
   * un placeholder; cuando el widget exista, se registra allí y esta
   * capa no cambia.
   *
   * Persistencia con INTENCIÓN, sin clampar:
   *   col/row negativos = anclado al borde derecho/inferior
   *   (col -1 = última columna). La resolución a celdas absolutas y
   *   el clamping ocurren SOLO en render. Así el layout sobrevive a
   *   cambios de resolución sin corromper lo guardado.
   *
   * Capas: z-index 2 → sobre wallpaper/logo (z1), bajo ventanas
   * (z≥100) y taskbar (z9000). pointer-events: none en la capa,
   * auto solo en widgets y menú → el escritorio vacío sigue siendo
   * del escritorio.
   */
  import { onMount } from 'svelte';
  import { prefs, setPrefImmediate } from '$lib/stores/theme.js';
  import { WIDGET_CATALOG, WIDGET_BY_ID, DEFAULT_LAYOUT, widgetSize } from '$lib/widgets/index.js';

  // ─── Geometría del grid ───
  // 1×1 = 144×144 · 2×1 = 302×144 (px CSS, pre-uiScale).
  // Subido de 116/12 (jun 2026): en monitores 2560+ quedaba pequeño.
  const CELL = 144;   // lado de celda en px (pre-zoom)
  const GAP  = 14;    // separación entre celdas
  const PAD  = 20;    // margen interior de la capa

  let layerEl;
  let gridCols = 0;
  let gridRows = 0;

  // ─── Layout (intención) desde prefs · Desktop monta post-loadPrefs ───
  // SANEADO SIEMPRE: entradas con col/row ausentes, null o NaN
  // (datos envenenados por bugs históricos o a saber qué) se reparan
  // con el preset del DEFAULT_LAYOUT o un fallback seguro. Sin esto,
  // una coordenada undefined produce left:NaNpx → widget clavado a
  // la izquierda y drag horizontal muerto (cazado con datos reales
  // de localStorage, jun 2026).
  $: layout = sanitizeLayout($prefs.widgetLayout);

  function sanitizeLayout(raw) {
    const src = Array.isArray(raw) ? raw : DEFAULT_LAYOUT;
    return src.map((it, i) => {
      const def = WIDGET_BY_ID[it.id];
      if (!def) return { ...it };
      const preset = DEFAULT_LAYOUT.find(d => d.id === it.id);
      const col = Number.isFinite(it.col) ? it.col : (preset ? preset.col : -def.w);
      const row = Number.isFinite(it.row) ? it.row : (preset ? preset.row : i);
      return { ...it, col, row };
    });
  }

  // ─── Resolución intención → celdas absolutas (clamp + colisiones) ───
  $: placed = resolvePlacements(layout, gridCols, gridRows);

  function resolvePlacements(items, cols, rows) {
    if (!cols || !rows) return [];
    const occupied = new Set();
    const out = [];

    const isFree = (c, r, w, h) => {
      if (c < 0 || r < 0 || c + w > cols || r + h > rows) return false;
      for (let i = c; i < c + w; i++)
        for (let j = r; j < r + h; j++)
          if (occupied.has(`${i},${j}`)) return false;
      return true;
    };
    const mark = (c, r, w, h) => {
      for (let i = c; i < c + w; i++)
        for (let j = r; j < r + h; j++)
          occupied.add(`${i},${j}`);
    };

    for (const item of items) {
      const def = WIDGET_BY_ID[item.id];
      if (!def) continue; // id desconocido en prefs viejas → ignorar
      const sz = widgetSize(item, def); // talla efectiva (por instancia)

      // Intención → absoluto
      let c = item.col >= 0 ? item.col : cols + item.col;
      let r = item.row >= 0 ? item.row : rows + item.row;
      // Clamp solo en render
      c = Math.max(0, Math.min(c, cols - sz.w));
      r = Math.max(0, Math.min(r, rows - sz.h));

      // Colisión → primero bajar filas, luego primer hueco libre
      if (!isFree(c, r, sz.w, sz.h)) {
        let found = false;
        for (let rr = r + 1; rr <= rows - sz.h && !found; rr++) {
          if (isFree(c, rr, sz.w, sz.h)) { r = rr; found = true; }
        }
        for (let rr = 0; rr <= rows - sz.h && !found; rr++) {
          for (let cc = cols - sz.w; cc >= 0 && !found; cc--) {
            if (isFree(cc, rr, sz.w, sz.h)) { c = cc; r = rr; found = true; }
          }
        }
        // sin hueco: se queda clampado (solapa antes que perderse)
      }
      mark(c, r, sz.w, sz.h);

      out.push({
        id: item.id, def, col: c, row: r,
        cw: sz.w, ch: sz.h, // talla efectiva en celdas
        x: PAD + c * (CELL + GAP),
        y: PAD + r * (CELL + GAP),
        w: sz.w * CELL + (sz.w - 1) * GAP,
        h: sz.h * CELL + (sz.h - 1) * GAP,
      });
    }
    return out;
  }

  // Codifica intención al guardar: mitad derecha/inferior → negativo
  function encodeIntent(col, row, w, h) {
    const centerC = col + w / 2;
    const centerR = row + h / 2;
    return {
      col: centerC > gridCols / 2 ? col - gridCols : col,
      row: centerR > gridRows / 2 ? row - gridRows : row,
    };
  }

  function saveLayout(next) {
    // INMEDIATO a localStorage + servidor, sin debounce: un drop o
    // cambio de talla es una acción discreta y debe sobrevivir a un
    // logout/reinicio en el segundo siguiente.
    setPrefImmediate('widgetLayout', next);
  }

  // ─── Medición del grid ───
  function measure() {
    if (!layerEl) return;
    const w = layerEl.offsetWidth;
    const h = layerEl.offsetHeight;
    gridCols = Math.max(1, Math.floor((w - 2 * PAD + GAP) / (CELL + GAP)));
    gridRows = Math.max(1, Math.floor((h - 2 * PAD + GAP) / (CELL + GAP)));
  }

  onMount(() => {
    measure();
    // ResizeObserver sobre la PROPIA capa, no window.resize:
    // en el arranque la primera medición puede pillar el layout a
    // medio asentar (zoom de uiScale, fuentes) y window.resize NO se
    // dispara cuando cambia el tamaño interno del elemento → el grid
    // se quedaba mal medido toda la sesión y los widgets clampaban a
    // la izquierda (bug cazado en arranques reales, jun 2026).
    // El observer re-mide ante cualquier cambio real de tamaño.
    let ro = null;
    if (typeof ResizeObserver !== 'undefined') {
      ro = new ResizeObserver(measure);
      ro.observe(layerEl);
    } else {
      window.addEventListener('resize', measure);
    }
    return () => {
      if (ro) ro.disconnect();
      else window.removeEventListener('resize', measure);
    };
  });

  // ─── Drag con snap a celda ───
  let drag = null; // { id, def, originX, originY, startCX, startCY, zoom, moving, ghostX, ghostY, target }

  // Ratio coordenadas visuales/layout · maneja root.style.zoom (uiScale)
  function zoomRatio() {
    if (!layerEl) return 1;
    const r = layerEl.getBoundingClientRect().width / layerEl.offsetWidth;
    return r || 1;
  }

  function onWidgetPointerDown(e, p) {
    if (e.button !== 0) return;
    // Elementos interactivos del contenido del widget NO inician drag
    if (e.target.closest('button, a, input, select, textarea')) return;
    e.preventDefault();
    e.currentTarget.setPointerCapture(e.pointerId);
    drag = {
      id: p.id, def: p.def,
      cw: p.cw, ch: p.ch, // talla efectiva en celdas
      originX: p.x, originY: p.y,
      startCX: e.clientX, startCY: e.clientY,
      zoom: zoomRatio(),
      moving: false,
      ghostX: p.x, ghostY: p.y,
      target: null,
    };
  }

  function onWidgetPointerMove(e) {
    if (!drag) return;
    const dx = (e.clientX - drag.startCX) / drag.zoom;
    const dy = (e.clientY - drag.startCY) / drag.zoom;
    if (!drag.moving && Math.hypot(dx, dy) < 4) return;
    drag.moving = true;

    const pxW = drag.cw * CELL + (drag.cw - 1) * GAP;
    const pxH = drag.ch * CELL + (drag.ch - 1) * GAP;
    const maxX = layerEl.offsetWidth - PAD - pxW;
    const maxY = layerEl.offsetHeight - PAD - pxH;
    drag.ghostX = Math.max(PAD, Math.min(drag.originX + dx, maxX));
    drag.ghostY = Math.max(PAD, Math.min(drag.originY + dy, maxY));

    // Celda destino más cercana al ghost
    let c = Math.round((drag.ghostX - PAD) / (CELL + GAP));
    let r = Math.round((drag.ghostY - PAD) / (CELL + GAP));
    c = Math.max(0, Math.min(c, gridCols - drag.cw));
    r = Math.max(0, Math.min(r, gridRows - drag.ch));

    drag.target = { col: c, row: r, ok: targetFree(c, r, drag.cw, drag.ch, drag.id) };
    drag = drag; // trigger reactividad
  }

  function targetFree(c, r, w, h, selfId) {
    for (const p of placed) {
      if (p.id === selfId) continue;
      const overlapC = c < p.col + p.cw && c + w > p.col;
      const overlapR = r < p.row + p.ch && r + h > p.row;
      if (overlapC && overlapR) return false;
    }
    return true;
  }

  function onWidgetPointerUp() {
    if (!drag) return;
    if (drag.moving && drag.target?.ok) {
      const intent = encodeIntent(drag.target.col, drag.target.row, drag.cw, drag.ch);
      // Blindaje: una coordenada no finita NUNCA se persiste.
      // Antes que envenenar prefs, se descarta el drop (revert).
      if (Number.isFinite(intent.col) && Number.isFinite(intent.row)) {
        saveLayout(layout.map(it =>
          it.id === drag.id ? { ...it, ...intent } : it
        ));
      }
    }
    // destino inválido o sin movimiento → revert implícito (no se guarda)
    drag = null;
  }

  // ─── Menú contextual ───
  let menu = null; // { x, y, widgetId }

  function openMenu(e, widgetId = null) {
    e.preventDefault();
    const z = zoomRatio();
    menu = { x: e.clientX / z, y: e.clientY / z, widgetId };
  }

  function onLayerContextMenu(e) {
    // Solo si el click es sobre la capa vacía (los widgets abren el suyo)
    if (e.target === layerEl) openMenu(e, null);
  }

  function closeMenu() { menu = null; }

  function isActive(id) {
    return layout.some(it => it.id === id);
  }

  function toggleWidget(id) {
    if (isActive(id)) {
      saveLayout(layout.filter(it => it.id !== id));
    } else {
      // Posición por defecto si existe; si no, primer hueco libre
      // escaneando desde arriba a la derecha
      const def = WIDGET_BY_ID[id];
      const preset = DEFAULT_LAYOUT.find(d => d.id === id);
      let entry = preset ? { ...preset } : null;
      if (!entry) {
        outer:
        for (let r = 0; r <= gridRows - def.h; r++) {
          for (let c = gridCols - def.w; c >= 0; c--) {
            if (targetFree(c, r, def.w, def.h, null)) {
              entry = { id, ...encodeIntent(c, r, def.w, def.h) };
              break outer;
            }
          }
        }
      }
      if (!entry) entry = { id, col: -def.w, row: 0 }; // grid lleno: arriba derecha
      saveLayout([...layout, entry]);
    }
    closeMenu();
  }

  // ─── Talla por instancia ───
  function currentSize(id) {
    const p = placed.find(x => x.id === id);
    return p ? [p.cw, p.ch] : null;
  }

  function setSize(id, w, h) {
    const def = WIDGET_BY_ID[id];
    saveLayout(layout.map(it => {
      if (it.id !== id) return it;
      const next = { ...it };
      if (w === def.w && h === def.h) {
        delete next.size; // talla de serie → no se guarda nada
      } else {
        next.size = [w, h];
      }
      return next;
    }));
    // Si la nueva talla colisiona, resolvePlacements recoloca a los
    // vecinos en el siguiente render (mismo mecanismo que el resize
    // de viewport). El widget redimensionado conserva su intención.
    closeMenu();
  }

  function resetLayout() {
    saveLayout(DEFAULT_LAYOUT.map(x => ({ ...x })));
    closeMenu();
  }

  function onWindowKeydown(e) {
    if (e.key === 'Escape') closeMenu();
  }
</script>

<svelte:window on:keydown={onWindowKeydown} />

<!-- svelte-ignore a11y-no-noninteractive-element-interactions -->
<div
  class="widget-layer"
  bind:this={layerEl}
  on:contextmenu={onLayerContextMenu}
  role="presentation"
>
  <!-- Indicador de celda destino durante drag -->
  {#if drag?.moving && drag.target}
    <div
      class="drop-hint"
      class:invalid={!drag.target.ok}
      style="
        left:{PAD + drag.target.col * (CELL + GAP)}px;
        top:{PAD + drag.target.row * (CELL + GAP)}px;
        width:{drag.def.w * CELL + (drag.def.w - 1) * GAP}px;
        height:{drag.def.h * CELL + (drag.def.h - 1) * GAP}px;
      "
    ></div>
  {/if}

  {#each placed as p (p.id)}
    <!-- svelte-ignore a11y-no-static-element-interactions -->
    <div
      class="widget"
      class:dragging={drag?.id === p.id && drag.moving}
      style="
        left:{drag?.id === p.id && drag.moving ? drag.ghostX : p.x}px;
        top:{drag?.id === p.id && drag.moving ? drag.ghostY : p.y}px;
        width:{p.w}px;
        height:{p.h}px;
      "
      on:pointerdown={(e) => onWidgetPointerDown(e, p)}
      on:pointermove={onWidgetPointerMove}
      on:pointerup={onWidgetPointerUp}
      on:pointercancel={onWidgetPointerUp}
      on:contextmenu|stopPropagation={(e) => openMenu(e, p.id)}
    >
      {#if p.def.component}
        <!-- Contrato mínimo: el widget SOLO conoce su talla en celdas
             (+ props estáticas declaradas en el catálogo, ej. metric).
             Nada de col/row/px — el grid es asunto del contenedor. -->
        <svelte:component this={p.def.component} w={p.cw} h={p.ch} {...(p.def.props || {})} />
      {:else}
        <!-- Placeholder · fase contenedor · se sustituye al registrar
             el componente en el catálogo -->
        <div class="ph">
          <span class="ph-name">{p.def.name}</span>
          <span class="ph-meta">{p.cw}×{p.ch} · pendiente</span>
        </div>
      {/if}
    </div>
  {/each}

  <!-- Menú contextual -->
  {#if menu}
    <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
    <div class="menu-overlay" on:pointerdown={closeMenu} on:contextmenu|preventDefault={closeMenu}></div>
    <div class="ctx-menu" style="left:{menu.x}px; top:{menu.y}px;">
      {#if menu.widgetId}
        {@const mdef = WIDGET_BY_ID[menu.widgetId]}
        {@const msize = currentSize(menu.widgetId)}
        {#if mdef && (mdef.sizes || []).length > 1 && msize}
          <div class="ctx-label">Tamaño</div>
          {#each mdef.sizes as [sw, sh] (sw + 'x' + sh)}
            <button class="ctx-item" on:click={() => setSize(menu.widgetId, sw, sh)}>
              <span class="ctx-check">{msize[0] === sw && msize[1] === sh ? '✓' : ''}</span>
              {sw}×{sh}
            </button>
          {/each}
          <div class="ctx-sep"></div>
        {/if}
        <button class="ctx-item" on:click={() => toggleWidget(menu.widgetId)}>
          Ocultar {WIDGET_BY_ID[menu.widgetId]?.name}
        </button>
        <div class="ctx-sep"></div>
      {/if}
      <div class="ctx-label">Widgets</div>
      {#each WIDGET_CATALOG as w (w.id)}
        <button class="ctx-item" on:click={() => toggleWidget(w.id)}>
          <span class="ctx-check">{isActive(w.id) ? '✓' : ''}</span>
          {w.name}
        </button>
      {/each}
      <div class="ctx-sep"></div>
      <button class="ctx-item" on:click={resetLayout}>
        Restablecer disposición
      </button>
    </div>
  {/if}
</div>

<style>
  /* ═══════════════════════════════════════════════════════════
     CAPA · sobre wallpaper (z1), bajo ventanas (z≥100)
     pointer-events: auto · necesario para el contextmenu de fondo;
     no bloquea nada funcional (ventanas y taskbar están por encima,
     el escritorio vacío no tiene interacciones propias en Beta 8.1)
     ═══════════════════════════════════════════════════════════ */
  .widget-layer {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    bottom: var(--taskbar-height, 52px);
    z-index: 2;
    pointer-events: auto;
    background: transparent;
  }

  /* ═══════════════════════════════════════════════════════════
     WIDGET · frame canónico Beta 8.1
     bg-card + line + radius 12 · alineado con AppShell
     ═══════════════════════════════════════════════════════════ */
  .widget {
    position: absolute;
    /* Gradiente en el mismo tono (más claro arriba) → volumen real */
    background: linear-gradient(155deg, #26262f, #202028 58%, #1c1c23);
    border: 1px solid var(--line);
    border-radius: 12px;
    overflow: hidden;
    cursor: grab;
    user-select: none;
    touch-action: none;
    pointer-events: auto;
    /* Profundidad permanente: despega la tarjeta del fondo + luz interior arriba */
    box-shadow:
      0 10px 30px rgba(0, 0, 0, 0.40),
      0 2px 6px rgba(0, 0, 0, 0.25),
      inset 0 1px 0 rgba(255, 255, 255, 0.05);
    transition: border-color 0.15s ease, box-shadow 0.15s ease, transform 0.15s ease;
  }
  .widget:hover {
    border-color: var(--line-bright);
    box-shadow:
      0 14px 38px rgba(0, 0, 0, 0.48),
      0 3px 8px rgba(0, 0, 0, 0.3),
      inset 0 1px 0 rgba(255, 255, 255, 0.07);
  }
  .widget.dragging {
    cursor: grabbing;
    border-color: var(--line-bright);
    box-shadow:
      0 22px 55px rgba(0, 0, 0, 0.55),
      0 4px 12px rgba(0, 0, 0, 0.35),
      inset 0 1px 0 rgba(255, 255, 255, 0.08);
    transform: scale(1.015);
    transition: none;
    z-index: 3;
  }

  /* ─── Placeholder fase contenedor ─── */
  .ph {
    height: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 6px;
    padding: 12px;
  }
  .ph-name {
    font-family: var(--font-sans);
    font-size: 13px;
    font-weight: 600;
    color: var(--ink-dim);
  }
  .ph-meta {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--ink-faint);
    letter-spacing: 0.04em;
  }

  /* ─── Indicador de celda destino ─── */
  .drop-hint {
    position: absolute;
    border: 1px dashed var(--signal);
    border-radius: 12px;
    background: var(--signal-soft);
    pointer-events: none;
  }
  .drop-hint.invalid {
    border-color: var(--crit);
    background: var(--crit-dim);
  }

  /* ═══════════════════════════════════════════════════════════
     MENÚ CONTEXTUAL
     ═══════════════════════════════════════════════════════════ */
  .menu-overlay {
    position: fixed;
    inset: 0;
    z-index: 9600;
    pointer-events: auto;
  }
  .ctx-menu {
    position: fixed;
    z-index: 9610;
    min-width: 190px;
    padding: 6px;
    background: var(--panel-elev);
    border: 1px solid var(--line-bright);
    border-radius: 10px;
    box-shadow: 0 10px 32px rgba(0, 0, 0, 0.5);
    pointer-events: auto;
  }
  .ctx-label {
    padding: 5px 10px 3px;
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--ink-faint);
  }
  .ctx-item {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    padding: 7px 10px;
    border: none;
    background: transparent;
    border-radius: 6px;
    font-family: var(--font-sans);
    font-size: 12.5px;
    color: var(--ink);
    text-align: left;
    cursor: pointer;
  }
  .ctx-item:hover {
    background: var(--signal-soft);
    color: var(--signal);
  }
  .ctx-check {
    width: 14px;
    color: var(--signal);
    font-size: 11px;
  }
  .ctx-sep {
    height: 1px;
    margin: 5px 4px;
    background: var(--line);
  }
</style>
