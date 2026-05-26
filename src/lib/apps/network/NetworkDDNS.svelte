<script>
  /**
   * NetworkDDNS · Sub-componente del tab DDNS de NetworkApp
   * ─────────────────────────────────────────────────────────
   * Consume exclusivamente Network v4:
   *   GET    /api/v4/network/ddns           — listar
   *   POST   /api/v4/network/ddns           — crear
   *   PUT    /api/v4/network/ddns/:id       — modificar enabled/auto_update/update_interval
   *   DELETE /api/v4/network/ddns/:id       — borrar (con ?delete_secret=true)
   *   POST   /api/v4/network/ddns/:id/token — rotar token
   *
   * Solo DuckDNS por ahora. Cuando el backend implemente más providers
   * (noip, dynu, freedns, cloudflare), se añaden al PROVIDERS array.
   *
   * Fases visuales:
   *   - active        : ya hay un DDNS configurado, mostrar estado
   *   - empty         : no hay DDNS, mostrar empty state con CTA
   *   - select        : selector de proveedor (paso 1)
   *   - form          : formulario del provider seleccionado (paso 2)
   *
   * El backend NO devuelve el token (HasToken bool) — solo se envía al
   * crear o rotar. La UI nunca lo muestra ni lo solicita en GETs.
   */

  import { onMount, onDestroy } from 'svelte';
  import { token } from '$lib/stores/auth.js';
  import {
    SectionHead, BevelButton, IconButton, TextInput,
    Badge, LED, Spinner
  } from '$lib/ui';

  // ─── Props ───
  // (ninguna por ahora — el componente es autónomo y consume su propia API)

  // ─── State ───
  let loading = true;
  let entry = null;             // ddnsView del primero/único DDNS, o null si no hay
  let phase = 'empty';          // 'active' | 'empty' | 'select' | 'form'

  // Form state
  let form = { provider: 'duckdns', domain: '', token: '' };
  let tokenVisible = false;
  let saving = false;
  let msg = '';
  let msgError = false;

  // Poll
  let pollTimer = null;
  const POLL_INTERVAL_MS = 10000;  // 10s; el reconciler en backend va a 60s

  // Providers UI — añadir aquí solo cuando exista implementación backend.
  // Cada provider declara qué campos pide en su formulario.
  const PROVIDERS = [
    {
      id: 'duckdns',
      name: 'DuckDNS',
      desc: 'Gratis · token único',
      fields: 'subdominio + token'
    }
    // Cuando backend añada noip/dynu/freedns/cloudflare, descomentar aquí
    // y añadir su rama en el formulario abajo.
  ];

  // ─── Lifecycle ───
  onMount(() => {
    refresh();
    pollTimer = setInterval(refresh, POLL_INTERVAL_MS);
  });

  onDestroy(() => {
    if (pollTimer) clearInterval(pollTimer);
  });

  // ─── API helpers ───
  function hdrs() {
    return $token ? { Authorization: `Bearer ${$token}` } : {};
  }

  function jsonHdrs() {
    return { ...hdrs(), 'Content-Type': 'application/json' };
  }

  async function refresh() {
    try {
      const r = await fetch('/api/v4/network/ddns', { headers: hdrs() });
      if (!r.ok) {
        // 401/503 — no rompemos UI, simplemente quedamos vacíos
        loading = false;
        return;
      }
      const data = await r.json();
      const list = data.ddns || [];
      // NimOS gestiona un solo dominio DDNS principal (visión actual).
      // Si en futuro hay multi-domain, esto se hace lista.
      entry = list.length > 0 ? list[0] : null;
      phase = entry ? 'active' : (phase === 'select' || phase === 'form' ? phase : 'empty');
    } catch (e) {
      // Network error — silencioso, próximo poll lo arregla
    } finally {
      loading = false;
    }
  }

  // ─── Actions ───

  function startAdd() {
    msg = '';
    msgError = false;
    form = { provider: 'duckdns', domain: '', token: '' };
    phase = 'select';
  }

  function selectProvider(id) {
    form.provider = id;
    phase = 'form';
  }

  function cancelForm() {
    msg = '';
    msgError = false;
    phase = entry ? 'active' : 'empty';
  }

  async function saveDdns() {
    msg = '';
    msgError = false;
    saving = true;
    try {
      const body = {
        provider: form.provider,
        domain: form.domain.trim(),
        token: form.token.trim(),
        enabled: true,
        auto_update: true,
        update_interval: 900
      };
      const r = await fetch('/api/v4/network/ddns', {
        method: 'POST',
        headers: jsonHdrs(),
        body: JSON.stringify(body)
      });
      if (r.status === 201) {
        msg = 'Configuración guardada · esperando primera actualización…';
        msgError = false;
        await refresh();
        phase = 'active';
      } else if (r.status === 409) {
        msg = 'Ya existe un DDNS para este dominio.';
        msgError = true;
      } else {
        const err = await r.json().catch(() => ({ error: r.statusText }));
        msg = err.error || `Error ${r.status}`;
        msgError = true;
      }
    } catch (e) {
      msg = 'Error de red: ' + e.message;
      msgError = true;
    } finally {
      saving = false;
    }
  }

  async function toggleAutoUpdate() {
    if (!entry) return;
    const body = {
      enabled: entry.enabled,
      auto_update: !entry.auto_update,
      update_interval: entry.update_interval
    };
    try {
      const r = await fetch(`/api/v4/network/ddns/${entry.id}`, {
        method: 'PUT',
        headers: jsonHdrs(),
        body: JSON.stringify(body)
      });
      if (r.ok) await refresh();
    } catch (e) {
      // poll lo arregla
    }
  }

  async function disableDdns() {
    if (!entry) return;
    if (!confirm('¿Desactivar DDNS? El dominio dejará de actualizarse pero la config se conserva.')) return;
    const body = {
      enabled: false,
      auto_update: entry.auto_update,
      update_interval: entry.update_interval
    };
    try {
      const r = await fetch(`/api/v4/network/ddns/${entry.id}`, {
        method: 'PUT',
        headers: jsonHdrs(),
        body: JSON.stringify(body)
      });
      if (r.ok) await refresh();
    } catch (e) {}
  }

  async function enableDdns() {
    if (!entry) return;
    const body = {
      enabled: true,
      auto_update: entry.auto_update,
      update_interval: entry.update_interval
    };
    try {
      const r = await fetch(`/api/v4/network/ddns/${entry.id}`, {
        method: 'PUT',
        headers: jsonHdrs(),
        body: JSON.stringify(body)
      });
      if (r.ok) await refresh();
    } catch (e) {}
  }

  async function deleteDdns() {
    if (!entry) return;
    if (!confirm('¿Borrar DDNS por completo? También se borrará el token cifrado.')) return;
    try {
      const r = await fetch(`/api/v4/network/ddns/${entry.id}?delete_secret=true`, {
        method: 'DELETE',
        headers: hdrs()
      });
      if (r.status === 204) {
        entry = null;
        phase = 'empty';
      }
    } catch (e) {}
  }

  // ─── Formato ───
  function fmtRelative(iso) {
    if (!iso) return '—';
    try {
      const t = new Date(iso).getTime();
      const diff = Math.floor((Date.now() - t) / 1000);
      if (diff < 60) return 'hace ' + diff + 's';
      if (diff < 3600) return 'hace ' + Math.floor(diff / 60) + ' min';
      if (diff < 86400) return 'hace ' + Math.floor(diff / 3600) + ' h';
      return 'hace ' + Math.floor(diff / 86400) + ' días';
    } catch (e) {
      return iso;
    }
  }

  // statusVariant: traduce status del backend a variant de LED
  function statusVariant(s) {
    if (s === 'converged') return 'ok';
    if (s === 'pending') return 'warn';
    return 'warn';
  }

  function statusLabel(e) {
    if (!e) return '—';
    if (!e.enabled) return 'Desactivado';
    if (e.last_run_result === 'success') return 'Activo';
    if (e.last_run_result === 'failed') return 'Error última act.';
    if (e.status === 'pending') return 'Pendiente';
    return 'Esperando primera act.';
  }
