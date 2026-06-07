/**
 * Catálogo de widgets · NimOS Beta 8.1
 * ─────────────────────────────────────
 * Registro central de los widgets de escritorio. WidgetLayer consume
 * este catálogo para colocar y arrastrar cajas — NO sabe qué pinta
 * cada widget. Cada widget es un componente Svelte autocontenido en
 * src/lib/widgets/ que recibe `widget` como prop y consume datos de
 * widgetData.js.
 *
 * Reglas (decisión de diseño, junio 2026):
 *   - Tamaños FIJOS por widget (w×h en celdas). Sin redimensión.
 *   - Catálogo cerrado: estos 5 widgets existen, el usuario activa
 *     o desactiva los que quiera desde el menú contextual.
 *   - `component: null` → WidgetLayer renderiza placeholder (fase
 *     contenedor). Al implementar cada widget, se importa aquí y
 *     se asigna. WidgetLayer no cambia.
 *
 * topic → clave de polling en widgetData.js que el widget necesita.
 */

export const WIDGET_CATALOG = [
  {
    id: 'clock',
    name: 'Reloj',
    w: 1,
    h: 1,
    topic: null,          // no necesita datos del backend
    component: null,      // → src/lib/widgets/Clock.svelte (pendiente)
    defaultOn: true,
  },
  {
    id: 'sysmon',
    name: 'Sistema',
    w: 2,
    h: 1,
    topic: 'system',      // /api/hardware/stats · CPU + RAM rings
    component: null,      // → src/lib/widgets/SysMon.svelte (pendiente)
    defaultOn: true,
  },
  {
    id: 'storage',
    name: 'Storage',
    w: 2,
    h: 1,
    topic: 'storage',     // /api/storage/v2/pools (+smart en el widget)
    component: null,      // → src/lib/widgets/Storage.svelte (pendiente)
    defaultOn: true,
  },
  {
    id: 'network',
    name: 'Red',
    w: 2,
    h: 1,
    topic: 'network',     // /api/network · sparklines DL/UL
    component: null,      // → src/lib/widgets/Network.svelte (pendiente)
    defaultOn: true,
  },
  {
    id: 'nimtorrent',
    name: 'NimTorrent',
    w: 2,
    h: 1,
    topic: 'torrent',     // definido al implementar el widget
    component: null,      // → src/lib/widgets/Torrent.svelte (pendiente)
    defaultOn: false,     // existe en catálogo, apagado por defecto
  },
];

/** Lookup rápido por id. */
export const WIDGET_BY_ID = Object.fromEntries(
  WIDGET_CATALOG.map(w => [w.id, w])
);

/**
 * Layout por defecto · columna anclada al borde derecho.
 * col/row negativos = intención "desde el borde derecho/inferior":
 *   col -1 → última columna · col -2 → penúltima (origen de un 2×1)
 * La resolución a celdas absolutas y el clamping ocurren SOLO en
 * render (WidgetLayer), nunca aquí ni al guardar.
 */
export const DEFAULT_LAYOUT = [
  { id: 'clock',   col: -1, row: 0 },
  { id: 'sysmon',  col: -2, row: 1 },
  { id: 'storage', col: -2, row: 2 },
  { id: 'network', col: -2, row: 3 },
];
