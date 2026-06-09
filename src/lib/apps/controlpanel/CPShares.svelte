<script>
  /**
   * CPShares · Panel de Control · sección Compartidas
   * ───────────────────────────────────────────────────
   * Carpetas compartidas: listar y crear. Migrado desde Settings
   * (sección 'shares') al lenguaje visual v3.
   *
   * API:
   *   GET  /api/shares
   *   GET  /api/storage/v2/pools   (para elegir pool destino)
   *   POST /api/shares             { name, pool, smb, nfs, ftp, public }
   */
  import { onMount } from 'svelte';
  import { hdrs } from '$lib/stores/auth.js';
  import { StatCard } from '$lib/ui';

  let shares = [];
  let pools = [];
  let loading = true;

  let creatingShare = false;
  let newShareForm = null;
  let savingShare = false;
  let shareMsg = '';
  let shareMsgError = false;

  async function loadShares() {
    try {
      const [rs, rp] = await Promise.all([
        fetch('/api/shares', { headers: hdrs() }),
        fetch('/api/storage/v2/pools', { headers: hdrs() }),
      ]);
      if (rs.ok) shares = await rs.json();
      if (rp.ok) {
        const pd = await rp.json();
        pools = pd.data || pd.pools || (Array.isArray(pd) ? pd : []);
      }
    } catch {}
    loading = false;
  }

  function startNewShare() {
    if (pools.length === 0) {
      shareMsg = 'Necesitas crear un pool de almacenamiento primero';
      shareMsgError = true;
      return;
    }
    newShareForm = { name: '', pool: pools[0]?.name || '', smb: true, nfs: false, ftp: false, public: false };
    creatingShare = true;
    shareMsg = '';
  }

  function cancelNewShare() {
    creatingShare = false;
    newShareForm = null;
    shareMsg = '';
  }

  async function saveNewShare() {
    if (!newShareForm || !newShareForm.name || savingShare) return;
    savingShare = true;
    shareMsg = '';
    try {
      const r = await fetch('/api/shares', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify(newShareForm),
      });
      if (r.ok) {
        creatingShare = false;
        newShareForm = null;
        await loadShares();
      } else {
        const e = await r.json().catch(() => ({}));
        shareMsg = e.error || 'Error al crear';
        shareMsgError = true;
      }
    } catch {
      shareMsg = 'Error de red';
      shareMsgError = true;
    }
    savingShare = false;
  }

  $: publicCount = shares.filter((s) => s.public).length;

  onMount(loadShares);
</script>

