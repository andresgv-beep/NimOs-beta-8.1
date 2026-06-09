<script>
  /**
   * ControlPanel · Panel de Control · administración del sistema
   * ─────────────────────────────────────────────────────────────
   * App de administración del NAS. Agrupa lo que antes vivía disperso en
   * NimSettings más los servicios de red que no tenían UI.
   *
   * Secciones (se irán cableando por fases — ver CONTROL-PANEL-PLAN.md):
   *   · Usuarios        (Fase 1 · desde Settings)
   *   · Compartidas     (Fase 2 · desde Settings)
   *   · Servicios       (Fase 3 · nuevo · SMB/WebDAV/SSH)
   *   · Permisos apps   (Fase 4 · desde Settings)
   *   · Portal / 2FA    (Fase 5 · desde Settings)
   *   · Actualizaciones (Fase 6 · desde Settings)
   *
   * FASE 0 (actual): andamiaje. Shell + navegación, secciones vacías con
   * placeholder. No mueve lógica todavía; Settings sigue intacto.
   */
  import AppShell from '$lib/components/AppShell.svelte';
  import CPUsers from './controlpanel/CPUsers.svelte';
  import CPShares from './controlpanel/CPShares.svelte';

  let active = 'users';

  const sections = [
    {
      label: 'Sistema',
      items: [
        { id: 'users',       label: 'Usuarios' },
        { id: 'shares',      label: 'Compartidas' },
        { id: 'services',    label: 'Servicios' },
        { id: 'permissions', label: 'Permisos de apps' },
        { id: 'portal',      label: 'Portal · 2FA' },
        { id: 'updates',     label: 'Actualizaciones' },
      ],
    },
  ];

  const meta = {
    users:       { t: 'Usuarios',         s: '· cuentas y accesos' },
    shares:      { t: 'Compartidas',      s: '· carpetas compartidas' },
    services:    { t: 'Servicios',        s: '· SMB · WebDAV · SSH' },
    permissions: { t: 'Permisos de apps', s: '· qué puede usar cada usuario' },
    portal:      { t: 'Portal · 2FA',     s: '· seguridad de acceso' },
    updates:     { t: 'Actualizaciones',  s: '· versión del sistema' },
  };
</script>

<AppShell
  appId="controlpanel"
  title="Panel de Control"
  headerIcon="⚙"
  {sections}
  bind:active
>
  <svelte:fragment slot="page-header">
    <b>{meta[active]?.t || 'Panel de Control'}</b>
    <span class="cp-sub">{meta[active]?.s || ''}</span>
  </svelte:fragment>

  <div class="cp-body">
    {#if active === 'users'}
      <CPUsers />
    {:else if active === 'shares'}
      <CPShares />
    {:else}
      <div class="cp-placeholder">
        <div class="cp-ph-icon"></div>
        <div class="cp-ph-title">{meta[active]?.t}</div>
        <div class="cp-ph-hint">Sección en construcción · se cableará en su fase de migración.</div>
      </div>
    {/if}
  </div>
</AppShell>

<style>
  .cp-sub {
    color: var(--fg-4, #7a7a82);
    font-size: 12px;
    font-weight: 400;
  }
  .cp-body {
    min-height: 200px;
  }
  .cp-placeholder {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 10px;
    padding: 60px 24px;
    text-align: center;
  }
  .cp-ph-icon {
    width: 28px;
    height: 28px;
    border-radius: 7px;
    border: 1px solid var(--bd-3, #2a2a32);
    background: var(--bg-card, #15151a);
    margin-bottom: 6px;
  }
  .cp-ph-title {
    font-size: 14px;
    color: var(--fg-2, #d0d0d4);
    font-family: var(--font-mono);
  }
  .cp-ph-hint {
    font-size: 11px;
    color: var(--fg-5, #5a5a62);
    font-family: var(--font-mono);
    max-width: 320px;
    line-height: 1.5;
  }
</style>