</script>

{#if loading}
  <div class="ddns-loading">
    <Spinner label="Cargando DDNS…" />
  </div>
{:else}

  <!-- ═══ Fase: ACTIVE ═══ -->
  {#if phase === 'active' && entry}
    <div class="section">
      <SectionHead count={entry.enabled ? '· activo' : '· desactivado'}>Dynamic DNS</SectionHead>

      <div class="status-bar cols-3">
        <div class="status-cell">
          <div class="sc-label">Proveedor</div>
          <div class="sc-value">{entry.provider}</div>
        </div>
        <div class="status-cell">
          <div class="sc-label">Dominio</div>
          <div class="sc-value mono tc-accent" style="font-size:12px">{entry.domain}</div>
        </div>
        <div class="status-cell">
          <div class="sc-label">Estado</div>
          <div class="sc-value">
            <LED size={7} variant={statusVariant(entry.status)} />
            <span>{statusLabel(entry)}</span>
          </div>
        </div>
      </div>

      <div class="toggle-row">
        <div class="toggle"
             class:on={entry.auto_update}
             on:click={toggleAutoUpdate}
             role="button"
             tabindex="0"
             on:keydown={(e) => e.key === 'Enter' && toggleAutoUpdate()}>
          <div class="toggle-track"><div class="toggle-thumb"></div></div>
        </div>
        <span class="toggle-label">
          Auto-actualización {entry.auto_update ? 'activada' : 'desactivada'} · cada {entry.update_interval}s
        </span>
      </div>

      <div class="detail-rows">
        <div class="detail-row">
          <div class="dr-label">IP detectada</div>
          <div class="dr-value"><code>{entry.last_ip || '—'}</code></div>
        </div>
        <div class="detail-row">
          <div class="dr-label">Última act.</div>
          <div class="dr-value tc-mute">
            {fmtRelative(entry.last_run_at)}
            {#if entry.last_run_result}
              · {entry.last_run_result === 'success' ? '✓ ok' : '✗ error'}
            {/if}
          </div>
        </div>
        <div class="detail-row">
          <div class="dr-label">Token cifrado</div>
          <div class="dr-value">
            {#if entry.has_token}
              <Badge size="sm" variant="accent">presente</Badge>
            {:else}
              <Badge size="sm">no</Badge>
            {/if}
          </div>
        </div>
      </div>

      <div class="actions-row">
        {#if entry.enabled}
          <BevelButton size="sm" variant="danger" onClick={disableDdns}>
            Desactivar
          </BevelButton>
        {:else}
          <BevelButton size="sm" variant="primary" onClick={enableDdns}>
            Activar
          </BevelButton>
        {/if}
        <div style="flex:1"></div>
        <BevelButton size="sm" variant="danger" onClick={deleteDdns}>
          Borrar
        </BevelButton>
      </div>
    </div>
  {/if}

  <!-- ═══ Fase: EMPTY ═══ -->
  {#if phase === 'empty'}
    <div class="empty-box">
      <div class="empty-icon">⇄</div>
      <div class="empty-title">Sin dominios DDNS configurados</div>
      <div class="empty-desc">
        Configura un dominio dinámico para acceder a NimOS desde fuera de tu red local.
      </div>
      <BevelButton variant="primary" size="sm" onClick={startAdd}>
        ▸ Añadir dominio
      </BevelButton>
    </div>
  {/if}

  <!-- ═══ Fase: SELECT PROVIDER ═══ -->
  {#if phase === 'select'}
    <div class="section">
      <SectionHead>Selecciona proveedor DDNS</SectionHead>

      <div class="provider-grid">
        {#each PROVIDERS as prov}
          <button
            class="provider-card"
            class:selected={form.provider === prov.id}
            on:click={() => selectProvider(prov.id)}
          >
            <div class="pc-name">
              <span class="pc-dot"></span>
              {prov.name}
            </div>
            <div class="pc-desc">{prov.desc}</div>
            <div class="pc-fields">campos: {prov.fields}</div>
          </button>
        {/each}
      </div>

      <div class="actions-row" style="margin-top:14px">
        <BevelButton size="sm" onClick={cancelForm}>Cancelar</BevelButton>
      </div>
    </div>
  {/if}

  <!-- ═══ Fase: FORM ═══ -->
  {#if phase === 'form'}
    <div class="section">
      <SectionHead>Configurar {form.provider}</SectionHead>

      {#if form.provider === 'duckdns'}
        <div class="form-group">
          <label class="form-label">Subdominio</label>
          <TextInput bind:value={form.domain} placeholder="midominio.duckdns.org" size="sm" />
          <div class="form-hint">Tu subdominio completo de DuckDNS</div>
        </div>
        <div class="form-group">
          <label class="form-label">Token</label>
          <div class="input-with-eye">
            <TextInput
              bind:value={form.token}
              type={tokenVisible ? 'text' : 'password'}
              placeholder="Token de DuckDNS"
              size="sm"
            />
            <IconButton
              size="sm"
              title={tokenVisible ? 'Ocultar' : 'Mostrar'}
              onClick={() => tokenVisible = !tokenVisible}
            >
              {tokenVisible ? '◉' : '○'}
            </IconButton>
          </div>
          <div class="form-hint">Token de tu cuenta DuckDNS · duckdns.org/domains</div>
        </div>
      {/if}

      <div class="actions-row">
        <BevelButton
          variant="primary"
          size="sm"
          onClick={saveDdns}
          disabled={saving || !form.domain || !form.token}
        >
          {saving ? '▸ Guardando…' : '▸ Guardar'}
        </BevelButton>
        <BevelButton size="sm" onClick={cancelForm}>Cancelar</BevelButton>
      </div>

      {#if msg}
        <div class="msg" class:ok={!msgError} class:error={msgError}>{msg}</div>
      {/if}
    </div>
  {/if}

{/if}

<style>
  /* Estilos heredados del NetworkApp original; consistente con el resto */
  .ddns-loading {
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 60px 0;
  }
  .section {
    margin-bottom: 16px;
  }
  .status-bar {
    display: grid;
    gap: 12px;
    padding: 12px 14px;
    border: 1px solid var(--border, #2a2a2a);
    background: var(--surface-1, #161616);
    border-radius: 2px;
    margin-bottom: 12px;
  }
  .status-bar.cols-3 {
    grid-template-columns: 1fr 1fr 1fr;
  }
  .status-cell {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .sc-label {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-mute, #888);
  }
  .sc-value {
    font-size: 13px;
    color: var(--text, #ddd);
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .sc-value.mono { font-family: monospace; }
  .sc-value.tc-accent { color: var(--accent, #e8a040); }

  .toggle-row {
    display: flex;
    align-items: center;
    gap: 12px;
    margin: 12px 0;
  }
  .toggle {
    cursor: pointer;
    user-select: none;
  }
  .toggle-track {
    width: 36px;
    height: 18px;
    background: var(--surface-2, #222);
    border: 1px solid var(--border, #2a2a2a);
    border-radius: 9px;
    position: relative;
    transition: background 0.15s;
  }
  .toggle-thumb {
    width: 14px;
    height: 14px;
    background: var(--text-mute, #888);
    border-radius: 50%;
    position: absolute;
    top: 1px;
    left: 1px;
    transition: left 0.15s, background 0.15s;
  }
  .toggle.on .toggle-track {
    background: var(--accent-dim, #3a2818);
  }
  .toggle.on .toggle-thumb {
    left: 19px;
    background: var(--accent, #e8a040);
  }
  .toggle-label {
    font-size: 12px;
    color: var(--text-mute, #888);
  }

  .detail-rows {
    border-top: 1px solid var(--border-dim, #1a1a1a);
    margin: 12px 0;
  }
  .detail-row {
    display: flex;
    justify-content: space-between;
    padding: 8px 0;
    font-size: 12px;
    border-bottom: 1px solid var(--border-dim, #1a1a1a);
  }
  .dr-label { color: var(--text-mute, #888); }
  .dr-value code {
    background: var(--surface-2, #222);
    padding: 1px 6px;
    border-radius: 2px;
    font-family: monospace;
  }
  .tc-mute { color: var(--text-mute, #888); }

  .actions-row {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 12px;
  }

  .empty-box {
    text-align: center;
    padding: 40px 20px;
    border: 1px dashed var(--border, #2a2a2a);
    border-radius: 2px;
  }
  .empty-icon {
    font-size: 32px;
    color: var(--text-mute, #888);
    margin-bottom: 12px;
  }
  .empty-title {
    font-size: 14px;
    color: var(--text, #ddd);
    margin-bottom: 8px;
  }
  .empty-desc {
    font-size: 12px;
    color: var(--text-mute, #888);
    margin-bottom: 16px;
    max-width: 400px;
    margin-left: auto;
    margin-right: auto;
  }

  .provider-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 8px;
  }
  .provider-card {
    background: var(--surface-1, #161616);
    border: 1px solid var(--border, #2a2a2a);
    padding: 12px;
    text-align: left;
    cursor: pointer;
    color: var(--text, #ddd);
    font: inherit;
    transition: border-color 0.1s;
  }
  .provider-card:hover {
    border-color: var(--accent, #e8a040);
  }
  .provider-card.selected {
    border-color: var(--accent, #e8a040);
    background: var(--accent-dim, #1f1810);
  }
  .pc-name {
    font-size: 13px;
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 6px;
  }
  .pc-dot {
    width: 6px;
    height: 6px;
    background: var(--accent, #e8a040);
    border-radius: 50%;
  }
  .pc-desc {
    font-size: 11px;
    color: var(--text-mute, #888);
    margin-bottom: 4px;
  }
  .pc-fields {
    font-size: 10px;
    color: var(--text-mute, #666);
    font-family: monospace;
  }

  .form-group {
    margin-bottom: 12px;
  }
  .form-label {
    display: block;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-mute, #888);
    margin-bottom: 4px;
  }
  .form-hint {
    font-size: 11px;
    color: var(--text-mute, #666);
    margin-top: 4px;
  }
  .input-with-eye {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .input-with-eye :global(.text-input) {
    flex: 1;
  }

  .msg {
    margin-top: 10px;
    padding: 8px 12px;
    font-size: 12px;
    border-radius: 2px;
    border: 1px solid var(--border, #2a2a2a);
  }
  .msg.ok {
    border-color: var(--ok, #4a9a4a);
    background: var(--ok-dim, #0a1a0a);
    color: var(--ok-text, #8ac88a);
  }
  .msg.error {
    border-color: var(--danger, #c04444);
    background: var(--danger-dim, #1a0a0a);
    color: var(--danger-text, #ff8888);
  }
</style>
