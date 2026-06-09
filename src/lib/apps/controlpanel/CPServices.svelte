<script>
  /**
   * CPServices · Panel de Control · sección Servicios
   * ───────────────────────────────────────────────────
   * Da UI a los servicios de red que ya existían en el backend sin panel:
   *   · SMB (Samba)  → /api/smb/*   (status/start/stop/restart/set-password)
   *   · SSH          → /api/ssh/*   (status/start/stop)
   *   · WebDAV       → no existe en backend aún (placeholder "próximamente")
   *
   * Las respuestas del daemon son JSON plano (jsonOk no envuelve en {data}).
   *   GET  /api/smb/status → { installed, running, version, config, port }
   *   GET  /api/ssh/status → { running, version }
   */
  import { onMount, onDestroy } from 'svelte';
  import { hdrs } from '$lib/stores/auth.js';
  import { LED } from '$lib/ui';

  // Estado por servicio
  let smb = { installed: true, running: false, version: '', port: 445, loading: true };
  let ssh = { running: false, version: '', loading: true };

  let expanded = null;        // 'smb' | 'ssh' | 'webdav' | null
  let busy = {};              // { smb: true } mientras una acción está en curso
  let msg = '';
  let msgError = false;

  // SMB · cambio de contraseña
  let pwUser = '';
  let pwPass = '';
  let pwBusy = false;

  let pollTimer = null;

  async function loadStatus() {
    try {
      const [rsmb, rssh] = await Promise.all([
        fetch('/api/smb/status', { headers: hdrs() }),
        fetch('/api/ssh/status', { headers: hdrs() }),
      ]);
      if (rsmb.ok) {
        const d = await rsmb.json();
        smb = { ...smb, ...d, loading: false };
      } else smb.loading = false;
      if (rssh.ok) {
        const d = await rssh.json();
        ssh = { ...ssh, ...d, loading: false };
      } else ssh.loading = false;
    } catch {
      smb.loading = false;
      ssh.loading = false;
    }
  }

  async function toggle(service) {
    if (busy[service]) return;
    busy = { ...busy, [service]: true };
    msg = '';
    const running = service === 'smb' ? smb.running : ssh.running;
    const action = running ? 'stop' : 'start';
    try {
      const r = await fetch(`/api/${service}/${action}`, { method: 'POST', headers: hdrs() });
      if (!r.ok) {
        const e = await r.json().catch(() => ({}));
        msg = e.error || `Error al ${action === 'start' ? 'iniciar' : 'detener'} ${service.toUpperCase()}`;
        msgError = true;
      }
    } catch {
      msg = 'Error de red';
      msgError = true;
    }
    // Releer estado tras la acción (systemctl tarda un instante)
    setTimeout(loadStatus, 600);
    busy = { ...busy, [service]: false };
  }

  async function restartSmb() {
    if (busy.smb) return;
    busy = { ...busy, smb: true };
    msg = '';
    try {
      await fetch('/api/smb/restart', { method: 'POST', headers: hdrs() });
    } catch {}
    setTimeout(loadStatus, 600);
    busy = { ...busy, smb: false };
  }

  async function setSmbPassword() {
    if (!pwUser || !pwPass || pwBusy) return;
    pwBusy = true;
    msg = '';
    try {
      const r = await fetch('/api/smb/set-password', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: pwUser, password: pwPass }),
      });
      if (r.ok) {
        msg = `Contraseña SMB actualizada para ${pwUser}`;
        msgError = false;
        pwUser = '';
        pwPass = '';
      } else {
        const e = await r.json().catch(() => ({}));
        msg = e.error || 'Error al actualizar contraseña';
        msgError = true;
      }
    } catch {
      msg = 'Error de red';
      msgError = true;
    }
    pwBusy = false;
  }

  function toggleExpand(s) {
    expanded = expanded === s ? null : s;
  }

  onMount(() => {
    loadStatus();
    pollTimer = setInterval(loadStatus, 8000);
  });
  onDestroy(() => clearInterval(pollTimer));
</script>

