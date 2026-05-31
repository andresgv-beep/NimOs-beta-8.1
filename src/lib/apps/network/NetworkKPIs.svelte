<script>
  /**
   * NetworkKPIs · Banner de 4 KPIs de la sección Exposición.
   * ─────────────────────────────────────────────────────────
   * Expuestas · Caddy · Certificados · Dominio
   *
   * Deriva los valores de las props (apps + certs + config).
   *
   * Props:
   *   · apps   — array de apps expuestas
   *   · certs  — snapshot del observer { reachable, certs:[...] } | null
   *   · config — { base_domain, enabled }
   */
  import { KPICard } from '$lib/ui';

  export let apps = [];
  export let certs = null;
  export let config = { base_domain: '', enabled: false };

  $: exposedCount = apps.filter((a) => a.enabled).length;
  $: caddyReachable = certs ? certs.reachable : null;
  $: certCount = certs?.certs?.length || 0;
  $: certsExpiringSoon = (certs?.certs || []).filter(
    (c) => typeof c.days_left === 'number' && c.days_left < 15
  ).length;

  $: caddyState = caddyReachable === null ? 'desconocido' : caddyReachable ? 'online' : 'sin respuesta';
  $: caddyVariant = caddyReachable === null ? 'warn' : caddyReachable ? 'ok' : 'crit';

  $: domainState = !config.base_domain ? 'sin configurar' : config.enabled ? 'activo' : 'pausado';
  $: domainVariant = !config.base_domain ? 'warn' : config.enabled ? 'ok' : 'warn';
</script>

<div class="nx-kpis">
  <KPICard
    label="Expuestas"
    value={String(exposedCount)}
    state={apps.length > exposedCount ? `${apps.length - exposedCount} pausadas` : 'todas activas'}
    stateVariant={exposedCount > 0 ? 'ok' : 'warn'}
    valueVariant={exposedCount > 0 ? 'accent' : 'default'}
    bracketVariant={exposedCount > 0 ? 'accent' : 'warn'}
  />
  <KPICard
    label="Caddy"
    value={caddyReachable === null ? '—' : caddyReachable ? 'OK' : 'OFF'}
    state={caddyState}
    stateVariant={caddyVariant}
    valueVariant={caddyReachable ? 'accent' : caddyReachable === false ? 'crit' : 'default'}
    bracketVariant={caddyVariant === 'crit' ? 'crit' : 'accent'}
  />
  <KPICard
    label="Certificados"
    value={String(certCount)}
    state={certsExpiringSoon > 0 ? `${certsExpiringSoon} por expirar` : certCount > 0 ? 'todos válidos' : '—'}
    stateVariant={certsExpiringSoon > 0 ? 'warn' : 'ok'}
    valueVariant="default"
    bracketVariant={certsExpiringSoon > 0 ? 'warn' : 'accent'}
  />
  <KPICard
    label="Dominio"
    value={config.base_domain || '—'}
    state={domainState}
    stateVariant={domainVariant}
    valueVariant={config.base_domain ? 'default' : 'default'}
    bracketVariant={domainVariant === 'warn' ? 'warn' : 'accent'}
  />
</div>

<style>
  .nx-kpis {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 8px;
  }
  @media (min-width: 600px) {
    .nx-kpis { grid-template-columns: repeat(4, 1fr); }
  }
</style>
