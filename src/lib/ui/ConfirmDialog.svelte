<script>
  /**
   * ConfirmDialog · Modal de confirmación
   * ───────────────────────────────────────
   * Modal genérico para confirmar acciones. Soporta:
   *   - Modo simple (sí/no)
   *   - Modo "escribe palabra para confirmar" (inputConfirm)
   *   - Variantes visuales: default / warn / danger
   *
   * Uso simple:
   *   <ConfirmDialog
   *     open={true}
   *     title="¿Continuar?"
   *     message="Esta acción borrará el snapshot."
   *     variant="danger"
   *     on:confirm={handleConfirm}
   *     on:cancel={() => open = false}
   *   />
   *
   * Uso con input-to-confirm:
   *   <ConfirmDialog
   *     open={true}
   *     title="Destruir pool"
   *     message="Esta acción es irreversible."
   *     inputConfirm="data3"
   *     variant="danger"
   *     confirmLabel="Destruir pool"
   *     on:confirm={handleDestroy}
   *     on:cancel={() => open = false}
   *   />
   */
  import { createEventDispatcher, onMount, onDestroy } from 'svelte';

  export let open = false;
  export let title = 'Confirmar';
  export let message = '';
  export let confirmLabel = 'Confirmar';
  export let cancelLabel = 'Cancelar';
  /** 'default' | 'warn' | 'danger' */
  export let variant = 'default';
  /** Si se pasa, el usuario debe escribir exactamente esta cadena para habilitar el botón confirmar. */
  export let inputConfirm = null;
  /** Deshabilita confirmación mientras procesa (ej. llamada async) */
  export let processing = false;

  const dispatch = createEventDispatcher();

  let inputValue = '';
  let inputEl;

  $: canConfirm = !processing && (
    inputConfirm === null ||
    inputValue.trim() === inputConfirm
  );

  function handleConfirm() {
    if (!canConfirm) return;
    dispatch('confirm');
  }

  function handleCancel() {
    dispatch('cancel');
  }

  function handleBackdrop(e) {
    if (e.target === e.currentTarget) handleCancel();
  }

  function handleKeydown(e) {
    if (!open) return;
    if (e.key === 'Escape') handleCancel();
    if (e.key === 'Enter' && canConfirm && inputConfirm !== null) handleConfirm();
  }

  // Auto-focus input al abrir
  $: if (open && inputConfirm !== null) {
    setTimeout(() => inputEl?.focus(), 50);
  }
  // Reset input al cerrar
  $: if (!open) inputValue = '';

  onMount(() => window.addEventListener('keydown', handleKeydown));
  onDestroy(() => window.removeEventListener('keydown', handleKeydown));
</script>

