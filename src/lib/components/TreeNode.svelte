<script>
  /**
   * TreeNode · Nodo de árbol de carpetas para Files · v3.2
   * ───────────────────────────────────────────────────────
   * Renderiza recursivamente la jerarquía de directorios de
   * una share. Se monta en el sidebar de FileManager dentro
   * del slot `sidebar-content` del AppShell v3.1.
   *
   * CAMBIOS v3.2:
   *   · Eliminado el SVG folder genérico (Beta 6/7).
   *   · Sustituido por un cubo 10×10 — firma NimOS · pareja
   *     micro del `.ink-cube` blanco del titlebar.
   *   · Color del cubo: naranja `--nim-folder` para shares
   *     locales, azul `--nim-remote` para remotas. El cubo
   *     marca origen por sí mismo en cualquier depth.
   *   · Microinteracción: cuando el activeShare/activePath
   *     cae dentro de este subárbol (`isActive || shouldBeOpen`),
   *     el cubo rota 45° con glow del color de origen.
   *     Cuadrado quieto = no estás aquí. Rombo = estás dentro.
   *
   * CAMBIOS v3.1 (preservados):
   *   · Estética alineada al patrón `.sb-item` del AppShell.
   *   · Indicador de origen separado (dot 8×8 en depth=0).
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
  /* v3.2: cubo rota cuando este nodo está siendo navegado
     (es el activo o uno ancestro del activo). */
  $: inTrail = isActive || shouldBeOpen;
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="tree-item"
  class:active={isActive}
  class:in-trail={inTrail}
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

  <!-- Cubo · firma NimOS · rota a 45° cuando inTrail -->
  <span class="tn-cube" aria-hidden="true"></span>

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
     Verde nim para local, azul nim-remote para remote.
     Reemplaza el sistema de "folder coloreado" de Beta 6/7.
     v3.2: el cubo .tn-cube también refleja origen, así que
     el dot es indicador adicional de "esto es root" (depth=0).
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
    background: var(--nim-remote, #4db8ff);
    box-shadow: 0 0 4px rgba(77, 184, 255, 0.4);
  }

  /* ─── Cubo · firma NimOS para "carpeta navegable" ───
     Cuadrado 10×10 quieto = carpeta cerrada / no estás aquí.
     Rotado 45° con glow = estás navegando este subárbol.
     Color del cubo refleja origen:
       · Local  → --nim-folder (naranja)
       · Remote → --nim-remote (azul)
  */
  .tn-cube {
    width: 10px;
    height: 10px;
    flex-shrink: 0;
    background: var(--nim-folder, #ff9c5a);
    transition:
      transform 0.25s cubic-bezier(0.4, 0, 0.2, 1),
      box-shadow 0.2s ease,
      opacity 0.15s;
  }
  .tree-item.remote .tn-cube {
    background: var(--nim-remote, #4db8ff);
  }
  .tree-item.in-trail .tn-cube {
    transform: rotate(45deg);
    box-shadow: 0 0 5px rgba(255, 156, 90, 0.45);
  }
  .tree-item.remote.in-trail .tn-cube {
    box-shadow: 0 0 5px rgba(77, 184, 255, 0.45);
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
     y el cubo para que casen con el resto del chrome inactivo
     (ink-cube, LEDs).
  */
  :global(.window.inactive) .tn-origin {
    opacity: 0.45;
    box-shadow: 0 0 2px var(--signal-glow, rgba(0, 255, 159, 0.2));
  }
  :global(.window.inactive) .tree-item.remote .tn-origin {
    box-shadow: 0 0 2px rgba(77, 184, 255, 0.2);
  }
  :global(.window.inactive) .tn-cube {
    opacity: 0.55;
  }
  :global(.window.inactive) .tree-item.in-trail .tn-cube {
    box-shadow: 0 0 2px rgba(255, 156, 90, 0.2);
  }
  :global(.window.inactive) .tree-item.remote.in-trail .tn-cube {
    box-shadow: 0 0 2px rgba(77, 184, 255, 0.2);
  }
</style>
