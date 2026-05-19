import { writable, derived } from 'svelte/store';
import { getToken } from './auth.js';

/**
 * NimOS Beta 8.1 theme store
 * ─────────────────────────
 * Soporta multi-tema (oscuro / crema) + accent color customizable.
 *
 * Funcionamiento:
 *   1. El usuario elige tema en Settings → applyToDOM aplica
 *      `data-theme="dark"` o `data-theme="cream"` al <html>
 *   2. Las variables CSS semánticas (--canvas, --panel, --ink...)
 *      cambian automáticamente al cambiar el atributo data-theme
 *   3. El accent color se aplica como HSL (--signal-h/s/l) para
 *      que app.css derive variantes (hover, dim, glow, soft)
 *      automáticamente sin recalcular hex en JS
 *
 * Compatibilidad: los tokens legacy (--accent, --accent-dim, etc.)
 * son aliases en app.css → cualquier componente viejo sigue
 * funcionando sin cambios.
 */

const ACCENT_COLORS = {
  green:    '#00ff9f', // default · verde fósforo NimOS
  amber:    '#ffb800',
  cyan:     '#4db8ff',
  magenta:  '#e873ff',
  orange:   '#ff8c3f',
  red:      '#ff5a5a',
};

const THEMES = ['dark', 'cream'];

const DEFAULTS = {
  // ─── Tema ───
  theme: 'dark', // 'dark' | 'cream'

  // ─── Accent color ───
  accentColor: 'green',
  customAccentColor: '#00ff9f',

  // ─── Taskbar ───
  taskbarSize:     'medium',    // 'small' (44px) | 'medium' (52px) | 'large' (60px)
  autoHideTaskbar: false,

  // ─── Reloj ───
  clock24: true,

  // ─── Escalado UI ───
  textScale: 100,
  uiScale:   'auto', // 'auto' | number (75..150)

  // ─── Wallpaper (path o URL) ───
  wallpaper: '',

  // ─── Scanlines / CRT overlay opcional ───
  crtOverlay: false,

  // ─── Apps ancladas al taskbar ───
  pinnedApps: ['files', 'appstore', 'nimsettings', 'nimhealth'],
};

export const prefs = writable({ ...DEFAULTS });

// Derivados
export const accentColor = derived(prefs, $p =>
  ACCENT_COLORS[$p.accentColor] || $p.customAccentColor || ACCENT_COLORS.green
);
export const pinnedApps = derived(prefs, $p => $p.pinnedApps);
export const currentTheme = derived(prefs, $p => $p.theme || 'dark');

let saveTimeout = null;

/**
 * Calcula el factor de escala automático según resolución/DPR.
 * Mismo algoritmo que Beta 7 — funcionaba bien.
 */
function computeUiScale(setting) {
  if (setting !== 'auto' && typeof setting === 'number') return setting / 100;

  const w = window.innerWidth;
  const dpr = window.devicePixelRatio || 1;
  const physicalWidth = w * dpr;
  const baseline = 1920;

  let scale;
  if (dpr > 1 && physicalWidth > baseline * 1.5) {
    scale = w / baseline;
  } else {
    scale = physicalWidth / baseline;
  }

  return Math.max(0.75, Math.min(1.5, Math.round(scale * 20) / 20));
}

/**
 * Convierte un color hex (#rrggbb) a HSL.
 * Returns: { h: 0-360, s: 0-100, l: 0-100 } o null si inválido.
 */
function hexToHsl(hex) {
  const clean = hex.replace('#', '');
  if (clean.length !== 6) return null;

  const r = parseInt(clean.slice(0, 2), 16) / 255;
  const g = parseInt(clean.slice(2, 4), 16) / 255;
  const b = parseInt(clean.slice(4, 6), 16) / 255;

  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const delta = max - min;
  let h = 0, s = 0;
  const l = (max + min) / 2;

  if (delta !== 0) {
    s = l > 0.5 ? delta / (2 - max - min) : delta / (max + min);
    switch (max) {
      case r: h = ((g - b) / delta + (g < b ? 6 : 0)); break;
      case g: h = ((b - r) / delta + 2); break;
      case b: h = ((r - g) / delta + 4); break;
    }
    h *= 60;
  }

  return {
    h: Math.round(h),
    s: Math.round(s * 100),
    l: Math.round(l * 100),
  };
}

/**
 * Backup: convierte hex a RGB para los aliases legacy --accent-dim/glow.
 */
function hexToRgb(hex) {
  const clean = hex.replace('#', '');
  if (clean.length !== 6) return null;
  return {
    r: parseInt(clean.slice(0, 2), 16),
    g: parseInt(clean.slice(2, 4), 16),
    b: parseInt(clean.slice(4, 6), 16),
  };
}

/**
 * Aplica las prefs al DOM vía variables CSS y atributos.
 */
