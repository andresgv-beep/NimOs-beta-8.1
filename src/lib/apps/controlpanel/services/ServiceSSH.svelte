<script>
  /**
   * ServiceSSH · Panel de Control · página dedicada de SSH
   * ────────────────────────────────────────────────────────
   * El backend solo expone start/stop/status (sin config de puerto, etc.),
   * así que la página es simple: toggle + acceso + aviso de seguridad.
   *   GET  /api/ssh/status → { running, version }
   *   POST /api/ssh/start | stop
   */
  import { onMount, createEventDispatcher } from 'svelte';
  import { hdrs } from '$lib/stores/auth.js';

  const dispatch = createEventDispatcher();

  let status = { running: false, version: '', loading: true };
  let busy = false;
  let msg = '';
  let msgError = false;
  let lanHost = '';

  async function load() {
    try {
      const r = await fetch('/api/ssh/status', { headers: hdrs() });
      if (r.ok) { const d = await r.json(); status = { ...status, ...d, loading: false }; }
      else status.loading = false;
    } catch { status.loading = false; }
    lanHost = window.location.hostname || 'tu-nas';
  }

  async function toggleService() {
    if (busy) return;
    busy = true; msg = '';
    const action = status.running ? 'stop' : 'start';
    try {
      const r = await fetch(`/api/ssh/${action}`, { method: 'POST', headers: hdrs() });
      if (!r.ok) { const e = await r.json().catch(() => ({})); msg = e.error || 'Error'; msgError = true; }
    } catch { msg = 'Error de red'; msgError = true; }
    setTimeout(load, 600);
    busy = false;
  }

  onMount(load);
</script>

<div class="svc-page">
  <div class="sp-head">
    <button class="sp-back" on:click={() => dispatch('back')} title="Volver">‹</button>
    <span class="sp-led" class:on={status.running}></span>
    <div class="sp-title">
      SSH
      <span class="sp-sub">
        {#if status.loading}· comprobando…
        {:else}· {status.running ? 'activo' : 'detenido'}{status.version ? ` · ${status.version}` : ''}{/if}
      </span>
    </div>
    <div class="sp-spacer"></div>
    <button class="sp-toggle" class:on={status.running} disabled={busy} on:click={toggleService}>
      <span class="sp-toggle-thumb"></span>
    </button>
  </div>

  {#if msg}<div class="sp-msg" class:error={msgError}>{msg}</div>{/if}

  <div class="sp-lan">
    Acceso remoto: <b>ssh usuario@{lanHost}</b> · puerto 22
  </div>

  {#if status.running}
    <div class="sp-note">
      ⚠ El acceso remoto por terminal está activo. Cualquiera con credenciales válidas puede
      conectarse al sistema. Mantenlo apagado si no lo necesitas.
    </div>
  {/if}

  <div class="sp-info">
    SSH permite administrar el NAS desde una terminal remota. La configuración avanzada
    (puerto, claves, acceso root) se gestiona por ahora desde el archivo del sistema;
    aquí solo se controla el arranque del servicio.
  </div>
</div>

<style>
  .svc-page { display: flex; flex-direction: column; gap: 18px; max-width: 860px; }
  .sp-head { display: flex; align-items: center; gap: 12px; }
  .sp-back {
    width: 28px; height: 28px;
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 6px;
    background: var(--bg-inner, #101015);
    color: var(--fg-3, #9c9ca4);
    cursor: pointer; font-size: 16px; line-height: 1; flex-shrink: 0;
  }
  .sp-back:hover { color: var(--fg, #f0f0f0); border-color: var(--bd-3, #2a2a32); }
  .sp-led { width: 9px; height: 9px; border-radius: 2.5px; background: var(--fg-5, #5a5a62); }
  .sp-led.on { background: var(--st-ok, #00ff9f); }
  .sp-title { font-size: 14px; color: var(--fg, #f0f0f0); font-weight: 600; font-family: var(--font-mono); }
  .sp-sub { color: var(--fg-4, #7a7a82); font-size: 12px; font-weight: 400; }
  .sp-spacer { flex: 1; }
  .sp-toggle {
    width: 40px; height: 20px;
    background: var(--bg-inner, #101015);
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 5px; position: relative; cursor: pointer; padding: 0; flex-shrink: 0;
  }
  .sp-toggle-thumb {
    position: absolute; top: 2px; left: 2px;
    width: 14px; height: 14px;
    background: var(--fg-5, #5a5a62);
    border-radius: 3px; transition: left 0.15s, background 0.15s;
  }
  .sp-toggle.on { background: rgba(0,255,159,0.12); border-color: rgba(0,255,159,0.4); }
  .sp-toggle.on .sp-toggle-thumb { left: 22px; background: var(--nim-green, #00ff9f); }
  .sp-toggle:disabled { opacity: 0.5; cursor: not-allowed; }

  .sp-msg { font-size: 11px; color: var(--fg-3, #9c9ca4); font-family: var(--font-mono); }
  .sp-msg.error { color: var(--st-crit, #ff5a5a); }

  .sp-lan {
    background: var(--bg-inner, #101015);
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 8px; padding: 12px 14px;
    font-size: 11px; color: var(--fg-3, #9c9ca4); font-family: var(--font-mono);
  }
  .sp-lan b { color: var(--st-info, #4db8ff); }
  .sp-note {
    font-size: 10px; color: var(--st-warn, #ffc857);
    font-family: var(--font-mono); line-height: 1.5;
    background: rgba(255,200,87,0.06);
    border: 1px solid rgba(255,200,87,0.2);
    border-radius: 6px; padding: 10px 12px;
  }
  .sp-info {
    font-size: 11px; color: var(--fg-4, #7a7a82);
    font-family: var(--font-mono); line-height: 1.6;
  }
</style>