<div class="cp-services">
  {#if msg}
    <div class="cps-msg" class:error={msgError}>{msg}</div>
  {/if}

  <!-- ═══ SMB / Samba ═══ -->
  <div class="svc-card" class:open={expanded === 'smb'}>
    <div class="svc-row">
      <div class="svc-id">
        <LED size={9} variant={smb.loading ? 'off' : smb.running ? 'ok' : 'off'} />
        <div class="svc-meta">
          <div class="svc-name">SMB · Samba</div>
          <div class="svc-sub">
            {#if smb.loading}comprobando…
            {:else if !smb.installed}no instalado
            {:else}{smb.running ? 'activo' : 'detenido'} · puerto {smb.port || 445}{smb.version ? ` · v${smb.version}` : ''}{/if}
          </div>
        </div>
      </div>
      <div class="svc-ctl">
        <button class="svc-toggle" class:on={smb.running} disabled={busy.smb || !smb.installed} on:click={() => toggle('smb')}>
          <span class="svc-toggle-thumb"></span>
        </button>
        <button class="svc-more" class:active={expanded === 'smb'} on:click={() => toggleExpand('smb')} title="Configuración">⋮</button>
      </div>
    </div>

    {#if expanded === 'smb'}
      <div class="svc-detail">
        <div class="svc-actions">
          <button class="svc-btn" disabled={busy.smb || !smb.running} on:click={restartSmb}>Reiniciar servicio</button>
        </div>
        <div class="svc-pw">
          <div class="svc-pw-title">Contraseña SMB de un usuario</div>
          <div class="svc-pw-row">
            <input class="svc-input" type="text" placeholder="usuario" bind:value={pwUser} autocomplete="off" />
            <input class="svc-input" type="password" placeholder="nueva contraseña" bind:value={pwPass} autocomplete="new-password" />
            <button class="svc-btn primary" disabled={pwBusy || !pwUser || !pwPass} on:click={setSmbPassword}>
              {pwBusy ? 'Guardando…' : 'Aplicar'}
            </button>
          </div>
          <div class="svc-hint">Samba mantiene contraseñas propias por usuario, separadas de la del sistema.</div>
        </div>
      </div>
    {/if}
  </div>

  <!-- ═══ SSH ═══ -->
  <div class="svc-card">
    <div class="svc-row">
      <div class="svc-id">
        <LED size={9} variant={ssh.loading ? 'off' : ssh.running ? 'ok' : 'off'} />
        <div class="svc-meta">
          <div class="svc-name">SSH</div>
          <div class="svc-sub">
            {#if ssh.loading}comprobando…
            {:else}{ssh.running ? 'activo' : 'detenido'} · puerto 22{ssh.version ? ` · ${ssh.version}` : ''}{/if}
          </div>
        </div>
      </div>
      <div class="svc-ctl">
        <button class="svc-toggle" class:on={ssh.running} disabled={busy.ssh} on:click={() => toggle('ssh')}>
          <span class="svc-toggle-thumb"></span>
        </button>
      </div>
    </div>
    {#if ssh.running}
      <div class="svc-warn">Acceso remoto por terminal activo. Mantenlo apagado si no lo necesitas.</div>
    {/if}
  </div>

  <!-- ═══ WebDAV (aún no en backend) ═══ -->
  <div class="svc-card disabled">
    <div class="svc-row">
      <div class="svc-id">
        <LED size={9} variant="off" />
        <div class="svc-meta">
          <div class="svc-name">WebDAV</div>
          <div class="svc-sub">no disponible todavía</div>
        </div>
      </div>
      <span class="svc-soon">próximamente</span>
    </div>
  </div>
</div>

<style>
  .cp-services { display: flex; flex-direction: column; gap: 10px; max-width: 820px; }

  .cps-msg { font-size: 11px; color: var(--fg-3, #9c9ca4); font-family: var(--font-mono); }
  .cps-msg.error { color: var(--st-crit, #ff5a5a); }

  .svc-card {
    background: var(--bg-card, #15151a);
    border-radius: 8px;
    overflow: hidden;
  }
  .svc-card.disabled { opacity: 0.55; }

  .svc-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 14px 16px;
  }
  .svc-id { display: flex; align-items: center; gap: 12px; }
  .svc-meta { display: flex; flex-direction: column; gap: 2px; }
  .svc-name {
    font-size: 13px;
    color: var(--fg, #f0f0f0);
    font-family: var(--font-mono);
    font-weight: 600;
  }
  .svc-sub {
    font-size: 11px;
    color: var(--fg-4, #7a7a82);
    font-family: var(--font-mono);
  }

  .svc-ctl { display: flex; align-items: center; gap: 10px; }

  /* Toggle cuadrado v3 (como el engine de NimShield) */
  .svc-toggle {
    width: 40px;
    height: 20px;
    background: var(--bg-inner, #101015);
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 5px;
    position: relative;
    cursor: pointer;
    padding: 0;
    transition: background 0.15s, border-color 0.15s;
  }
  .svc-toggle-thumb {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 14px;
    height: 14px;
    background: var(--fg-5, #5a5a62);
    border-radius: 3px;
    transition: left 0.15s, background 0.15s;
  }
  .svc-toggle.on {
    background: rgba(0, 255, 159, 0.12);
    border-color: rgba(0, 255, 159, 0.4);
  }
  .svc-toggle.on .svc-toggle-thumb {
    left: 22px;
    background: var(--nim-green, #00ff9f);
  }
  .svc-toggle:disabled { opacity: 0.5; cursor: not-allowed; }

  .svc-more {
    width: 26px;
    height: 26px;
    background: transparent;
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 4px;
    color: var(--fg-4, #7a7a82);
    cursor: pointer;
    font-size: 14px;
    line-height: 1;
  }
  .svc-more.active { color: var(--nim-green, #00ff9f); border-color: rgba(0, 255, 159, 0.35); }

  .svc-soon {
    font-size: 9px;
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.8px;
    color: var(--fg-5, #5a5a62);
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 3px;
    padding: 2px 7px;
  }

  .svc-warn {
    padding: 8px 16px 14px;
    font-size: 10px;
    color: var(--st-warn, #ffc857);
    font-family: var(--font-mono);
  }

  /* Detalle expandido */
  .svc-detail {
    border-top: 1px solid var(--bd-2, #20202a);
    padding: 14px 16px;
    display: flex;
    flex-direction: column;
    gap: 14px;
    background: rgba(0, 0, 0, 0.15);
  }
  .svc-actions { display: flex; gap: 8px; }
  .svc-pw { display: flex; flex-direction: column; gap: 8px; }
  .svc-pw-title {
    font-size: 11px;
    color: var(--fg-3, #9c9ca4);
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.5px;
  }
  .svc-pw-row { display: flex; gap: 8px; flex-wrap: wrap; }
  .svc-input {
    flex: 1;
    min-width: 130px;
    background: var(--bg-inner, #101015);
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 6px;
    padding: 8px 11px;
    color: var(--fg, #f0f0f0);
    font-size: 12px;
    font-family: var(--font-mono);
    outline: none;
  }
  .svc-input:focus { border-color: rgba(0, 255, 159, 0.35); }
  .svc-hint {
    font-size: 10px;
    color: var(--fg-5, #5a5a62);
    font-family: var(--font-mono);
    line-height: 1.5;
  }

  .svc-btn {
    padding: 8px 14px;
    background: var(--bg-inner, #101015);
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 6px;
    color: var(--fg-3, #9c9ca4);
    font-size: 11px;
    font-family: var(--font-mono);
    cursor: pointer;
    transition: all 0.12s;
  }
  .svc-btn:hover:not(:disabled) { color: var(--fg, #f0f0f0); border-color: var(--bd-3, #2a2a32); }
  .svc-btn.primary {
    background: var(--nim-green, #00ff9f);
    border-color: var(--nim-green, #00ff9f);
    color: var(--bg-window, #16161a);
    font-weight: 600;
  }
  .svc-btn.primary:hover:not(:disabled) { filter: brightness(1.08); }
  .svc-btn:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
