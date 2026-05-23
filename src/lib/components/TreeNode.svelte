<script>
  /**
   * TreeNode · Nodo de árbol de carpetas para Files · v3.1
   * ───────────────────────────────────────────────────────
   * Renderiza recursivamente la jerarquía de directorios de
   * una share. Se monta en el sidebar de FileManager dentro
   * del slot `sidebar-content` del AppShell v3.1.
   *
   * CAMBIOS v3.1:
   *   · Estética alineada al patrón `.sb-item` del AppShell:
   *     padding 7×10, gap 10, font-size 13, tokens compartidos
   *     `--side-hover` / `--side-active-bg` / `--side-active-fg`.
   *   · Folder icon en color neutro (`--ink-mute`), no más
   *     ámbar/azul hardcodeados de Beta 6/7.
   *   · Indicador de origen separado: solo en depth=0 aparece
   *     un dot 8×8 a la izquierda — verde NimOS para local,
   *     púrpura (`--nim-remote`) para remote. Subcarpetas no
   *     lo llevan (contexto heredado del padre).
   *   · Indent uniforme: padding-left = 10 + depth × 14.
   *
   * MECÁNICA (sin cambios):
   *   · Recursión interna con <TreeNode> auto-importado.
   *   · loadChildren() lazy al primer expand.
   *   · shouldBeOpen: auto-expande si activeShare === share
   *     y el path actual es descendiente del nodo.
   *   · Click en chevron alterna expand; click en row navega.
   *
   * API:
   *   share        · nombre de la share raíz
   *   path         · ruta dentro de la share ("/", "/sub", …)
   *   name         · display name del nodo
   *   depth        · nivel (0 = root de la share)
   *   activePath   · path actualmente seleccionado en FileManager
   *   activeShare  · share actualmente seleccionada
   *   onNavigate   · callback (share, path) al hacer click
   *   remote       · true si la share raíz es remota
   */
  import { getToken } from '$lib/stores/auth.js';
  import TreeNode from '$lib/components/TreeNode.svelte';

  export let share;
  export let path;
  export let name;
  export let depth = 0;
  export let activePath;
  export let activeShare;
  export let onNavigate;
  export let remote = false;

  const hdrs = () => ({ 'Authorization': `Bearer ${getToken()}` });

  let expanded = false;
  let children = null;

  $: shouldBeOpen = activeShare === share && isAncestor(path, activePath);
  $: if (shouldBeOpen && !expanded) { expanded = true; if (children === null) loadChildren(); }

  function isAncestor(nodePath, targetPath) {
    if (!targetPath || !nodePath) return false;
    if (nodePath === '/') return targetPath !== '/';
    return targetPath.startsWith(nodePath + '/');
  }

  async function loadChildren() {
    try {
      const r = await fetch('/api/files?share=' + share + '&path=' + encodeURIComponent(path), { headers: hdrs() });
      const d = await r.json();
      children = (d.files || []).filter(f => f.isDirectory);
    } catch { children = []; }
  }

  async function toggle(e) {
    e.stopPropagation();
    expanded = !expanded;
    if (expanded && children === null) await loadChildren();
  }

  function handleClick() { onNavigate(share, path); }

  $: isActive = activeShare === share && activePath === path;
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="tree-item"
  class:active={isActive}
  class:root={depth === 0}
  class:remote
  style="padding-left:{10 + depth * 14}px"
  on:click={handleClick}
  on:keydown
  role="button"
  tabindex="0"
>
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class="tn-chevron"
    class:open={expanded}
    class:invisible={children !== null && children.length === 0}
    on:click={toggle}
  >
    <svg viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
      <polyline points="4.5 2 8.5 6 4.5 10"/>
    </svg>
  </div>

  {#if depth === 0}
    <span class="tn-origin" aria-hidden="true"></span>
  {/if}

  <svg
    class="tn-folder"
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    stroke-width="1.5"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <path d="M2 4.5a1 1 0 0 1 1-1h3.5l1.5 1.5h5a1 1 0 0 1 1 1V12a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1z"/>
  </svg>

  <span class="tn-name">{name}</span>
</div>

{#if expanded && children}
  {#each children as child}
    <TreeNode
      share={share}
      path={path === '/' ? '/' + child.name : path + '/' + child.name}
      name={child.name}
      depth={depth + 1}
      activePath={activePath}
      activeShare={activeShare}
      onNavigate={onNavigate}
      remote={remote}
    />
  {/each}
{/if}

<style>
  /* ─── Tree row · alineado a sb-item del AppShell ─── */
  .tree-item {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 7px 10px;
    margin: 1px 0;
    border-radius: 6px;
    cursor: pointer;
    user-select: none;
    color: var(--ink-dim, #c8c8cf);
    font-family: var(--font-sans);
    font-size: 13px;
    font-weight: 400;
    transition: background 0.12s, color 0.12s;
    /* padding-left se inyecta por style attr según depth */
  }
  .tree-item:hover {
    background: var(--side-hover, rgba(255, 255, 255, 0.04));
    color: var(--ink, #f2f2f5);
  }
  .tree-item.active {
    background: var(--side-active-bg, rgba(122, 158, 177, 0.10));
    color: var(--side-active-fg, #7a9eb1);
  }

  /* ─── Chevron ─── */
  .tn-chevron {
    width: 12px;
    height: 12px;
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--ink-trace, #44444a);
    border-radius: 2px;
    transition: transform 0.15s ease, color 0.12s;
  }
  .tn-chevron:hover {
    color: var(--ink, #f2f2f5);
  }
  .tn-chevron.open {
    transform: rotate(90deg);
  }
  .tn-chevron.invisible {
    visibility: hidden;
    pointer-events: none;
  }
  .tn-chevron svg {
    width: 12px;
    height: 12px;
    pointer-events: none;
  }

  /* ─── Origen (solo en root, depth=0) ───
     Verde nim para local, púrpura nim-remote para remote.
     Reemplaza el sistema de "folder coloreado" de Beta 6/7.
  */
  .tn-origin {
    width: 8px;
    height: 8px;
    flex-shrink: 0;
    border-radius: 2px;
    background: var(--signal, #00ff9f);
    box-shadow: 0 0 4px var(--signal-glow, rgba(0, 255, 159, 0.4));
  }
  .tree-item.remote .tn-origin {
    background: var(--nim-remote, #b48fff);
    box-shadow: 0 0 4px rgba(180, 143, 255, 0.4);
  }

  /* ─── Folder icon · color neutro siempre ─── */
  .tn-folder {
    width: 14px;
    height: 14px;
    flex-shrink: 0;
    color: var(--ink-mute, #9a9aa3);
  }
  .tree-item:hover .tn-folder,
  .tree-item.active .tn-folder {
    color: currentColor;
  }

  /* ─── Nombre ─── */
  .tn-name {
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  /* ─── Estado inactivo de la ventana ───
     Cuando la ventana no tiene foco, atenuar el dot de origen
     para que case con el resto del chrome inactivo (cubo, LEDs).
  */
  :global(.window.inactive) .tn-origin {
    opacity: 0.45;
    box-shadow: 0 0 2px var(--signal-glow, rgba(0, 255, 159, 0.2));
  }
  :global(.window.inactive) .tree-item.remote .tn-origin {
    box-shadow: 0 0 2px rgba(180, 143, 255, 0.2);
  }
</style>
