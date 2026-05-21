<script>
  /**
   * /network-v4 · Preview del módulo network v4 (Beta 8.1)
   * ─────────────────────────────────────────────────────
   * Ruta de preview para probar los componentes del módulo network v4
   * antes de integrarlos en la app principal.
   *
   * Hoy contiene:
   *   - PortsConfig: edición de puertos HTTP/HTTPS via /api/v4/network/ports
   *
   * Esta ruta NO está enlazada desde el desktop principal — se accede
   * directamente vía URL (/network-v4). Una vez F-008 cierre y todo el
   * módulo v4 esté estable, PortsConfig se integrará como sub-tab en
   * NetworkApp.svelte y esta ruta puede eliminarse.
   */
  import PortsConfig from '$lib/apps/network/PortsConfig.svelte';
</script>

<div class="container">
  <header>
    <h1>Network v4 · Preview</h1>
    <p>
      Vista de desarrollo. Requiere sesión admin activa.
      Endpoints consumidos: <code>/api/v4/network/ports</code>.
    </p>
  </header>

  <section>
    <PortsConfig />
  </section>
</div>

<style>
  .container {
    max-width: 1100px;
    margin: 0 auto;
    padding: 32px 24px;
    color: var(--text-primary);
    background: var(--panel);
    min-height: 100vh;
  }

  header {
    margin-bottom: 28px;
    border-bottom: 1px solid var(--border);
    padding-bottom: 16px;
  }

  h1 {
    font-family: var(--font-mono);
    font-size: 20px;
    font-weight: 600;
    letter-spacing: 1px;
    margin: 0 0 4px;
    color: var(--text-primary);
  }

  header p {
    margin: 0;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-dim);
  }

  header code {
    color: var(--accent);
    padding: 0 4px;
  }
</style>
