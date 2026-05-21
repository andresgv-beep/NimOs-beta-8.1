<script>
  /**
   * PortsConfig · Network module v4 · Beta 8.1
   * ────────────────────────────────────────────────────────────
   * Configuración de puertos HTTP/HTTPS del daemon.
   *
   * Endpoints consumidos (módulo v4):
   *   GET  /api/v4/network/ports        — lista los 2 puertos
   *   PUT  /api/v4/network/ports/:id    — actualiza config
   *
   * Diseño:
   *   - 2 cards lado a lado (http y https), max-width 480px cada una.
   *   - Cada card muestra: ID, estado (LED), port + bind + enabled,
   *     y un badge de convergencia (converged/pending/drifted).
   *   - Edición inline: click en el card → modo edición con TextInput.
   *
   * NO toca puertos del kernel: solo persiste config en DB. El
   * reconciler de F-004+ aplica el cambio real (rebind del listener).
   *
   * Convergencia (de NIMOS_DISCIPLINE):
   *   - converged: applied == desired → todo está aplicado.
   *   - pending:   applied < desired  → cambio sin aplicar todavía.
   *   - drifted:   observed != applied → estado real divergente.
   */

  import { onMount } from 'svelte';
  import { jsonHdrs, hdrs } from '$lib/stores/auth.js';
  import {
    KPICard, SectionHead, BevelButton, TextInput, LED, Badge, Spinner
  } from '$lib/ui';

  // Estado.
  let ports = [];
  let loading = true;
  let error = '';

  // Estado de edición — un map id → editingState.
  let editing = {};
  let saving = {};
  let saveError = {};

  // ── Fetch ───────────────────────────────────────────────────

  async function loadPorts() {
    loading = true;
    error = '';
    try {
      const res = await fetch('/api/v4/network/ports', { headers: hdrs() });
      if (!res.ok) {
        error = `Failed to load ports (${res.status})`;
        ports = [];
        return;
      }
      const data = await res.json();
      ports = data.ports || [];
    } catch (e) {
      error = `Network error: ${e.message}`;
      ports = [];
    } finally {
      loading = false;
    }
  }

  async function savePort(id) {
    const draft = editing[id];
    if (!draft) return;

    const portNum = parseInt(draft.port, 10);
    if (isNaN(portNum) || portNum < 1 || portNum > 65535) {
      saveError = { ...saveError, [id]: 'Port must be 1..65535' };
      return;
    }
    if (!draft.bind_address || draft.bind_address.trim() === '') {
      saveError = { ...saveError, [id]: 'Bind address is required' };
      return;
    }

    saving = { ...saving, [id]: true };
    saveError = { ...saveError, [id]: '' };

    try {
      const res = await fetch(`/api/v4/network/ports/${id}`, {
        method: 'PUT',
        headers: jsonHdrs(),
        body: JSON.stringify({
          port: portNum,
          bind_address: draft.bind_address.trim(),
          enabled: draft.enabled,
        }),
      });
      if (!res.ok) {
        const errBody = await res.text();
        saveError = { ...saveError, [id]: errBody || `HTTP ${res.status}` };
        return;
      }
      const updated = await res.json();
      ports = ports.map(p => (p.id === id ? updated : p));
      // Cerrar edición.
      const { [id]: _, ...rest } = editing;
      editing = rest;
    } catch (e) {
      saveError = { ...saveError, [id]: `Network error: ${e.message}` };
    } finally {
      saving = { ...saving, [id]: false };
    }
  }

  function startEditing(p) {
    editing = {
      ...editing,
      [p.id]: {
        port: String(p.port),
        bind_address: p.bind_address,
        enabled: p.enabled,
      },
    };
    saveError = { ...saveError, [p.id]: '' };
  }

  function cancelEditing(id) {
    const { [id]: _, ...rest } = editing;
    editing = rest;
    saveError = { ...saveError, [id]: '' };
  }

  // ── Derivados de status ─────────────────────────────────────

  function statusLabel(s) {
    switch (s) {
      case 'converged': return 'CONVERGED';
      case 'pending':   return 'PENDING';
      case 'drifted':   return 'DRIFTED';
      default:          return s?.toUpperCase() || '—';
    }
  }

  function statusVariant(s) {
    switch (s) {
      case 'converged': return 'ok';      // verde
      case 'pending':   return 'warn';    // amarillo (esperando)
      case 'drifted':   return 'crit';    // rojo (estado real ≠ aplicado)
      default:          return 'off';
    }
  }

  onMount(loadPorts);
</script>

