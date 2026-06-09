<script>
  /**
   * CPServices · Panel de Control · sección Servicios
   * ───────────────────────────────────────────────────
   * Índice de servicios de red. Cada uno abre su página dedicada.
   *   · SMB (Samba)  → ServiceSMB     (/api/smb/*)
   *   · SSH          → ServiceSSH     (/api/ssh/*)
   *   · WebDAV       → ServiceWebDAV  (placeholder · sin backend aún)
   *
   * Respuestas del daemon: JSON plano (jsonOk no envuelve en {data}).
   */
  import { onMount, onDestroy } from 'svelte';
  import { hdrs } from '$lib/stores/auth.js';
  import { LED } from '$lib/ui';
  import ServiceSMB from './services/ServiceSMB.svelte';
  import ServiceSSH from './services/ServiceSSH.svelte';
  import ServiceWebDAV from './services/ServiceWebDAV.svelte';

  let view = 'index';          // 'index' | 'smb' | 'ssh' | 'webdav'
  let smb = { installed: true, running: false, version: '', loading: true };
  let ssh = { running: false, version: '', loading: true };
  let pollTimer = null;

  async function loadStatus() {
    try {
      const [rsmb, rssh] = await Promise.all([
        fetch('/api/smb/status', { headers: hdrs() }),
        fetch('/api/ssh/status', { headers: hdrs() }),
      ]);
      if (rsmb.ok) { const d = await rsmb.json(); smb = { ...smb, ...d, loading: false }; }
      else smb.loading = false;
      if (rssh.ok) { const d = await rssh.json(); ssh = { ...ssh, ...d, loading: false }; }
      else ssh.loading = false;
    } catch { smb.loading = false; ssh.loading = false; }
  }

  function open(v) { view = v; }
  function back() { view = 'index'; loadStatus(); }

  onMount(() => {
    loadStatus();
    pollTimer = setInterval(() => { if (view === 'index') loadStatus(); }, 8000);
  });
  onDestroy(() => clearInterval(pollTimer));
</script>

{#if view === 'smb'}
  <ServiceSMB on:back={back} />
{:else if view === 'ssh'}
  <ServiceSSH on:back={back} />
{:else if view === 'webdav'}
  <ServiceWebDAV on:back={back} />
{:else}
  <div class="cp-services">
    <button class="svc-card" on:click={() => open('smb')}>
      <div class="svc-id">
        <LED size={9} variant={smb.loading ? 'off' : smb.running ? 'ok' : 'off'} />
        <div class="svc-meta">
          <div class="svc-name">SMB · Samba</div>
          <div class="svc-sub">
            {#if smb.loading}comprobando…
            {:else if !smb.installed}no instalado
            {:else}{smb.running ? 'activo' : 'detenido'} · compartición de archivos en red{/if}
          </div>
        </div>
      </div>
      <span class="svc-chev">›</span>
    </button>

    <button class="svc-card" on:click={() => open('ssh')}>
      <div class="svc-id">
        <LED size={9} variant={ssh.loading ? 'off' : ssh.running ? 'ok' : 'off'} />
        <div class="svc-meta">
          <div class="svc-name">SSH</div>
          <div class="svc-sub">
            {#if ssh.loading}comprobando…
            {:else}{ssh.running ? 'activo' : 'detenido'} · acceso remoto por terminal{/if}
          </div>
        </div>
      </div>
      <span class="svc-chev">›</span>
    </button>

    <button class="svc-card disabled" on:click={() => open('webdav')}>
      <div class="svc-id">
        <LED size={9} variant="off" />
        <div class="svc-meta">
          <div class="svc-name">WebDAV</div>
          <div class="svc-sub">acceso a archivos por HTTP · próximamente</div>
        </div>
      </div>
      <span class="svc-chev">›</span>
    </button>
  </div>
{/if}

<style>
  .cp-services { display: flex; flex-direction: column; gap: 10px; max-width: 820px; }
  .svc-card {
    background: var(--bg-card, #15151a);
    border: 1px solid transparent;
    border-radius: 8px;
    padding: 14px 16px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    cursor: pointer;
    text-align: left;
    transition: border-color 0.12s, background 0.12s;
    font: inherit;
    color: inherit;
  }
  .svc-card:hover { border-color: var(--bd-3, #2a2a32); background: var(--bg-inner, #101015); }
  .svc-card.disabled { opacity: 0.55; }
  .svc-id { display: flex; align-items: center; gap: 12px; }
  .svc-meta { display: flex; flex-direction: column; gap: 2px; }
  .svc-name { font-size: 13px; color: var(--fg, #f0f0f0); font-family: var(--font-mono); font-weight: 600; }
  .svc-sub { font-size: 11px; color: var(--fg-4, #7a7a82); font-family: var(--font-mono); }
  .svc-chev { color: var(--fg-4, #7a7a82); font-size: 18px; }
</style>