{#if open}
  <div class="cd-backdrop" on:click={handleBackdrop} role="presentation">
    <div class="cd" role="dialog" aria-modal="true" aria-labelledby="cd-title">
      <div class="cd-inner">

        <div class="cd-head">
          <div class="cd-title variant-{variant}" id="cd-title">{title}</div>
          <button
            class="cd-close"
            on:click={handleCancel}
            title="Cerrar"
            aria-label="Cerrar"
          >×</button>
        </div>

        <div class="cd-body">
          {#if message}
            <div class="cd-message">{message}</div>
          {/if}

          <slot />

          {#if inputConfirm !== null}
            <div class="cd-confirm-label">
              Escribe <b>{inputConfirm}</b> para confirmar:
            </div>
            <input
              bind:this={inputEl}
              bind:value={inputValue}
              class="cd-confirm-input"
              class:ok={canConfirm && inputConfirm !== null}
              type="text"
              placeholder={inputConfirm}
              autocomplete="off"
              autocorrect="off"
              autocapitalize="off"
              spellcheck="false"
            />
          {/if}
        </div>

        <div class="cd-foot">
          <button class="cd-btn" on:click={handleCancel} disabled={processing}>
            {cancelLabel}
          </button>
          <div class="cd-spacer"></div>
          <button
            class="cd-btn btn-{variant}"
            on:click={handleConfirm}
            disabled={!canConfirm}
          >
            {processing ? 'Procesando...' : confirmLabel}
          </button>
        </div>

      </div>
    </div>
  </div>
{/if}

<style>
  .cd-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.6);
    backdrop-filter: blur(2px);
    -webkit-backdrop-filter: blur(2px);
    z-index: 9999;
    display: flex;
    align-items: center;
    justify-content: center;
    animation: cd-fade-in 0.15s ease-out;
  }
  @keyframes cd-fade-in { from { opacity: 0; } to { opacity: 1; } }

  .cd {
    width: 480px;
    max-width: calc(100vw - 40px);
    background: var(--border-bright);
    padding: 1px;
    font-family: var(--font-mono);
    clip-path: polygon(
      0 0, 100% 0,
      100% calc(100% - 14px),
      calc(100% - 14px) 100%,
      0 100%
    );
    box-shadow: 0 0 32px rgba(0, 0, 0, 0.6), 0 0 12px var(--accent-glow, rgba(0,255,159,0.08));
    animation: cd-slide-in 0.18s cubic-bezier(0.16, 1, 0.3, 1);
  }
  @keyframes cd-slide-in {
    from { opacity: 0; transform: translateY(-10px) scale(0.98); }
    to   { opacity: 1; transform: translateY(0) scale(1); }
  }

  .cd-inner {
    background: var(--bg-1);
    clip-path: polygon(
      0 0, 100% 0,
      100% calc(100% - 13px),
      calc(100% - 13px) 100%,
      0 100%
    );
    display: flex;
    flex-direction: column;
  }

  /* HEAD */
  .cd-head {
    padding: 14px 18px 12px;
    border-bottom: 1px solid var(--border);
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .cd-title {
    font-size: 11px;
    letter-spacing: 1.2px;
    text-transform: uppercase;
    font-weight: 600;
    color: var(--fg);
  }
  .cd-title.variant-warn   { color: var(--warn); }
  .cd-title.variant-danger { color: var(--crit); }

  .cd-close {
    margin-left: auto;
    width: 20px;
    height: 20px;
    border: 1px solid var(--border-bright);
    background: transparent;
    color: var(--fg-mute);
    font-size: 12px;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    transition: all 0.12s;
    font-family: inherit;
  }
  .cd-close:hover {
    border-color: var(--crit);
    color: var(--crit);
  }

  /* BODY */
  .cd-body {
    padding: 18px 20px;
    min-height: 60px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .cd-message {
    font-size: 12px;
    color: var(--fg-dim);
    line-height: 1.6;
    font-family: var(--font-sans, inherit);
  }

  /* Input para confirmación literal */
  .cd-confirm-label {
    font-size: 10px;
    color: var(--fg-dim);
    letter-spacing: 1px;
    text-transform: uppercase;
    margin-top: 4px;
  }
  .cd-confirm-label :global(b) {
    color: var(--crit);
    font-weight: 700;
    font-size: 11px;
  }
  .cd-confirm-input {
    width: 100%;
    padding: 10px 14px;
    background: var(--bg);
    border: 1px solid var(--border-bright);
    color: var(--fg);
    font-family: var(--font-mono);
    font-size: 13px;
    letter-spacing: 2px;
    outline: none;
    transition: border-color 0.15s, color 0.15s;
  }
  .cd-confirm-input:focus { border-color: var(--accent); }
  .cd-confirm-input.ok    { border-color: var(--ok, #00d97e); color: var(--ok, #00d97e); }

  /* FOOT */
  .cd-foot {
    padding: 12px 18px;
    border-top: 1px solid var(--border);
    background: var(--bg);
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .cd-spacer { flex: 1; }

  .cd-btn {
    padding: 8px 16px;
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 1.5px;
    text-transform: uppercase;
    background: var(--bg-2);
    border: 1px solid var(--border-bright);
    color: var(--fg-dim);
    cursor: pointer;
    transition: all 0.12s;
    clip-path: polygon(
      0 0, calc(100% - 5px) 0, 100% 5px,
      100% 100%, 5px 100%, 0 calc(100% - 5px)
    );
  }
  .cd-btn:hover:not(:disabled) {
    border-color: var(--accent);
    color: var(--accent);
    background: var(--bg-1);
  }
  .cd-btn:disabled {
    opacity: 0.35;
    cursor: not-allowed;
  }

  .cd-btn.btn-default {
    border-color: var(--accent);
    color: var(--accent);
    background: var(--accent-dim, rgba(255,145,68,0.05));
  }
  .cd-btn.btn-default:hover:not(:disabled) {
    background: rgba(255, 145, 68, 0.12);
  }
  .cd-btn.btn-warn {
    border-color: var(--warn);
    color: var(--warn);
    background: rgba(255, 184, 0, 0.05);
  }
  .cd-btn.btn-warn:hover:not(:disabled) {
    background: rgba(255, 184, 0, 0.12);
  }
  .cd-btn.btn-danger {
    border-color: var(--crit);
    color: var(--crit);
    background: rgba(255, 90, 90, 0.05);
  }
  .cd-btn.btn-danger:hover:not(:disabled) {
    background: rgba(255, 90, 90, 0.12);
  }
</style>
