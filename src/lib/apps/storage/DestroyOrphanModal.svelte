<script>
  /**
   * DestroyOrphanModal · Destruye un filesystem BTRFS huérfano (irreversible).
   * ──────────────────────────────────────────────────────────────────────────
   * Itera sobre los devices del filesystem y aplica `wipe(force=true)` a cada
   * uno. El force=true es necesario porque el preflight bloquea wipe sobre
   * discos con BTRFS — pero el usuario ha confirmado tipeando "DESTRUIR".
   *
   * NUNCA toca pools managed: el padre solo abre este modal sobre orphans.
   *
   * Props:
   *   · fs — ObservedBtrfs (uuid, label, used_bytes, devices)
   *
   * Eventos:
   *   · done   — destrucción completa, el padre refresca estado
   *   · cancel — usuario cierra sin destruir
   *
   * Uso:
   *   <DestroyOrphanModal {fs} on:done on:cancel />
   */
  import { createEventDispatcher } from 'svelte';
  import { BevelButton } from '$lib/ui';
  import { fmtBytes } from './formatters.js';
  import * as api from './api.js';
  import './modal-styles.css';

  export let fs;

  const dispatch = createEventDispatcher();

  let confirmText = '';
  let processing = false;
  let error = '';

  function close() {
    if (processing) return;
    dispatch('cancel');
  }

  async function submit() {
    if (!fs) return;
    if (confirmText !== 'DESTRUIR') {
      error = 'Escribe "DESTRUIR" para confirmar';
      return;
    }
    processing = true;
    error = '';
    try {
      const paths = (fs.devices || []).map(d => d.path).filter(Boolean);
      for (const path of paths) {
        await api.wipeDisk(path, { force: true });
      }
      dispatch('done');
    } catch (e) {
      error = e.message || 'Error desconocido al destruir';
      processing = false;
    }
  }
</script>

<div class="modal-backdrop" on:click={close} on:keydown={(e) => e.key === 'Escape' && close()} role="presentation">
  <div class="modal-card destructive" on:click|stopPropagation role="dialog" aria-modal="true">
    <div class="modal-head">
      <h3>⚠ Destruir filesystem</h3>
      <button class="modal-close" on:click={close} aria-label="Cerrar">×</button>
    </div>

    <div class="modal-body">
      <p class="modal-text destructive-text">
        Esta acción es <strong>irreversible</strong>. Se borrarán todos los datos
        del filesystem BTRFS y sus discos quedarán vacíos.
      </p>

      <div class="modal-info">
        <div class="info-row">
          <span class="tc-mute">Filesystem:</span>
          <span class="mono">{fs.label || '(sin label)'}</span>
        </div>
        <div class="info-row">
          <span class="tc-mute">UUID:</span>
          <span class="mono sm">{fs.uuid}</span>
        </div>
        {#if fs.used_bytes > 0}
          <div class="info-row">
            <span class="tc-mute">Datos a borrar:</span>
            <span class="mono tc-crit">{fmtBytes(fs.used_bytes)}</span>
          </div>
        {/if}
        <div class="info-row">
          <span class="tc-mute">Discos afectados:</span>
          <span class="mono">
            {(fs.devices || []).map(d => d.path).join(', ')}
          </span>
        </div>
      </div>

      <label class="modal-field">
        <span class="modal-field-label">
          Escribe <strong>DESTRUIR</strong> para confirmar:
        </span>
        <input
          type="text"
          bind:value={confirmText}
          placeholder="DESTRUIR"
          disabled={processing}
        />
      </label>

      {#if error}
        <div class="modal-error">{error}</div>
      {/if}
    </div>

    <div class="modal-actions">
      <BevelButton size="sm" onClick={close} disabled={processing}>
        Cancelar
      </BevelButton>
      <BevelButton
        size="sm"
        onClick={submit}
        disabled={processing || confirmText !== 'DESTRUIR'}
      >
        {processing ? '▸ Destruyendo...' : 'DESTRUIR'}
      </BevelButton>
    </div>
  </div>
</div>