<div class="ports-config">
  <SectionHead count={loading ? '· loading…' : `· ${ports.length} ports`}>
    HTTP/HTTPS Listeners
  </SectionHead>

  {#if loading}
    <div class="loading">
      <Spinner /> <span>Loading ports…</span>
    </div>
  {:else if error}
    <div class="error-banner">{error}</div>
  {:else if ports.length === 0}
    <p class="empty">
      No ports configured. The daemon will create defaults on first boot.
    </p>
  {:else}
    <div class="cards">
      {#each ports as port (port.id)}
        {@const isEditing = editing[port.id] != null}
        {@const isSaving = saving[port.id]}
        {@const err = saveError[port.id]}

        <div class="card" class:editing={isEditing}>
          <header>
            <div class="title-row">
              <LED variant={statusVariant(port.status)} />
              <span class="id">{port.id.toUpperCase()}</span>
            </div>
            <Badge variant={statusVariant(port.status)}>
              {statusLabel(port.status)}
            </Badge>
          </header>

          {#if !isEditing}
            <dl class="fields">
              <div><dt>Port</dt><dd>{port.port}</dd></div>
              <div><dt>Bind</dt><dd>{port.bind_address}</dd></div>
              <div>
                <dt>Enabled</dt>
                <dd>{port.enabled ? 'yes' : 'no'}</dd>
              </div>
              <div>
                <dt>Generations</dt>
                <dd class="gen">
                  d{port.desired_generation}
                  · o{port.observed_generation}
                  · a{port.applied_generation}
                </dd>
              </div>
            </dl>
            <footer>
              <BevelButton onClick={() => startEditing(port)}>
                Edit
              </BevelButton>
            </footer>
          {:else}
            <div class="form">
              <label>
                <span>Port</span>
                <TextInput
                  bind:value={editing[port.id].port}
                  placeholder="1..65535"
                />
              </label>
              <label>
                <span>Bind address</span>
                <TextInput
                  bind:value={editing[port.id].bind_address}
                  placeholder="0.0.0.0"
                />
              </label>
              <label class="checkbox">
                <input
                  type="checkbox"
                  bind:checked={editing[port.id].enabled}
                />
                <span>Enabled</span>
              </label>
              {#if err}
                <div class="form-error">{err}</div>
              {/if}
              <div class="form-actions">
                <BevelButton
                  variant="primary"
                  onClick={() => savePort(port.id)}
                  disabled={isSaving}
                >
                  {isSaving ? 'Saving…' : 'Save'}
                </BevelButton>
                <BevelButton
                  onClick={() => cancelEditing(port.id)}
                  disabled={isSaving}
                >
                  Cancel
                </BevelButton>
              </div>
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .ports-config {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .loading,
  .empty,
  .error-banner {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg-dim);
    padding: 12px;
  }

  .loading {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .error-banner {
    color: var(--crit);
    border-left: 2px solid var(--crit);
    padding-left: 10px;
  }

  .cards {
    display: flex;
    flex-wrap: wrap;
    gap: 14px;
  }

  .card {
    background: var(--panel-elev);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 14px;
    width: 100%;
    max-width: 480px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .card.editing {
    border-color: var(--accent);
  }

  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .title-row {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .id {
    font-family: var(--font-mono);
    font-size: 13px;
    font-weight: 600;
    letter-spacing: 1px;
    color: var(--text-primary);
  }

  dl.fields {
    margin: 0;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px 16px;
  }

  dl.fields > div {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  dl.fields dt {
    font-family: var(--font-mono);
    font-size: 9px;
    letter-spacing: 1.5px;
    text-transform: uppercase;
    color: var(--fg-faint);
  }

  dl.fields dd {
    margin: 0;
    font-family: var(--font-mono);
    font-size: 13px;
    color: var(--text-primary);
  }

  dl.fields dd.gen {
    font-size: 11px;
    color: var(--fg-dim);
  }

  footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }

  .form {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .form label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .form label > span {
    font-family: var(--font-mono);
    font-size: 9px;
    letter-spacing: 1.5px;
    text-transform: uppercase;
    color: var(--fg-faint);
  }

  .form label.checkbox {
    flex-direction: row;
    align-items: center;
    gap: 8px;
  }

  .form label.checkbox > span {
    font-size: 13px;
    color: var(--text-primary);
    text-transform: none;
    letter-spacing: 0;
  }

  .form-error {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--crit);
    padding: 6px 8px;
    background: var(--panel);
    border-left: 2px solid var(--crit);
  }

  .form-actions {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
  }
</style>
