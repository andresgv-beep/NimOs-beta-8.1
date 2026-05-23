<script>
  /**
   * WizardModal · Modal multipaso con stepper
   * ───────────────────────────────────────────
   * Marco de wizard reutilizable. La app que lo usa se encarga del contenido
   * de cada paso, este componente gestiona:
   *   - Header con título, stepper, botón cerrar
   *   - Barra de progreso
   *   - Footer con botones Atrás / Cancelar / Siguiente (contextuales)
   *   - ESC cierra con confirmación opcional
   *
   * Uso:
   *   <WizardModal
   *     open={true}
   *     title="Desmontar pool"
   *     tag="data3"
   *     tagColor="accent"
   *     currentStep={2}
   *     totalSteps={3}
   *     canAdvance={servicesStopped}
   *     canGoBack={true}
   *     nextLabel="Continuar →"
   *     on:next={handleNext}
   *     on:back={handleBack}
   *     on:cancel={handleCancel}
   *   >
   *     <!-- contenido del paso actual -->
   *     <MyStep2Content />
   *   </WizardModal>
   */
  import { createEventDispatcher, onMount, onDestroy } from 'svelte';

  export let open = false;
  export let title = 'Wizard';
  /** Tag opcional que aparece tras el título (ej. nombre del pool) */
  export let tag = '';
  /** 'accent' | 'warn' | 'danger' · color del tag */
  export let tagColor = 'accent';
  export let currentStep = 1;
  export let totalSteps = 3;
  /** Habilita el botón "Continuar" */
  export let canAdvance = true;
  /** Habilita el botón "Atrás" */
  export let canGoBack = true;
  /** Label botón siguiente (cambia en último paso) */
  export let nextLabel = 'Continuar →';
  /** Label botón cancelar */
  export let cancelLabel = 'Cancelar';
  /** Variante del botón siguiente · 'primary' | 'danger' | 'warn' */
  export let nextVariant = 'primary';
  /** Ancho del modal en px */
  export let width = 560;

  const dispatch = createEventDispatcher();

  $: progress = totalSteps > 0 ? (currentStep / totalSteps) * 100 : 0;
  $: isFirstStep = currentStep === 1;
  $: isLastStep  = currentStep === totalSteps;

  function handleNext()   { if (canAdvance) dispatch('next'); }
  function handleBack()   { if (canGoBack && !isFirstStep) dispatch('back'); }
  function handleCancel() { dispatch('cancel'); }

  function handleBackdrop(e) {
    if (e.target === e.currentTarget) handleCancel();
  }

  function handleKeydown(e) {
    if (!open) return;
    if (e.key === 'Escape') handleCancel();
  }

  onMount(() => window.addEventListener('keydown', handleKeydown));
  onDestroy(() => window.removeEventListener('keydown', handleKeydown));
</script>