function applyToDOM(p) {
  const root = document.documentElement;

  // ─── Tema (data-theme attribute) ───
  const theme = THEMES.includes(p.theme) ? p.theme : 'dark';
  root.setAttribute('data-theme', theme);

  // ─── Accent color ───
  const accentHex = ACCENT_COLORS[p.accentColor] || p.customAccentColor || ACCENT_COLORS.green;

  // Modo nuevo · HSL para que app.css derive variantes
  const hsl = hexToHsl(accentHex);
  if (hsl) {
    root.style.setProperty('--signal-h', hsl.h.toString());
    root.style.setProperty('--signal-s', hsl.s + '%');
    root.style.setProperty('--signal-l', hsl.l + '%');
  }

  // Modo legacy · compat con código viejo (cuando no usa HSL)
  root.style.setProperty('--accent', accentHex);
  const rgb = hexToRgb(accentHex);
  if (rgb) {
    root.style.setProperty('--accent-dim', `rgba(${rgb.r}, ${rgb.g}, ${rgb.b}, 0.12)`);
    root.style.setProperty('--accent-glow', `rgba(${rgb.r}, ${rgb.g}, ${rgb.b}, 0.35)`);
  }

  // ─── Taskbar height ───
  const tbH = p.taskbarSize === 'small' ? 44
            : p.taskbarSize === 'large' ? 60
            : 52;
  root.style.setProperty('--taskbar-height', tbH + 'px');

  // ─── Text / UI scale ───
  root.style.setProperty('--text-scale', (p.textScale / 100).toString());
  root.style.setProperty('--glow-intensity', '0.5');

  const scale = computeUiScale(p.uiScale);
  root.style.setProperty('--ui-scale', scale.toString());
  root.style.setProperty('--ui-zoom', scale.toString());
  root.style.zoom = scale;

  // ─── CRT overlay ───
  root.classList.toggle('crt-overlay', !!p.crtOverlay);
}

/**
 * Carga prefs desde servidor con fallback a localStorage y defaults.
 */
export async function loadPrefs() {
  // 1 · Prefs inyectadas server-side (instant)
  if (typeof document !== 'undefined') {
    const el = document.getElementById('__nimos_prefs_v1');
    if (el) {
      try {
        const serverPrefs = JSON.parse(atob(el.getAttribute('content')));
        const p = { ...DEFAULTS, ...serverPrefs };
        prefs.set(p);
        applyToDOM(p);
        localStorage.setItem('nimos-prefs', JSON.stringify(p));
        el.remove();
        return;
      } catch {}
    }
  }

  // 2 · Defaults + localStorage
  applyToDOM({ ...DEFAULTS });
  try {
    const cached = localStorage.getItem('nimos-prefs');
    if (cached) {
      const p = { ...DEFAULTS, ...JSON.parse(cached) };
      prefs.set(p);
      applyToDOM(p);
    }
  } catch {}

  // 3 · Fetch al backend
  const token = getToken();
  if (!token) return;

  try {
    const res = await fetch('/api/user/preferences', {
      headers: { 'Authorization': `Bearer ${token}` },
    });
    const data = await res.json();
    if (data.preferences) {
      const p = { ...DEFAULTS, ...data.preferences };
      prefs.set(p);
      applyToDOM(p);
      localStorage.setItem('nimos-prefs', JSON.stringify(p));
    }
  } catch (err) {
    console.error('[Prefs] Load failed:', err.message);
  }
}

export function setPref(key, value) {
  prefs.update(p => {
    const updated = { ...p, [key]: value };
    applyToDOM(updated);
    localStorage.setItem('nimos-prefs', JSON.stringify(updated));
    if (saveTimeout) clearTimeout(saveTimeout);
    saveTimeout = setTimeout(() => saveToServer(key, value), 1500);
    return updated;
  });
}

export function setPrefs(updates) {
  prefs.update(p => {
    const updated = { ...p, ...updates };
    applyToDOM(updated);
    localStorage.setItem('nimos-prefs', JSON.stringify(updated));
    if (saveTimeout) clearTimeout(saveTimeout);
    saveTimeout = setTimeout(() => saveToServer(null, null, updates), 1500);
    return updated;
  });
}

/**
 * Helper: cambiar el tema. Acepta 'dark' | 'cream'.
 */
export function setTheme(theme) {
  if (THEMES.includes(theme)) {
    setPref('theme', theme);
  }
}

/**
 * Helper: toggle entre dark y cream.
 */
export function toggleTheme() {
  prefs.update(p => {
    const next = p.theme === 'dark' ? 'cream' : 'dark';
    const updated = { ...p, theme: next };
    applyToDOM(updated);
    localStorage.setItem('nimos-prefs', JSON.stringify(updated));
    if (saveTimeout) clearTimeout(saveTimeout);
    saveTimeout = setTimeout(() => saveToServer('theme', next), 1500);
    return updated;
  });
}

async function saveToServer(key, value, bulk = null) {
  const token = getToken();
  if (!token) return;
  try {
    const body = bulk || { [key]: value };
    await fetch('/api/user/preferences', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
      body: JSON.stringify(body),
    });
  } catch {}
}

export { ACCENT_COLORS, DEFAULTS, THEMES };
