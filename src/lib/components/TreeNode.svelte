<script>
  import { getToken } from '$lib/stores/auth.js';
  import TreeNode from '$lib/components/TreeNode.svelte';

  export let share;
  export let path;
  export let name;
  export let depth = 0;
  export let activePath;
  export let activeShare;
  export let onNavigate;
  export let remote = false; // root level: is this a remote share?

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
  $: indent = depth * 12;
  $: iconColor = remote ? '#3b82f6' : '#f59e0b';
</script>

<div class="tree-item" class:active={isActive} style="padding-left:{14 + indent}px"
  on:click={handleClick} on:keydown role="button" tabindex="0">
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="tree-chevron" class:open={expanded}
    class:invisible={children !== null && children.length === 0}
    on:click={toggle}>
    <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round">
      <polyline points="9 18 15 12 9 6"/>
    </svg>
  </div>
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke={iconColor} stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" class="tree-folder-ico">
    <path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/>
  </svg>
  <span class="tree-name">{name}</span>
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
  .tree-item { display:flex; align-items:center; gap:5px; padding-right:8px; height:28px; border-radius:6px; cursor:pointer; border:1px solid transparent; transition:all .12s; color:var(--fg-2); font-size:12px; user-select:none; }
  .tree-item:hover { background:rgba(128,128,128,0.08); color:var(--fg); }
  .tree-item.active { background:var(--ui-select-bg); color:var(--fg); border-color:var(--bd-3); }
  .tree-chevron { width:14px; height:14px; flex-shrink:0; display:flex; align-items:center; justify-content:center; color:var(--fg-4); border-radius:3px; transition:transform .15s, color .12s; }
  .tree-chevron:hover { color:var(--fg); }
  .tree-chevron.open { transform:rotate(90deg); }
  .tree-chevron.invisible { visibility:hidden; pointer-events:none; }
  .tree-chevron svg { pointer-events:none; }
  .tree-folder-ico { flex-shrink:0; }
  .tree-name { flex:1; min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
</style>
