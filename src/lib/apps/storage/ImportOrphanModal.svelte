<script>
  /**
   * ImportOrphanModal · Adopta un filesystem BTRFS huérfano como pool managed.
   * ─────────────────────────────────────────────────────────────────────────
   * El filesystem ya existe en disco. Solo se registra en SQLite y se le
   * asigna un nombre managed. Los datos se preservan completamente.
   *
   * Props:
   *   · fs            — ObservedBtrfs (uuid, label, profile, devices_online, ...)
   *   · suggestedName — nombre inicial sugerido para el input (opcional)
   *
   * Eventos:
   *   · done   — import exitoso, el padre refresca estado
   *   · cancel — usuario cierra sin importar
   *
   * Uso:
   *   <ImportOrphanModal {fs} {suggestedName} on:done on:cancel />
   */
  import { createEventDispatcher } from 'svelte';
  import { BevelButton } from '$lib/ui';
  import * as api from './api.js';
  import './modal-styles.css';

  export let fs;
  export let suggestedName = '';

  const dispatch = createEventDispatcher();

  let name = suggestedName;
  let processing = false;
  let error = '';

  function close() {
    if (processing) return;
    dispatch('cancel');
  }

  async function submit() {
    if (!fs || !name || processing) return;
    processing = true;
    error = '';
    try {
      await api.importPool({ uuid: fs.uuid, name });
      dispatch('done');
    } catch (e) {
      error = e.message || 'Error desconocido al importar';
      processing = false;
    }
  }
</script>

<div class="modal-backdrop" on:click={close} on:keydown={(e) => e.key === 'Escape' && close()} role="presentation">
  <div class="modal-card" on:click|stopPropagation role="dialog" aria-modal="true">
    <div class="modal-head">
      <h3>Importar pool BTRFS</h3>
      <button class="modal-close" on:click={close} aria-label="Cerrar">×</button>
    </div>

    <div class="modal-body">
      <div class="modal-info">
        <div class="info-row">
          <span class="tc-mute">UUID:</span>
          <span class="mono sm">{fs.uuid}</span>
        </div>
        <div class="info-row">
          <span class="tc-mute">Label original:</span>
          <span class="mono">{fs.label || '(sin label)'}</span>
        </div>
        <div class="info-row">
          <span class="tc-mute">Profile:</span>
          <span class="mono">{fs.profile || 'single'}</span>
        </div>
        <div class="info-row">
          <span class="tc-mute">Discos:</span>
          <span class="mono">{fs.devices_online} dispositivos</span>
        </div>
      </div>

      <p class="modal-text">
        Este filesystem se registrará en NimOS como un pool gestionado.
        Los datos existentes se preservan completamente.
      </p>

      <label class="modal-field">
        <span class="modal-field-label">Nombre del pool en NimOS:</span>
        <input
          type="text"
          bind:value={name}
          placeholder="my-pool"
          maxlength="32"
          disabled={processing}
          on:keydown={(e) => e.key === 'Enter' && submit()}
        />
        <span class="modal-field-hint tc-mute">
          Alfanumérico y guiones, máximo 32 caracteres
        </span>
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
        variant="primary"
        size="sm"
        onClick={submit}
        disabled={processing || !name}
      >
        {processing ? '▸ Importando...' : 'Importar pool'}
      </BevelButton>
    </div>
  </div>
</div>