<div class="cp-shares">
  <!-- Resumen -->
  <div class="cps-stats">
    <StatCard label="Carpetas" value={shares.length} variant="ok" tag="compartidas" />
    <StatCard label="Pools" value={pools.length} variant="info" tag="disponibles" tagVariant="info" />
    <StatCard label="Públicas" value={publicCount} variant={publicCount > 0 ? 'warn' : 'default'} tag={publicCount > 0 ? 'sin auth' : 'ninguna'} tagVariant={publicCount > 0 ? 'warn' : 'default'} />
  </div>

  {#if shareMsg}
    <div class="cps-msg" class:error={shareMsgError}>{shareMsg}</div>
  {/if}

  {#if creatingShare && newShareForm}
    <!-- Formulario nueva carpeta -->
    <div class="cps-form">
      <div class="cps-form-title">Nueva carpeta compartida</div>

      <div class="cps-field">
        <label class="cps-label" for="cps-name">Nombre</label>
        <input id="cps-name" type="text" class="cps-input" bind:value={newShareForm.name} placeholder="ej: media" />
      </div>

      <div class="cps-field">
        <label class="cps-label" for="cps-pool">Pool</label>
        <select id="cps-pool" class="cps-input" bind:value={newShareForm.pool}>
          {#each pools as p}
            <option value={p.name}>{p.name}</option>
          {/each}
        </select>
      </div>

      <div class="cps-field">
        <span class="cps-label">Protocolos</span>
        <div class="cps-protos">
          <button class="cps-proto" class:on={newShareForm.smb} on:click={() => newShareForm.smb = !newShareForm.smb}>SMB</button>
          <button class="cps-proto" class:on={newShareForm.nfs} on:click={() => newShareForm.nfs = !newShareForm.nfs}>NFS</button>
          <button class="cps-proto" class:on={newShareForm.ftp} on:click={() => newShareForm.ftp = !newShareForm.ftp}>FTP</button>
        </div>
      </div>

      <div class="cps-field">
        <button class="cps-public" class:on={newShareForm.public} on:click={() => newShareForm.public = !newShareForm.public}>
          <span class="cps-check" class:on={newShareForm.public}></span>
          Acceso público (sin autenticación)
        </button>
      </div>

      <div class="cps-actions">
        <button class="cps-btn primary" on:click={saveNewShare} disabled={savingShare || !newShareForm.name}>
          {savingShare ? 'Creando…' : 'Crear carpeta'}
        </button>
        <button class="cps-btn" on:click={cancelNewShare}>Cancelar</button>
      </div>
    </div>
  {:else}
    <div class="cps-head">
      <span class="cps-head-lbl">Carpetas · {shares.length}</span>
      <button class="cps-btn primary" on:click={startNewShare}>+ Nueva carpeta</button>
    </div>
  {/if}

  <!-- Lista de carpetas -->
  {#if loading}
    <div class="cps-empty">Cargando carpetas…</div>
  {:else if shares.length === 0 && !creatingShare}
    <div class="cps-empty">No hay carpetas compartidas. Crea la primera con «+ Nueva carpeta».</div>
  {:else if shares.length > 0}
    <div class="cps-list">
      {#each shares as s (s.name)}
        <div class="cps-card">
          <div class="cps-card-icon"></div>
          <div class="cps-card-ident">
            <div class="cps-card-name">{s.displayName || s.name}</div>
            <div class="cps-card-sub">{s.pool || '—'} · {Object.keys(s.permissions || {}).length} usuarios</div>
          </div>
          <div class="cps-card-protos">
            <span class="cps-tag" class:on={s.smb}>SMB</span>
            <span class="cps-tag" class:on={s.nfs}>NFS</span>
            <span class="cps-tag" class:on={s.ftp}>FTP</span>
            {#if s.public}<span class="cps-tag pub on">PÚBLICA</span>{/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .cp-shares { display: flex; flex-direction: column; gap: 16px; max-width: 820px; }

  .cps-stats {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 8px;
  }

  .cps-msg { font-size: 11px; color: var(--fg-3, #9c9ca4); font-family: var(--font-mono); }
  .cps-msg.error { color: var(--st-crit, #ff5a5a); }

  .cps-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .cps-head-lbl {
    font-size: 11px;
    color: var(--fg-4, #7a7a82);
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.6px;
  }

  /* Lista de carpetas */
  .cps-list { display: flex; flex-direction: column; gap: 8px; }
  .cps-card {
    background: var(--bg-card, #15151a);
    border-radius: 8px;
    padding: 14px 16px;
    display: flex;
    align-items: center;
    gap: 14px;
  }
  .cps-card-icon {
    width: 32px;
    height: 32px;
    border-radius: 7px;
    background: rgba(0, 255, 159, 0.08);
    border: 1px solid rgba(0, 255, 159, 0.2);
    flex-shrink: 0;
  }
  .cps-card-ident { flex: 1; min-width: 0; }
  .cps-card-name {
    font-size: 13px;
    color: var(--fg, #f0f0f0);
    font-family: var(--font-mono);
    font-weight: 600;
  }
  .cps-card-sub {
    font-size: 11px;
    color: var(--fg-4, #7a7a82);
    font-family: var(--font-mono);
    margin-top: 2px;
  }
  .cps-card-protos { display: flex; gap: 4px; flex-shrink: 0; }
  .cps-tag {
    font-size: 9px;
    font-family: var(--font-mono);
    letter-spacing: 0.5px;
    padding: 2px 7px;
    border-radius: 3px;
    border: 1px solid var(--bd-2, #20202a);
    color: var(--fg-5, #5a5a62);
  }
  .cps-tag.on {
    color: var(--nim-green, #00ff9f);
    border-color: rgba(0, 255, 159, 0.3);
    background: rgba(0, 255, 159, 0.06);
  }
  .cps-tag.pub.on {
    color: var(--st-warn, #ffc857);
    border-color: rgba(255, 200, 87, 0.3);
    background: rgba(255, 200, 87, 0.06);
  }

  /* Formulario */
  .cps-form {
    background: var(--bg-card, #15151a);
    border-radius: 8px;
    padding: 18px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .cps-form-title {
    font-size: 13px;
    color: var(--fg, #f0f0f0);
    font-family: var(--font-mono);
    font-weight: 600;
  }
  .cps-field { display: flex; flex-direction: column; gap: 6px; }
  .cps-label {
    font-size: 10px;
    color: var(--fg-4, #7a7a82);
    text-transform: uppercase;
    letter-spacing: 0.6px;
    font-family: var(--font-mono);
  }
  .cps-input {
    background: var(--bg-inner, #101015);
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 6px;
    padding: 9px 12px;
    color: var(--fg, #f0f0f0);
    font-size: 13px;
    font-family: var(--font-mono);
    outline: none;
  }
  .cps-input:focus { border-color: rgba(0, 255, 159, 0.35); }

  .cps-protos { display: flex; gap: 6px; }
  .cps-proto {
    padding: 7px 16px;
    background: var(--bg-inner, #101015);
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 6px;
    color: var(--fg-3, #9c9ca4);
    font-size: 11px;
    font-family: var(--font-mono);
    cursor: pointer;
  }
  .cps-proto.on {
    color: var(--nim-green, #00ff9f);
    border-color: rgba(0, 255, 159, 0.35);
    background: rgba(0, 255, 159, 0.06);
  }

  .cps-public {
    display: flex;
    align-items: center;
    gap: 8px;
    background: transparent;
    border: none;
    color: var(--fg-3, #9c9ca4);
    font-size: 12px;
    font-family: var(--font-mono);
    cursor: pointer;
    padding: 0;
  }
  .cps-check {
    width: 16px;
    height: 16px;
    border: 1px solid var(--bd-3, #2a2a32);
    border-radius: 4px;
    background: var(--bg-inner, #101015);
    flex-shrink: 0;
  }
  .cps-check.on {
    background: var(--nim-green, #00ff9f);
    border-color: var(--nim-green, #00ff9f);
  }

  .cps-actions { display: flex; gap: 8px; }
  .cps-btn {
    padding: 9px 16px;
    background: var(--bg-inner, #101015);
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 6px;
    color: var(--fg-3, #9c9ca4);
    font-size: 12px;
    font-family: var(--font-mono);
    cursor: pointer;
    transition: all 0.12s;
  }
  .cps-btn:hover:not(:disabled) { color: var(--fg, #f0f0f0); border-color: var(--bd-3, #2a2a32); }
  .cps-btn.primary {
    background: var(--nim-green, #00ff9f);
    border-color: var(--nim-green, #00ff9f);
    color: var(--bg-window, #16161a);
    font-weight: 600;
  }
  .cps-btn.primary:hover:not(:disabled) { filter: brightness(1.08); }
  .cps-btn:disabled { opacity: 0.5; cursor: not-allowed; }

  .cps-empty {
    padding: 24px;
    text-align: center;
    color: var(--fg-5, #5a5a62);
    font-size: 12px;
    font-family: var(--font-mono);
  }
</style>
