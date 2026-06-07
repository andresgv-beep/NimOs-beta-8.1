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

import Clock from './Clock.svelte';
import SysMon from './SysMon.svelte';
import Storage from './Storage.svelte';
import Network from './Network.svelte';
import RingSolo from './RingSolo.svelte';
import Services from './Services.svelte';
import Torrent from './Torrent.svelte';

export const WIDGET_CATALOG = [
  {
    id: 'clock',
    name: 'Reloj',
    w: 1,
    h: 1,
    topic: null,          // no necesita datos del backend
    component: Clock,
    defaultOn: true,
    sizes: [[1, 1], [2, 1]],
  },
  {
    id: 'sysmon',
    name: 'Sistema',
    w: 2,
    h: 1,
    topic: 'system',      // /api/hardware/stats · CPU + RAM rings
    component: SysMon,
    defaultOn: true,
    sizes: [[2, 1]],
  },
  {
    id: 'cpu',
    name: 'CPU',
    w: 1,
    h: 1,
    topic: 'system',      // mismo topic que sysmon · un solo polling compartido
    component: RingSolo,
    props: { metric: 'cpu' },
    defaultOn: false,
    sizes: [[1, 1]],
  },
  {
    id: 'ram',
    name: 'RAM',
    w: 1,
    h: 1,
    topic: 'system',      // mismo topic que sysmon
    component: RingSolo,
    props: { metric: 'ram' },
    defaultOn: false,
    sizes: [[1, 1]],
  },
  {
    id: 'storage',
    name: 'Storage',
    w: 2,
    h: 1,
    topic: 'storage',     // /api/storage/v2/pools (+smart en el widget)
    component: Storage,
    defaultOn: true,
    sizes: [[2, 1], [2, 2]],
  },
  {
    id: 'network',
    name: 'Red',
    w: 2,
    h: 1,
    topic: 'network',     // /api/network · sparklines DL/UL
    component: Network,
    defaultOn: true,
    sizes: [[2, 1], [2, 2]],
  },
  {
    id: 'services',
    name: 'Servicios',
    w: 2,
    h: 1,
    topic: 'services',    // /api/services · NimHealth
    component: Services,
    defaultOn: true,
    sizes: [[1, 1], [2, 1], [2, 2]],
    // Orden en el widget: failed/error primero, luego degraded/stopped,
    // running al final. Lo que falla sube arriba solo.
  },
  {
    id: 'nimtorrent',
    name: 'NimTorrent',
    w: 2,
    h: 1,
    topic: 'torrent',     // /api/torrent/torrents · proxy Go → torrentd
    component: Torrent,
    defaultOn: false,     // existe en catálogo, apagado por defecto
    sizes: [[2, 1], [2, 2]],
  },
];

/** Lookup rápido por id. */
export const WIDGET_BY_ID = Object.fromEntries(
  WIDGET_CATALOG.map(w => [w.id, w])
);

/**
 * Talla efectiva de una instancia de widget.
 * ──────────────────────────────────────────
 * `sizes` en el catálogo = tallas soportadas [[w,h],...]; w/h del
 * catálogo = talla de serie. El layout puede llevar `size: [w,h]`
 * por instancia (elegida en el menú contextual). Una talla guardada
 * que el catálogo ya no soporte cae a la de serie — nunca rompe.
 */
export function widgetSize(item, def) {
  if (Array.isArray(item?.size) && item.size.length === 2) {
    const [w, h] = item.size;
    if ((def.sizes || []).some(([sw, sh]) => sw === w && sh === h)) {
      return { w, h };
    }
  }
  return { w: def.w, h: def.h };
}

/**
 * Layout por defecto · columna anclada al borde derecho.
 * col/row negativos = intención "desde el borde derecho/inferior":
 *   col -1 → última columna · col -2 → penúltima (origen de un 2×1)
 * La resolución a celdas absolutas y el clamping ocurren SOLO en
 * render (WidgetLayer), nunca aquí ni al guardar.
 */
export const DEFAULT_LAYOUT = [
  { id: 'clock',    col: -1, row: 0 },
  { id: 'sysmon',   col: -2, row: 1 },
  { id: 'storage',  col: -2, row: 2 },
  { id: 'network',  col: -2, row: 3 },
  { id: 'services', col: -2, row: 4 },
];