{#if open}
  <div class="wm-backdrop" on:click={handleBackdrop} role="presentation">
    <div class="wm" style="width: {width}px;" role="dialog" aria-modal="true">
      <div class="wm-inner">

        <div class="wm-head">
          <div class="wm-title">
            {title}
            {#if tag}
              <span class="wm-tag tag-{tagColor}">"{tag}"</span>
            {/if}
          </div>
          <div class="wm-step">
            PASO <span class="cur">{currentStep}</span>/{totalSteps}
          </div>
          <button
            class="wm-close"
            on:click={handleCancel}
            title="Cerrar"
            aria-label="Cerrar"
          >×</button>
        </div>

        <div class="wm-progress">
          <div class="wm-progress-bar" style="width: {progress}%"></div>
        </div>

        <div class="wm-body">
          <slot />
        </div>

        <div class="wm-foot">
          <button
            class="wm-btn"
            on:click={handleBack}
            disabled={!canGoBack || isFirstStep}
          >← Atrás</button>

          {#if !isLastStep}
            <button class="wm-btn" on:click={handleCancel}>{cancelLabel}</button>
          {/if}

          <div class="wm-spacer"></div>

          <button
            class="wm-btn btn-{nextVariant}"
            on:click={handleNext}
            disabled={!canAdvance}
          >{nextLabel}</button>
        </div>

      </div>
    </div>
  </div>
{/if}

<style>
  .wm-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.6);
    backdrop-filter: blur(2px);
    -webkit-backdrop-filter: blur(2px);
    z-index: 9998;
    display: flex;
    align-items: center;
    justify-content: center;
    animation: wm-fade 0.15s ease-out;
  }
  @keyframes wm-fade { from { opacity: 0; } to { opacity: 1; } }

  .wm {
    max-width: calc(100% - 40px);
    max-height: calc(100vh - 80px);
    background: var(--border-bright);
    padding: 1px;
    font-family: var(--font-mono);
    clip-path: polygon(
      0 0, 100% 0,
      100% calc(100% - 14px),
      calc(100% - 14px) 100%,
      0 100%
    );
    box-shadow: 0 0 32px rgba(0, 0, 0, 0.6), 0 0 12px var(--accent-glow, rgba(255,145,68,0.08));
    animation: wm-in 0.18s cubic-bezier(0.16, 1, 0.3, 1);
  }
  @keyframes wm-in {
    from { opacity: 0; transform: translateY(-10px) scale(0.98); }
    to   { opacity: 1; transform: translateY(0) scale(1); }
  }

  .wm-inner {
    background: var(--bg-1);
    clip-path: polygon(
      0 0, 100% 0,
      100% calc(100% - 13px),
      calc(100% - 13px) 100%,
      0 100%
    );
    display: flex;
    flex-direction: column;
    max-height: inherit;
  }

  /* HEAD */
  .wm-head {
    padding: 16px 20px 14px;
    border-bottom: 1px solid var(--border);
    display: flex;
    align-items: center;
    gap: 16px;
    flex-shrink: 0;
  }
  .wm-title {
    font-size: 11px;
    color: var(--fg);
    letter-spacing: 1.2px;
    text-transform: uppercase;
    font-weight: 600;
  }
  .wm-tag {
    font-weight: 700;
    margin-left: 2px;
  }
  .wm-tag.tag-accent { color: var(--accent); }
  .wm-tag.tag-warn   { color: var(--warn); }
  .wm-tag.tag-danger { color: var(--crit); }

  .wm-step {
    font-size: 9px;
    color: var(--fg-mute);
    letter-spacing: 1.5px;
    margin-left: auto;
  }
  .wm-step .cur { color: var(--accent); font-weight: 600; }

  .wm-close {
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
  .wm-close:hover {
    border-color: var(--crit);
    color: var(--crit);
  }

  /* PROGRESS BAR */
  .wm-progress {
    height: 2px;
    background: var(--border);
    position: relative;
    overflow: hidden;
    flex-shrink: 0;
  }
  .wm-progress-bar {
    position: absolute;
    left: 0; top: 0; bottom: 0;
    background: var(--accent);
    box-shadow: 0 0 6px var(--accent-glow, rgba(255,145,68,0.25));
    transition: width 0.3s cubic-bezier(0.16, 1, 0.3, 1);
  }

  /* BODY */
  .wm-body {
    padding: 22px 22px 20px;
    flex: 1;
    overflow-y: auto;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  /* FOOT */
  .wm-foot {
    padding: 14px 18px;
    border-top: 1px solid var(--border);
    background: var(--bg);
    display: flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
  }
  .wm-spacer { flex: 1; }

  .wm-btn {
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
  .wm-btn:hover:not(:disabled) {
    border-color: var(--accent);
    color: var(--accent);
    background: var(--bg-1);
  }
  .wm-btn:disabled {
    opacity: 0.35;
    cursor: not-allowed;
  }

  .wm-btn.btn-primary {
    border-color: var(--accent);
    color: var(--accent);
    background: var(--accent-dim, rgba(255,145,68,0.05));
  }
  .wm-btn.btn-primary:hover:not(:disabled) {
    background: rgba(255, 145, 68, 0.12);
  }
  .wm-btn.btn-warn {
    border-color: var(--warn);
    color: var(--warn);
    background: rgba(255, 184, 0, 0.05);
  }
  .wm-btn.btn-warn:hover:not(:disabled) {
    background: rgba(255, 184, 0, 0.12);
  }
  .wm-btn.btn-danger {
    border-color: var(--crit);
    color: var(--crit);
    background: rgba(255, 90, 90, 0.05);
  }
  .wm-btn.btn-danger:hover:not(:disabled) {
    background: rgba(255, 90, 90, 0.12);
  }
</style>
