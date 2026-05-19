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
   * Backend reinforcement: destroyPoolZfs / destroyPoolBtrfs rechazan
   * si el pool sigue montado (devuelven error "pool_still_mounted" o
   * "pool_still_imported"). Si eso pasa aquí, es un bug — lo mostramos.
   *
   * Flow:
   *   1. Contexto — info del pool (tamaño, discos, shares, features)
   *   2. Advertencia — qué desaparece (datos, snapshots, shares, config)
   *   3. Confirmación — escribir el nombre exacto del pool
   *
   * On confirm: POST /api/storage/pool/destroy { name }
   * Emits 'done' on success · 'cancel' if user closes.
   *
   * Usage:
   *   <DestroyPoolWizard pool={restorablePool} on:done on:cancel />
   *
   * El prop 'pool' es un objeto tal como lo devuelve /api/storage/restorable:
   *   { name, zpoolName, type, vdevType, size, health, hasBackup,
   *     hasDocker, shares, mountPoint, ... }
   */
  import { createEventDispatcher } from 'svelte';
  import { token } from '$lib/stores/auth.js';
  import WizardModal from '$lib/ui/WizardModal.svelte';
  import LED from '$lib/ui/LED.svelte';

  export let pool = null;

  const dispatch = createEventDispatcher();

  let step = 1;
  let confirmInput = '';
  let processing = false;
  let errorMsg = '';

  // El nombre que el usuario debe escribir para confirmar
  $: expectedName = pool?.name || '';

  // ─── Derived ───
  $: canAdvance = processing ? false
                : step === 1 ? true
                : step === 2 ? true
                : step === 3 ? confirmInput === expectedName && expectedName !== ''
                : false;

  $: nextLabel = step === 3 ? 'Destruir pool' : 'Continuar →';
  $: nextVariant = step === 3 ? 'danger' : (step === 2 ? 'warn' : 'primary');

  // ─── Handlers ───
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

  // ─── Destroy real ───
  /**
   * unwrapV2 tolera respuesta legacy (cuerpo directo) o v2 ({data: ...}).
   * En caso de error lanza Error con .code y .message del backend.
   */
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
      // Beta 8.1 · v2 endpoint /api/storage/pool/destroy
      //   · Payload sigue siendo {name}. Los campos type/zpoolName del
      //     legacy ya no son necesarios (Beta 8 es BTRFS-only y resuelve
      //     el pool por nombre vía SQLite, sin storage.json).
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
        // Errores semánticos del backend mapeados a mensajes para el usuario
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
      // Catch outer: errores de red
      console.error('destroy error:', err);
      errorMsg = err.message || 'Error de conexión al destruir el pool';
      processing = false;
    }
  }

  // ─── UI helpers ───
  function fmtBytes(b) {
    if (!b || b === 0) return '0 B';
    if (b >= 1e12) return (b / 1e12).toFixed(1) + ' TB';
    if (b >= 1e9)  return (b / 1e9).toFixed(1)  + ' GB';
    if (b >= 1e6)  return (b / 1e6).toFixed(0)  + ' MB';
    return b + ' B';
  }
</script>

