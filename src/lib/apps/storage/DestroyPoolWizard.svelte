<script>
  /**
   * DestroyPoolWizard · Wizard to permanently destroy a pool
   * ──────────────────────────────────────────────────────────
   * Solo se puede destruir un pool que ya está desmontado (exported).
   * El flujo es: Desmontar → ir a Discos → Destruir.
   *
   * Frontend reinforcement: este componente solo se abre desde la vista
   * "Pools desmontados" (sección Discos) o desde "Restaurar". Nunca desde
   * un pool activo en Resumen.
   *
   * Backend reinforcement: destroyPoolBtrfs rechaza si el pool sigue
   * montado (devuelve error "pool_still_mounted" o "pool_still_imported").
   * Si eso pasa aquí, es un bug — lo mostramos.
   *
   * Flow:
   *   1. Contexto — info del pool (tamaño, discos, shares, features)
   *   2. Advertencia — qué desaparece (datos, snapshots, shares, config)
   *   3. Confirmación — escribir el nombre exacto del pool
   *
   * On confirm: POST /api/storage/v2/pool/destroy { name }
   * Emits 'done' on success · 'cancel' if user closes.
   *
   * NOTA Beta 8.1: este wizard usa el frame visual nuevo (WizardFrame) del
   * Design System Beta 8.1. La LÓGICA es idéntica a la versión anterior —
   * solo cambia presentación.
   */
  import { createEventDispatcher } from 'svelte';
  import { token } from '$lib/stores/auth.js';
  import WizardFrame from '$lib/ui/WizardFrame.svelte';

  export let pool = null;

  const dispatch = createEventDispatcher();

  let step = 1;
  let confirmInput = '';
  let processing = false;
  let errorMsg = '';

  $: expectedName = pool?.name || '';

  $: canAdvance = processing ? false
                : step === 1 ? true
                : step === 2 ? true
                : step === 3 ? confirmInput === expectedName && expectedName !== ''
                : false;

  $: nextLabel = step === 3 ? 'Destruir pool' : 'Continuar →';
  $: nextVariant = step === 3 ? 'danger' : (step === 2 ? 'warn' : 'primary');

  function handleNext() {
    if (step === 3) {
      submitDestroy();
      return;
    }
    step += 1;
  }

  function handleBack() {
    if (step > 1) {
      step -= 1;
      errorMsg = '';
    }
  }

  function handleCancel() {
    if (processing) return;
    dispatch('cancel');
  }

  async function unwrapV2(res, label = 'api call') {
    let body;
    try {
      body = await res.json();
    } catch {
      throw new Error(`${label}: invalid JSON response (status ${res.status})`);
    }
    if (!res.ok) {
      let code = `http_${res.status}`;
      let msg = res.statusText || 'request failed';
      let details;
      if (body?.error) {
        if (typeof body.error === 'string') {
          msg = body.error;
          code = body.error;
        } else if (typeof body.error === 'object') {
          code = body.error.code || code;
          msg = body.error.message || msg;
          details = body.error.details;
        }
      }
      const e = new Error(msg);
      e.code = code;
      e.details = details;
      throw e;
    }
    if (body && typeof body === 'object' && 'data' in body && !Array.isArray(body)) {
      return body.data;
    }
    return body;
  }

  async function submitDestroy() {
    if (!expectedName) return;
    processing = true;
    errorMsg = '';
    try {
      const payload = { name: expectedName };
      const res = await fetch('/api/storage/v2/pool/destroy', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${$token}`,
        },
        body: JSON.stringify(payload),
      });
      try {
        await unwrapV2(res, 'destroy');
        processing = false;
        dispatch('done');
      } catch (e) {
        if (e.code === 'pool_still_mounted' || e.code === 'pool_still_imported') {
          errorMsg = 'El pool sigue montado. Desmóntalo antes de destruirlo.';
        } else if (e.code === 'destroy_failed') {
          errorMsg = e.message || 'No se pudo destruir el pool. Revisa el sistema y reintenta.';
        } else {
          errorMsg = e.message || 'Error al destruir el pool';
        }
        processing = false;
      }
    } catch (err) {
      console.error('destroy error:', err);
      errorMsg = err.message || 'Error de conexión al destruir el pool';
      processing = false;
    }
  }

  function fmtBytes(b) {
    if (!b || b === 0) return '0 B';
    if (b >= 1e12) return (b / 1e12).toFixed(1) + ' TB';
    if (b >= 1e9)  return (b / 1e9).toFixed(1)  + ' GB';
    if (b >= 1e6)  return (b / 1e6).toFixed(0)  + ' MB';
    return b + ' B';
  }

  $: deviceList = (pool?.devices || [])
    .map(d => typeof d === 'string' ? d : (d.current_path || ''))
    .filter(Boolean);
</script>

<WizardFrame
  open={true}
  title="Destruir pool"
  tag={expectedName}
  tagColor="danger"
  currentStep={step}
  totalSteps={3}
  {canAdvance}
  canGoBack={step > 1 && !processing}
  {nextLabel}
  {nextVariant}
  cancelLabel={processing ? 'Procesando...' : 'Cancelar'}
  on:next={handleNext}
  on:back={handleBack}
  on:cancel={handleCancel}
>
  <!-- PASO 1 · Contexto -->
  {#if step === 1}
    <div class="step-label">Contexto</div>
    <p class="step-desc">
      Revisa la información del pool antes de continuar. Esta operación es
      <b>irreversible</b> y borrará todos los datos de los discos físicos.
    </p>

    <div class="impact-card">
      <div class="impact-row">
        <span class="k">nombre</span>
        <span class="v">{pool?.name || '—'}</span>
      </div>
      {#if pool?.btrfs_uuid}
        <div class="impact-row">
          <span class="k">uuid</span>
          <span class="v sm">{pool.btrfs_uuid}</span>
        </div>
      {/if}
      <div class="impact-row">
        <span class="k">sistema</span>
        <span class="v">BTRFS · {pool?.profile || 'single'}</span>
      </div>
      <div class="impact-row">
        <span class="k">capacidad</span>
        <span class="v">{fmtBytes(pool?.usage?.total_bytes) || '—'}</span>
      </div>
      <div class="impact-row">
        <span class="k">estado</span>
        <span class="v">{pool?.health?.status || 'unknown'}</span>
      </div>
      <div class="impact-row">
        <span class="k">mount point</span>
        <span class="v sm">{pool?.mount_point || '—'}</span>
      </div>
    </div>

    {#if pool?.shares?.length > 0}
      <div class="notice">
        <b>Shares:</b> {pool.shares.join(', ')} — se eliminarán al destruir.
      </div>
    {/if}
  {/if}

  <!-- PASO 2 · Advertencia -->
  {#if step === 2}
    <div class="step-label">Advertencia</div>
    <p class="step-desc">
      Al destruir <b>{expectedName}</b> se eliminará <b>todo</b> de forma permanente:
    </p>

    <ul class="bullets">
      <li>Todos los <b>datos</b> almacenados en el pool</li>
      <li>Todos los <b>snapshots</b> (incluidos los marcados como importantes)</li>
      {#if pool?.shares?.length > 0}
        <li>Todos los <b>shares</b> ({pool.shares.length}) y su configuración</li>
      {/if}
      {#if pool?.hasBackup}
        <li>La <b>copia de configuración</b> de NimOS guardada en este pool</li>
      {/if}
      {#if pool?.hasDocker}
        <li>Los <b>datos de Docker</b> (volúmenes, imágenes, containers)</li>
      {/if}
      <li>Las <b>firmas BTRFS</b> en los discos físicos</li>
    </ul>

    <div class="alert-crit">
      <b>No se puede deshacer.</b> Los discos quedarán libres para usarse
      en un pool nuevo. No hay recuperación posible después de este punto.
    </div>
  {/if}

  <!-- PASO 3 · Confirmación final -->
  {#if step === 3}
    <div class="step-label">Confirmación final</div>
    <p class="step-desc">
      Estás a punto de <b>destruir permanentemente</b> el pool y todos sus datos.
      Esta operación borrará el sistema de archivos de los discos.
    </p>

    <div class="impact-card">
      <div class="impact-row">
        <span class="k">pool</span>
        <span class="v">{expectedName}</span>
      </div>
      {#if pool?.usage?.used_bytes}
        <div class="impact-row">
          <span class="k">datos</span>
          <span class="v crit">{fmtBytes(pool.usage.used_bytes)} se perderán</span>
        </div>
      {/if}
      {#if deviceList.length > 0}
        <div class="impact-row">
          <span class="k">discos a liberar</span>
          <span class="v">{deviceList.join(', ')}</span>
        </div>
      {/if}
    </div>

    <div class="confirm-block">
      <div class="confirm-label">
        Escribe el nombre del pool <b>{expectedName}</b> para confirmar:
      </div>
      <input
        class="confirm-input"
        class:ok={confirmInput === expectedName && expectedName !== ''}
        type="text"
        bind:value={confirmInput}
        placeholder={expectedName}
        autocomplete="off"
        autocorrect="off"
        autocapitalize="off"
        spellcheck="false"
        disabled={processing}
      />
      {#if confirmInput && confirmInput !== expectedName}
        <div class="hint-mismatch">
          El nombre no coincide. Debe ser exactamente {expectedName}.
        </div>
      {/if}
    </div>

    {#if errorMsg}
      <div class="alert-crit">{errorMsg}</div>
    {/if}
  {/if}
</WizardFrame>

<style>
  .step-label {
    font-size: 10px;
    color: var(--ink-trace);
    text-transform: uppercase;
    letter-spacing: 1.5px;
    font-weight: 600;
    margin-bottom: 2px;
    font-family: var(--font-sans);
  }

  .step-desc {
    font-size: 12px;
    color: var(--ink-dim);
    line-height: 1.6;
    font-family: var(--font-sans);
  }
  .step-desc :global(b) {
    color: var(--ink);
    font-weight: 600;
    font-family: var(--font-mono);
  }

  .impact-card {
    background: var(--bg-card);
    border: 1px solid var(--line);
    border-radius: 8px;
    padding: 14px 16px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .impact-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    font-size: 12px;
  }
  .impact-row .k {
    color: var(--ink-mute);
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
  }
  .impact-row .v {
    color: var(--ink);
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 500;
    text-align: right;
    word-break: break-all;
  }
  .impact-row .v.sm { font-size: 10px; }
  .impact-row .v.crit { color: var(--crit); font-weight: 600; }

  .notice {
    background: rgba(251, 191, 36, 0.06);
    border-left: 3px solid var(--warn);
    padding: 10px 12px;
    border-radius: 4px;
    font-size: 11px;
    color: var(--ink-dim);
    line-height: 1.5;
    font-family: var(--font-sans);
  }
  .notice :global(b) { color: var(--warn); font-weight: 600; }

  .alert-crit {
    background: rgba(248, 113, 113, 0.06);
    border-left: 3px solid var(--crit);
    padding: 12px 14px;
    border-radius: 4px;
    font-size: 11px;
    color: var(--ink-dim);
    line-height: 1.6;
    font-family: var(--font-sans);
  }
  .alert-crit :global(b) { color: var(--crit); font-weight: 600; }

  .bullets {
    list-style: none;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 0;
  }
  .bullets li {
    font-size: 12px;
    color: var(--ink-dim);
    padding-left: 18px;
    position: relative;
    line-height: 1.5;
    font-family: var(--font-sans);
  }
  .bullets li::before {
    content: '✕';
    position: absolute;
    left: 4px;
    color: var(--crit);
    font-weight: 700;
  }
  .bullets li :global(b) {
    color: var(--ink);
    font-weight: 600;
  }

  .confirm-block {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .confirm-label {
    font-size: 11px;
    color: var(--ink-dim);
    font-family: var(--font-sans);
  }
  .confirm-label :global(b) {
    color: var(--crit);
    font-family: var(--font-mono);
    font-weight: 700;
    letter-spacing: 1px;
  }
  .confirm-input {
    padding: 9px 12px;
    border-radius: 6px;
    background: var(--bg-inner);
    border: 1px solid var(--line);
    color: var(--ink);
    font-size: 13px;
    font-family: var(--font-mono);
    font-weight: 600;
    letter-spacing: 1.5px;
    outline: none;
    transition: border-color 0.2s, background 0.2s, color 0.2s;
  }
  .confirm-input:focus {
    border-color: var(--crit);
    background: rgba(248, 113, 113, 0.04);
  }
  .confirm-input.ok {
    border-color: var(--crit);
    color: var(--crit);
  }
  .confirm-input:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .hint-mismatch {
    font-size: 10px;
    color: var(--ink-mute);
    font-family: var(--font-mono);
    letter-spacing: 0.3px;
  }
</style>
