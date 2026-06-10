/**
 * filesStore.js · helpers puros de la app Files
 * ─────────────────────────────────────────────────────────────
 * Funciones sin estado reactivo extraídas de FileManager.svelte
 * durante la modularización (paso 1, sin cambios de comportamiento).
 *
 * Aquí va SOLO lógica pura/reutilizable: formato de tamaños y fechas,
 * iconos, detección de tipo. El estado reactivo (currentShare, files,
 * selected, clipboard…) y la orquestación siguen en FileManager.svelte.
 */

// ── SVG de carpetas (local / remoto, grande / pequeño) ──
export const SVG_FOLDER_LOCAL =
  `<svg width="36" height="36" viewBox="0 0 24 24" fill="#f59e0b" stroke="#d97706" stroke-width="0.5" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg>`;
export const SVG_FOLDER_REMOTE =
  `<svg width="36" height="36" viewBox="0 0 24 24" fill="#3b82f6" stroke="#2563eb" stroke-width="0.5" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg>`;
export const SVG_FOLDER_SM_LOCAL =
  `<svg width="15" height="15" viewBox="0 0 24 24" fill="#f59e0b" stroke="#d97706" stroke-width="0.5" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg>`;
export const SVG_FOLDER_SM_REMOTE =
  `<svg width="15" height="15" viewBox="0 0 24 24" fill="#3b82f6" stroke="#2563eb" stroke-width="0.5" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg>`;

// ── Tamaño legible ──
export function fmtSize(b) {
  if (!b) return '—';
  if (b >= 1e9) return (b / 1e9).toFixed(2) + ' GB';
  if (b >= 1e6) return (b / 1e6).toFixed(2) + ' MB';
  if (b >= 1e3) return (b / 1e3).toFixed(0) + ' KB';
  return b + ' B';
}

// ── Fecha legible dd/mm/aaaa hh:mm ──
export function fDate(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  return `${String(d.getDate()).padStart(2, '0')}/${String(d.getMonth() + 1).padStart(2, '0')}/${d.getFullYear()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
}

// ── Extensión en mayúsculas (para columna Tipo) ──
export function fExt(name) {
  const p = name.lastIndexOf('.');
  return p >= 0 ? name.slice(p + 1).toUpperCase() : '—';
}

// ── ¿Es un .zip? ──
export function isZipFile(file) {
  return !file.isDirectory && file.name.toLowerCase().endsWith('.zip');
}

// ── Icono emoji por extensión ──
export function fIcon(file) {
  if (file.isDirectory) return '📁';
  const e = file.name.split('.').pop().toLowerCase();
  return { mp4: '🎬', mkv: '🎬', avi: '🎬', mov: '🎬', mp3: '🎵', wav: '🎵', flac: '🎵', jpg: '🖼️', jpeg: '🖼️', png: '🖼️', gif: '🖼️', svg: '🎨', pdf: '📕', doc: '📝', zip: '📦', rar: '📦', js: '💻', py: '💻', go: '💻', txt: '📄', md: '📄', json: '📄', html: '📄', css: '🅰', iso: '💿', sh: '🔧' }[e] || '📄';
}

// ── Icono HTML (SVG carpeta o emoji) ──
export function fIconHtml(file, small = false) {
  if (file.isDirectory) return small ? SVG_FOLDER_SM_LOCAL : SVG_FOLDER_LOCAL;
  return fIcon(file);
}

// ── Tipo legible para la columna "Tipo" (estilo Synology) ──
export function fType(file) {
  if (file.isDirectory) return 'Carpeta';
  const e = file.name.split('.').pop().toLowerCase();
  if (e === file.name.toLowerCase()) return 'Archivo';
  return e.toUpperCase();
}