<WizardModal
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
    <div class="pretitle">CONTEXTO</div>
    <div class="h">Pool a destruir</div>
    <div class="desc">
      Revisa la información del pool antes de continuar. Esta operación es <b>irreversible</b>
      y borrará todos los datos de los discos físicos.
    </div>

    <div class="info-grid">
      <div class="info-row">
        <div class="info-label">Nombre</div>
        <div class="info-value mono">{pool?.name || '—'}</div>
      </div>
      {#if pool?.btrfs_uuid}
        <div class="info-row">
          <div class="info-label">UUID</div>
          <div class="info-value mono sm">{pool.btrfs_uuid}</div>
        </div>
      {/if}
      <div class="info-row">
        <div class="info-label">Sistema</div>
        <div class="info-value">BTRFS · {pool?.profile || 'single'}</div>
      </div>
      <div class="info-row">
        <div class="info-label">Capacidad</div>
        <div class="info-value">{fmtBytes(pool?.usage?.total_bytes) || '—'}</div>
      </div>
      <div class="info-row">
        <div class="info-label">Estado</div>
        <div class="info-value">
          <LED size={7} variant={pool?.health?.status === 'healthy' ? 'ok' : 'warn'} />
          <span>{pool?.health?.status || 'unknown'}</span>
        </div>
      </div>
      <div class="info-row">
        <div class="info-label">Mount point</div>
        <div class="info-value mono sm">{pool?.mount_point || '—'}</div>
      </div>
    </div>

    {#if pool?.shares?.length > 0}
      <div class="shares-note">
        <b>Shares:</b>
        {pool.shares.join(', ')}
        — se eliminarán todos al destruir
      </div>
    {/if}
  {/if}

  <!-- PASO 2 · Advertencia fuerte -->
  {#if step === 2}
    <div class="pretitle danger">ADVERTENCIA</div>
    <div class="h danger">Esta acción no se puede deshacer</div>
    <div class="desc">
      Al destruir <b class="mono">{expectedName}</b> se eliminará <b>todo</b> de forma permanente:
    </div>

    <ul class="bullets danger-bullets">
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
      <li>Las <b>firmas de ZFS/BTRFS</b> en los discos físicos</li>
    </ul>

    <div class="desc callout">
      Los discos quedarán <b>libres</b> para usarse en un pool nuevo.
      No hay recuperación posible después de este punto.
    </div>
  {/if}

  <!-- PASO 3 · Confirmación por nombre -->
  {#if step === 3}
    <div class="pretitle danger">CONFIRMACIÓN FINAL</div>
    <div class="h">Escribe el nombre del pool para confirmar</div>
    <div class="desc">
      Para evitar destrucciones accidentales, escribe exactamente el nombre del pool:
      <span class="mono pool-chip">{expectedName}</span>
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
        El nombre no coincide. Debe ser exactamente <span class="mono">{expectedName}</span>.
      </div>
    {/if}

    {#if errorMsg}
      <div class="err">{errorMsg}</div>
    {/if}
  {/if}

</WizardModal>

<style>
  .pretitle {
    font-size: 9px;
    color: var(--fg-faint);
    letter-spacing: 2px;
    text-transform: uppercase;
    font-family: var(--font-mono);
  }
  .pretitle.danger { color: var(--crit); }

  .h {
    font-size: 15px;
    color: var(--fg);
    letter-spacing: 0.4px;
    font-family: var(--font-sans, inherit);
    font-weight: 500;
    line-height: 1.3;
  }
  .h.danger { color: var(--crit); }

  .desc {
    font-size: 12px;
    color: var(--fg-dim);
    line-height: 1.6;
    font-family: var(--font-sans, inherit);
  }
  .desc :global(b) { color: var(--accent); font-weight: 600; }
  .desc.callout {
    padding: 10px 12px;
    background: rgba(255, 90, 90, 0.06);
    border-left: 3px solid var(--crit);
    margin-top: 4px;
  }
  .desc.callout :global(b) { color: var(--crit); }

  /* Info grid (paso 1) */
  .info-grid {
    display: flex;
    flex-direction: column;
    background: var(--bg);
    border: 1px solid var(--border);
  }
  .info-row {
    display: grid;
    grid-template-columns: 120px 1fr;
    align-items: center;
    gap: 12px;
    padding: 8px 14px;
    border-bottom: 1px solid var(--border);
    font-size: 11px;
  }
  .info-row:last-child { border-bottom: none; }
  .info-label {
    color: var(--fg-faint);
    letter-spacing: 1px;
    text-transform: uppercase;
    font-family: var(--font-mono);
    font-size: 9px;
  }
  .info-value {
    color: var(--fg);
    font-family: var(--font-sans, inherit);
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .info-value.mono, .mono { font-family: var(--font-mono); }
  .info-value.sm, .sm { font-size: 10px; }

  .shares-note {
    padding: 8px 12px;
    background: rgba(255, 184, 0, 0.05);
    border-left: 3px solid var(--warn, #ffb800);
    font-size: 11px;
    color: var(--fg-dim);
    font-family: var(--font-mono);
    letter-spacing: 0.3px;
    line-height: 1.4;
  }
  .shares-note :global(b) { color: var(--warn, #ffb800); }

  /* Bullets */
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
    color: var(--fg-dim);
    padding-left: 18px;
    position: relative;
    line-height: 1.5;
    font-family: var(--font-sans, inherit);
  }
  .bullets li::before {
    content: '✕';
    position: absolute;
    left: 4px;
    color: var(--crit);
    font-weight: 700;
  }
  .bullets li :global(b) {
    color: var(--fg);
    font-weight: 600;
  }
  .danger-bullets li :global(b) { color: var(--fg); }

  /* Confirm input */
  .pool-chip {
    display: inline-block;
    padding: 2px 8px;
    background: rgba(255, 90, 90, 0.1);
    border: 1px solid var(--crit);
    color: var(--crit);
    font-weight: 600;
    letter-spacing: 0.5px;
  }

  .confirm-input {
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
    margin-top: 4px;
  }
  .confirm-input:focus { border-color: var(--crit); }
  .confirm-input.ok    { border-color: var(--ok, #00d97e); color: var(--ok, #00d97e); }
  .confirm-input:disabled { opacity: 0.5; cursor: not-allowed; }

  .hint-mismatch {
    font-size: 10px;
    color: var(--fg-mute);
    font-family: var(--font-mono);
    letter-spacing: 0.3px;
    margin-top: -2px;
  }
  .hint-mismatch :global(.mono) { color: var(--fg); }

  .err {
    padding: 10px 12px;
    background: rgba(255, 90, 90, 0.08);
    border-left: 3px solid var(--crit);
    font-size: 11px;
    color: var(--crit);
    font-family: var(--font-mono);
    letter-spacing: 0.3px;
    line-height: 1.5;
  }
</style>
