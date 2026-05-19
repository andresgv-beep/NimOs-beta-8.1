<script>
  /**
   * ExportPoolWizard · Wizard to export a ZFS/BTRFS pool
   * ─────────────────────────────────────────────────────
   * Called "Desmontar" in the UI but technically this does a pool export
   * (matches backend endpoint POST /api/storage/pool/export).
   *
   * Flow:
   *   1. Detect dependent services (GET /api/services/dependencies?pool=X)
   *   2. If services → list with "→ NimHealth" button + poll every 3s
   *      If no services → skip directly to step 3
   *   3. Final confirmation with literal input "DESMONTAR"
   *
   * On confirm: POST /api/storage/pool/export { name }
   * Emits 'done' on success · 'cancel' if user closes.
   *
   * Usage:
   *   <ExportPoolWizard poolName="data3" on:done on:cancel />
   */
  import { createEventDispatcher, onMount, onDestroy } from 'svelte';
  import { token } from '$lib/stores/auth.js';
  import { openWindow } from '$lib/stores/windows.js';
  import WizardModal from '$lib/ui/WizardModal.svelte';
  import LED from '$lib/ui/LED.svelte';

  export let poolName = '';

  const dispatch = createEventDispatcher();

  let step = 1;                    // 1 = detectando · 2 = servicios · 3 = confirmar
  let loading = true;              // carga inicial
  let deps = [];                   // servicios activos
  let pollInterval = null;         // timer de re-verificación
  let confirmInput = '';           // input de confirmación
  let processing = false;          // estado del POST final
  let errorMsg = '';               // error al desmontar

  // ─── Derived ───
  $: allStopped = deps.length === 0 || deps.every(d => d.status === 'stopped' || d.status === 'exited');
  $: canAdvance = step === 1 ? false
                : step === 2 ? allStopped
                : step === 3 ? confirmInput.trim() === 'DESMONTAR' && !processing
                : false;

  // ─── Fetch dependencies ───
  async function fetchDeps() {
    try {
      const res = await fetch(`/api/services/dependencies?pool=${encodeURIComponent(poolName)}`, {
        headers: { 'Authorization': `Bearer ${$token}` },
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      deps = data.dependencies || [];
      return true;
    } catch (err) {
      console.error('fetchDeps error:', err);
      errorMsg = 'No se pudo consultar dependencias del pool.';
      return false;
    }
  }

  // ─── Handlers ───
  async function handleNext() {
    if (step === 2) {
      step = 3;
      stopPolling();
      return;
    }
    if (step === 3) {
      await submitExport();
      return;
    }
  }

  function handleBack() {
    if (step === 2) {
      // No debería pasar (step 1 → 2 es automático) pero por seguridad
      return;
    }
    if (step === 3) {
      step = deps.length > 0 ? 2 : 1;
      if (step === 2) startPolling();
    }
  }

  function handleCancel() {
    stopPolling();
    dispatch('cancel');
  }

  function openNimHealth() {
    openWindow('nimhealth');
  }

  // ─── Polling de re-verificación (solo paso 2) ───
  function startPolling() {
    stopPolling();
    pollInterval = setInterval(fetchDeps, 3000);
  }
  function stopPolling() {
    if (pollInterval) {
      clearInterval(pollInterval);
      pollInterval = null;
    }
  }

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

  // ─── Export real ───
  async function submitExport() {
    processing = true;
    errorMsg = '';
    try {
      const res = await fetch('/api/storage/v2/pool/export', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${$token}`,
        },
        body: JSON.stringify({ name: poolName }),
      });
      try {
        await unwrapV2(res, 'export');
        processing = false;
        dispatch('done');
      } catch (e) {
        // Error semántico: services_active devuelve la lista de servicios
        // afectados en e.details. El backend v2 envía esa lista en
        // {error:{code:'services_active', details:{services:[...]}}}
        if (e.code === 'services_active') {
          const services = e.details?.services || [];
          errorMsg = `Algún servicio se ha levantado: ${services.join(', ')}. Reintenta.`;
          // Retrocedemos al paso 2 para que el usuario vea el estado
          await fetchDeps();
          step = 2;
          startPolling();
        } else {
          errorMsg = e.message || 'Error al desmontar';
        }
        processing = false;
      }
    } catch (err) {
      console.error('export error:', err);
      errorMsg = err.message || 'Error de conexión al desmontar';
      processing = false;
    }
  }

  // ─── Lifecycle ───
  onMount(async () => {
    await fetchDeps();
    loading = false;
    // Si no hay dependencias, salta directo al paso 3
    if (deps.length === 0) {
      step = 3;
    } else {
      step = 2;
      startPolling();
    }
  });

  onDestroy(() => {
    stopPolling();
  });

  // ─── UI helpers ───
  function statusLabel(s) {
    if (s === 'running')  return 'running';
    if (s === 'stopped')  return 'stopped';
    if (s === 'exited')   return 'stopped';
    if (s === 'starting') return 'starting';
    if (s === 'stopping') return 'stopping';
    return s || 'unknown';
  }
  function statusLedVariant(s) {
    if (s === 'running')  return 'ok';
    if (s === 'starting' || s === 'stopping') return 'warn';
    return 'off';
  }
</script>

<WizardModal
  open={true}
  title="Desmontar pool"
  tag={poolName}
  tagColor="accent"
  currentStep={step === 1 ? 1 : step === 2 ? 2 : 3}
  totalSteps={3}
  canAdvance={canAdvance}
  canGoBack={step === 3 && deps.length > 0 && !processing}
  nextLabel={step === 3 ? 'Desmontar pool' : 'Continuar →'}
  nextVariant={step === 3 ? 'danger' : 'primary'}
  on:next={handleNext}
  on:back={handleBack}
  on:cancel={handleCancel}
>

  <!-- PASO 1 · Detección inicial -->
  {#if step === 1}
    <div class="pretitle">DETECCIÓN</div>
    <div class="h">Verificando servicios dependientes...</div>
    <div class="desc">
      Antes de desmontar el pool, NimOS comprueba qué servicios están usándolo activamente.
    </div>
    <div class="recheck">
      <span class="spin">⟳</span>
      <span>Consultando daemon...</span>
    </div>
    {#if errorMsg}
      <div class="err">{errorMsg}</div>
    {/if}
  {/if}

  <!-- PASO 2 · Lista de servicios -->
  {#if step === 2}
    <div class="pretitle">
      {#if allStopped}
        SERVICIOS DETENIDOS · {deps.length}
      {:else}
        SERVICIOS ACTIVOS · {deps.filter(d => d.status !== 'stopped' && d.status !== 'exited').length}
      {/if}
    </div>
    <div class="h">
      {#if allStopped}
        Todo listo para desmontar
      {:else}
        Detén los servicios antes de continuar
      {/if}
    </div>
    <div class="desc">
      {#if allStopped}
        Ningún servicio está usando el pool en este momento. Puedes continuar con el desmontaje.
      {:else}
        Los siguientes servicios están usando el pool. Ve a <b>NimHealth</b> para detenerlos.
        El wizard detectará automáticamente cuando estén todos parados.
      {/if}
    </div>

    <div class="svc-list">
      {#each deps as dep}
        <div class="svc-row">
          <LED size={8} variant={statusLedVariant(dep.status)} />
          <div class="svc-name">
            {dep.app || dep.id}
            {#if dep.appId && dep.appId !== dep.app}
              <span class="svc-tag">@{poolName}</span>
            {/if}
          </div>
          <div class="svc-state state-{dep.status === 'running' ? 'run' : 'stop'}">
            {statusLabel(dep.status)}
          </div>
          {#if dep.status === 'running' || dep.status === 'starting'}
            <button class="svc-action" on:click={openNimHealth}>→ NimHealth</button>
          {:else}
            <div class="svc-action muted">—</div>
          {/if}
        </div>
      {/each}
    </div>

    {#if allStopped}
      <div class="recheck ok">
        <span>✓</span>
        <span>Todos los servicios detenidos</span>
      </div>
    {:else}
      <div class="recheck">
        <span class="spin">⟳</span>
        <span>Re-verificando cada 3s...</span>
      </div>
    {/if}
    {#if errorMsg}
      <div class="err">{errorMsg}</div>
    {/if}
  {/if}

  <!-- PASO 3 · Confirmación final -->
  {#if step === 3}
    <div class="pretitle">CONFIRMACIÓN</div>
    <div class="h">Última comprobación antes de desmontar</div>

    <ul class="bullets">
      <li>Los datos <b>siguen intactos</b> en los discos físicos</li>
      <li>El pool <b>desaparece del sistema</b> temporalmente</li>
      <li>Puedes <b>reimportarlo después</b> desde la vista "Restaurar"</li>
      <li>Esta acción <b>detendrá el acceso</b> a /mnt/{poolName} inmediatamente</li>
    </ul>

    <div class="confirm-label">Escribe <b>DESMONTAR</b> para confirmar:</div>
    <input
      class="confirm-input"
      class:ok={confirmInput.trim() === 'DESMONTAR'}
      type="text"
      bind:value={confirmInput}
      placeholder="DESMONTAR"
      autocomplete="off"
      autocorrect="off"
      autocapitalize="off"
      spellcheck="false"
      disabled={processing}
    />

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
  .h {
    font-size: 15px;
    color: var(--fg);
    letter-spacing: 0.4px;
    font-family: var(--font-sans, inherit);
    font-weight: 500;
    line-height: 1.3;
  }
  .desc {
    font-size: 12px;
    color: var(--fg-dim);
    line-height: 1.6;
    font-family: var(--font-sans, inherit);
  }
  .desc :global(b) { color: var(--accent); font-weight: 600; }

  .recheck {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 10px;
    color: var(--fg-mute);
    letter-spacing: 0.5px;
    font-family: var(--font-mono);
    margin-top: 4px;
  }
  .recheck.ok { color: var(--ok, #00d97e); }
  .recheck .spin {
    display: inline-block;
    animation: spin 1s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }

  /* Service list */
  .svc-list {
    background: var(--bg);
    border: 1px solid var(--border);
    display: flex;
    flex-direction: column;
  }
  .svc-row {
    display: grid;
    grid-template-columns: 14px 1fr auto auto;
    align-items: center;
    gap: 12px;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border);
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .svc-row:last-child { border-bottom: none; }
  .svc-name { color: var(--fg); letter-spacing: 0.3px; }
  .svc-tag { color: var(--fg-faint); margin-left: 4px; font-size: 10px; }
  .svc-state {
    font-size: 9px;
    letter-spacing: 1.5px;
    text-transform: uppercase;
  }
  .svc-state.state-run  { color: var(--crit); }
  .svc-state.state-stop { color: var(--fg-mute); }

  .svc-action {
    font-size: 9px;
    letter-spacing: 1px;
    text-transform: uppercase;
    padding: 4px 10px;
    background: transparent;
    border: 1px solid var(--border-bright);
    color: var(--fg-dim);
    cursor: pointer;
    font-family: inherit;
    transition: all 0.12s;
    clip-path: polygon(
      0 0, calc(100% - 4px) 0, 100% 4px,
      100% 100%, 4px 100%, 0 calc(100% - 4px)
    );
  }
  .svc-action:hover:not(.muted) {
    border-color: var(--accent);
    color: var(--accent);
  }
  .svc-action.muted {
    opacity: 0.35;
    cursor: default;
    padding: 4px 10px;
    display: inline-block;
  }

  /* Bullets en paso 3 */
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
    content: '›';
    position: absolute;
    left: 4px;
    color: var(--accent);
    font-weight: 700;
  }
  .bullets li :global(b) {
    color: var(--fg);
    font-weight: 600;
  }

  /* Confirm input */
  .confirm-label {
    font-size: 10px;
    color: var(--fg-dim);
    letter-spacing: 1px;
    text-transform: uppercase;
    font-family: var(--font-mono);
    margin-top: 4px;
  }
  .confirm-label :global(b) {
    color: var(--crit);
    font-weight: 700;
    font-size: 11px;
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
  }
  .confirm-input:focus { border-color: var(--accent); }
  .confirm-input.ok    { border-color: var(--ok, #00d97e); color: var(--ok, #00d97e); }
  .confirm-input:disabled { opacity: 0.5; cursor: not-allowed; }

  .err {
    padding: 10px 12px;
    background: rgba(255, 90, 90, 0.08);
    border-left: 3px solid var(--crit);
    font-size: 11px;
    color: var(--crit);
    font-family: var(--font-mono);
    letter-spacing: 0.3px;
  }
</style>
