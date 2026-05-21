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
   * Comportamiento de sesión:
   *   - Al montar, llama a auth.init() — lee el token de localStorage
   *     y valida contra /api/auth/me.
   *   - Si no hay sesión válida → mensaje + link al desktop principal
   *     donde está el login.
   *   - Si la sesión existe pero el usuario no es admin → mensaje
   *     explicando que esta ruta requiere admin (PortsConfig usa
   *     endpoints admin-only).
   *   - Si todo OK → renderiza el componente.
   *
   * Esta ruta NO está enlazada desde el desktop principal — se accede
   * directamente vía URL. Una vez F-008 cierre y todo el módulo v4 esté
   * estable, PortsConfig se integrará como sub-tab en NetworkApp.svelte
   * y esta ruta puede eliminarse.
   */
  import { onMount } from 'svelte';
  import { user, appState } from '$lib/stores/auth.js';
  import * as auth from '$lib/stores/auth.js';
  import PortsConfig from '$lib/apps/network/PortsConfig.svelte';

  let ready = false;

  onMount(async () => {
    await auth.init();
    ready = true;
  });

  $: isLoggedIn = $appState === 'desktop' && $user != null;
  $: isAdmin = $user?.role === 'admin';
</script>

<div class="container">
  <header>
    <h1>Network v4 · Preview</h1>
    <p>
      Vista de desarrollo. Requiere sesión admin activa.
      Endpoints consumidos: <code>/api/v4/network/ports</code>.
    </p>
  </header>

  {#if !ready}
    <section class="state">
      <p>Verificando sesión…</p>
    </section>
  {:else if !isLoggedIn}
    <section class="state warn">
      <h2>No hay sesión activa</h2>
      <p>
        Esta ruta de preview necesita una sesión iniciada para acceder a
        <code>/api/v4/network/ports</code>.
      </p>
      <p>
        Inicia sesión en el <a href="/">desktop principal</a> y vuelve a esta URL.
      </p>
    </section>
  {:else if !isAdmin}
    <section class="state warn">
      <h2>Permisos insuficientes</h2>
      <p>
        La sesión activa es de un usuario no-admin ({$user?.username || 'desconocido'}).
        El endpoint <code>/api/v4/network/ports</code> requiere rol <strong>admin</strong>.
      </p>
    </section>
  {:else}
    <section>
      <p class="who">
        Sesión: <strong>{$user.username}</strong> · rol: <strong>{$user.role}</strong>
      </p>
      <PortsConfig />
    </section>
  {/if}
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

  header code,
  .state code {
    color: var(--accent);
    padding: 0 4px;
  }

  .state {
    padding: 20px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--panel-elev);
    font-family: var(--font-mono);
  }

  .state.warn {
    border-color: var(--crit);
    border-left-width: 3px;
  }

  .state h2 {
    font-size: 14px;
    margin: 0 0 12px;
    letter-spacing: 1px;
    color: var(--crit);
  }

  .state p {
    font-size: 12px;
    margin: 6px 0;
    color: var(--fg-dim);
  }

  .state a {
    color: var(--accent);
    text-decoration: underline;
  }

  .who {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-dim);
    margin: 0 0 16px;
  }

  .who strong {
    color: var(--text-primary);
    font-weight: 600;
  }
</style>
