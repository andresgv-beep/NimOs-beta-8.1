<script>
  /**
   * NetworkApp · Remote Access (v3 · según mockups oficiales)
   * ────────────────────────────────────────────────────────────
   * Sub-tabs horizontales: Ports · Router · DDNS · Proxy · Certs
   *
   * Flujo DDNS correcto según mockup:
   *   empty state → select provider (grid 4 cards) → form del proveedor → estado activo
   *
   * Puerto 5009 es HTTPS nativo del daemon Go (no 443 + proxy).
   *
   * Backend endpoints (idénticos a Beta 7, no tocar):
   *   GET    /api/ddns/status
   *   POST   /api/ddns/config
   *   POST   /api/ddns/test
   *   GET    /api/remote-access/status
   *   POST   /api/remote-access/request-ssl
   *   POST   /api/remote-access/enable-https
   *   GET    /api/router/status
   *   GET    /api/router/ports
   *   POST   /api/router/port
   *   DELETE /api/router/port
   *   POST   /api/router/test
   *   GET    /api/proxy/status
   */
  import { onMount, onDestroy } from 'svelte';
  import { token, hdrs } from '$lib/stores/auth.js';
  import AppShell from '$lib/components/AppShell.svelte';
  import {
    KPICard, SectionHead, BevelButton, IconButton, TextInput,
    Tab, Badge, LED, EmptyState, Spinner, DenseTable
  } from '$lib/ui';

  // ─── State ───
  let activeTab = 'ports'; // 'ports' | 'router' | 'ddns' | 'proxy' | 'certs'

  // HTTPS / Ports
  let httpsEnabled = false;
  let httpsSaving = false;
  const HTTPS_PORT = 5009;
  const HTTP_PORT = 5000;

  // Router / UPnP
  let routerStatus = {};
  let routerPorts = [];
  let routerMsg = '';
  let routerMsgError = false;
  let routerTesting = {};

  // Add port form
  let newPort = '';
  let newPortProto = 'TCP';
  let newPortDesc = '';

  // Presets (puertos comunes)
  const PRESETS = [
    { label: 'NimOS HTTP',  port: 5000,  proto: 'TCP', desc: 'NimOS HTTP' },
    { label: 'NimOS HTTPS', port: 5009,  proto: 'TCP', desc: 'NimOS HTTPS' },
    { label: 'Jellyfin',    port: 8096,  proto: 'TCP', desc: 'Jellyfin' },
    { label: 'Plex',        port: 32400, proto: 'TCP', desc: 'Plex Media Server' },
    { label: 'SSH',         port: 22,    proto: 'TCP', desc: 'SSH' },
  ];

  // DDNS
  let ddnsData = {};
  let ddnsPhase = 'loading'; // 'loading' | 'empty' | 'select-provider' | 'form' | 'active'
  let ddnsForm = { provider: '', domain: '', token: '', username: '', password: '' };
  let ddnsSaving = false;
  let ddnsTesting = false;
  let ddnsMsg = '';
  let ddnsMsgError = false;
  let tokenVisible = false;

  // Cert
  let certData = {};
  let certEmail = '';
  let certRequesting = false;
  let certMsg = '';
  let certMsgError = false;

  // Proxy
  let proxyData = { rules: [] };

  // Polling
  let pollInterval;
  let loading = true;

  // ─── Derived ───
  $: certDomain = ddnsData.config?.domain || certData.config?.ddns?.domain || '';
  $: externalIp = certData.ddns?.externalIp || ddnsData.externalIp || '';
  $: localIp = certData.localIp || routerStatus.internalIp || '';

  // Detección de cert con dos fuentes:
  //   1) /api/certs/status → lista certificados reales de certbot (fuente primaria)
  //   2) /api/remote-access/status → estado SSL del daemon (fallback)
  $: matchedCert = (certsFromCertbot || []).find(c =>
    c.domain === certDomain ||
    (c.domains && c.domains.includes(certDomain))
  ) || null;
  $: sslValid      = matchedCert?.valid || certData.ssl?.valid || false;
  $: sslExpiryDays = matchedCert?.expiryDays || certData.ssl?.expiryDays || 0;
  $: sslExpiryDate = matchedCert?.expiryDate || certData.ssl?.expiryDate || '';
  $: sslKeyType    = matchedCert?.keyType || '';

  $: port5009Open = (routerPorts || []).some(p =>
    parseInt(p.externalPort || p.port) === HTTPS_PORT
  );

  $: autoUpdate = ddnsData.config?.autoUpdate !== false;
  $: ddnsActive = ddnsData.config?.enabled && ddnsData.config?.domain;

  // Derivar fase de DDNS según estado
  $: if (ddnsData && Object.keys(ddnsData).length > 0) {
    if (ddnsActive) {
      ddnsPhase = 'active';
    } else if (ddnsPhase === 'loading') {
      ddnsPhase = 'empty';
    }
  }

  // Proveedores disponibles
  const PROVIDERS = [
    { id: 'duckdns', name: 'DuckDNS',  desc: 'Gratuito · Subdominio + Token', fields: 'dominio, token' },
    { id: 'noip',    name: 'No-IP',    desc: 'Gratuito/Premium · Email + Pass', fields: 'hostname, email, contraseña' },
    { id: 'dynu',    name: 'Dynu',     desc: 'Gratuito · Hostname + Password', fields: 'hostname, password' },
    { id: 'freedns', name: 'FreeDNS',  desc: 'Gratuito · Solo Update Key',     fields: 'update key' },
  ];

  // ─── API ───
  let certsFromCertbot = []; // Lista bruta de /api/certs/status (más fiable)

  async function loadAll() {
    try {
      const [ddns, certs, routerS, routerP, proxy, certsList] = await Promise.all([
        fetch('/api/ddns/status',           { headers: hdrs() }).then(r => r.json()).catch(() => ({})),
        fetch('/api/remote-access/status',  { headers: hdrs() }).then(r => r.json()).catch(() => ({})),
        fetch('/api/router/status',         { headers: hdrs() }).then(r => r.json()).catch(() => ({})),
        fetch('/api/router/ports',          { headers: hdrs() }).then(r => r.json()).catch(() => ({ ports: [] })),
        fetch('/api/proxy/status',          { headers: hdrs() }).then(r => r.json()).catch(() => ({ rules: [] })),
        fetch('/api/certs/status',          { headers: hdrs() }).then(r => r.json()).catch(() => ({ certificates: [] })),
      ]);
      ddnsData     = ddns || {};
      certData     = certs || {};
      routerStatus = routerS || {};
      routerPorts  = routerP?.ports || [];
      proxyData    = proxy || { rules: [] };
      httpsEnabled = certs.https?.running || certs.https?.enabled || false;
      certsFromCertbot = certsList?.certificates || [];
    } catch (e) {
      console.error('[NetworkApp] loadAll failed', e);
    }
    loading = false;
  }

  // ─── HTTPS toggle ───
  async function toggleHttps(enable) {
    httpsSaving = true;
    try {
      await fetch('/api/remote-access/enable-https', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ domain: certDomain, port: HTTPS_PORT, enabled: enable }),
      });
      await loadAll();
    } catch (e) { console.error('toggleHttps failed', e); }
    httpsSaving = false;
  }

  // ─── DDNS ───
  function selectProvider(pid) {
    ddnsForm = { provider: pid, domain: '', token: '', username: '', password: '' };
    ddnsMsg = '';
    ddnsMsgError = false;
    ddnsPhase = 'form';
  }

  function goToSelectProvider() {
    ddnsPhase = 'select-provider';
    ddnsMsg = '';
  }

  function cancelDdnsForm() {
    ddnsMsg = '';
    if (ddnsActive) {
      ddnsPhase = 'active';
    } else {
      ddnsPhase = 'empty';
    }
  }

  async function saveDdns() {
    const p = ddnsForm.provider;
    if (!p) { ddnsMsg = 'Selecciona un proveedor'; ddnsMsgError = true; return; }
    if (p === 'freedns' && !ddnsForm.token) {
      ddnsMsg = 'Introduce el update key'; ddnsMsgError = true; return;
    }
    if (p !== 'freedns' && !ddnsForm.domain) {
      ddnsMsg = 'Introduce el dominio'; ddnsMsgError = true; return;
    }
    if (p === 'noip' && (!ddnsForm.username || !ddnsForm.password)) {
      ddnsMsg = 'Introduce email y contraseña'; ddnsMsgError = true; return;
    }
    if ((p === 'duckdns' || p === 'dynu') && !ddnsForm.token) {
      ddnsMsg = 'Introduce el token'; ddnsMsgError = true; return;
    }
    ddnsSaving = true;
    ddnsMsg = '';
    try {
      const res = await fetch('/api/ddns/config', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...ddnsForm, enabled: true }),
      });
      const data = await res.json();
      if (data.ok) {
        ddnsMsg = 'Guardado correctamente';
        ddnsMsgError = false;
        await loadAll();
        ddnsPhase = 'active';
      } else {
        ddnsMsg = data.error || 'Error al guardar';
        ddnsMsgError = true;
      }
    } catch {
      ddnsMsg = 'Error de conexión';
      ddnsMsgError = true;
    }
    ddnsSaving = false;
  }

  async function testDdns() {
    ddnsTesting = true;
    ddnsMsg = '';
    try {
      const res = await fetch('/api/ddns/test', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify(ddnsForm),
      });
      const data = await res.json();
      if (data.ok) {
        ddnsMsg = 'Conexión exitosa' + (data.result ? ': ' + data.result : '');
        ddnsMsgError = false;
      } else {
        ddnsMsg = data.error || 'Falló la prueba';
        ddnsMsgError = true;
      }
    } catch {
      ddnsMsg = 'Error de conexión';
      ddnsMsgError = true;
    }
    ddnsTesting = false;
  }

  async function disableDdns() {
    if (!confirm('¿Desactivar DDNS? El dominio dejará de actualizarse.')) return;
    try {
      await fetch('/api/ddns/config', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: false }),
      });
      await loadAll();
      ddnsPhase = 'empty';
    } catch {
      ddnsMsg = 'Error al desactivar'; ddnsMsgError = true;
    }
  }

  async function toggleAutoUpdate() {
    const current = ddnsData.config?.autoUpdate !== false;
    try {
      await fetch('/api/ddns/config', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...ddnsData.config, autoUpdate: !current }),
      });
      await loadAll();
    } catch (e) { console.error('Toggle auto-update failed', e); }
  }

  function editDdns() {
    ddnsForm = {
      provider: ddnsData.config?.provider || '',
      domain:   ddnsData.config?.domain   || '',
      token:    ddnsData.config?.token    || '',
      username: ddnsData.config?.username || '',
      password: '',
    };
    ddnsPhase = 'form';
  }

  // ─── Router / UPnP ───
  async function addPort() {
    if (!newPort) return;
    routerMsg = '';
    try {
      const res = await fetch('/api/router/port', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify({
          port: parseInt(newPort),
          protocol: newPortProto,
          description: newPortDesc || 'NimOS',
        }),
      });
      const d = await res.json();
      if (d.ok) {
        routerMsg = `Puerto ${newPort}/${newPortProto} abierto`;
        routerMsgError = false;
        newPort = ''; newPortDesc = '';
        await loadAll();
      } else {
        routerMsg = d.error || 'Error'; routerMsgError = true;
      }
    } catch { routerMsg = 'Error de conexión'; routerMsgError = true; }
  }

  function applyPreset(preset) {
    newPort = String(preset.port);
    newPortProto = preset.proto;
    newPortDesc = preset.desc;
  }

  async function removePort(port, protocol) {
    if (!confirm(`¿Cerrar puerto ${port}/${protocol}?`)) return;
    try {
      const res = await fetch('/api/router/port', {
        method: 'DELETE',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ port: parseInt(port), protocol }),
      });
      const d = await res.json();
      if (d.ok) await loadAll();
      else { routerMsg = d.error || 'Error'; routerMsgError = true; }
    } catch { routerMsg = 'Error'; routerMsgError = true; }
  }

  async function testPort(port) {
    routerTesting = { ...routerTesting, [port]: 'testing' };
    try {
      const res = await fetch('/api/router/test', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ port: parseInt(port) }),
      });
      const d = await res.json();
      routerTesting = { ...routerTesting, [port]: d.reachable ? 'ok' : 'fail' };
    } catch {
      routerTesting = { ...routerTesting, [port]: 'fail' };
    }
    setTimeout(() => { routerTesting = { ...routerTesting, [port]: false }; }, 5000);
  }

  // ─── Cert ───
  // Emite un cert nuevo cuando no existe ninguno para este dominio
  async function requestCert() {
    const domain = certDomain;
    if (!domain) {
      certMsg = 'Configura un dominio DDNS primero'; certMsgError = true; return;
    }
    if (!certEmail) {
      certMsg = "Introduce un email para Let's Encrypt"; certMsgError = true; return;
    }
    certRequesting = true; certMsg = '';
    try {
      const provider = ddnsData.config?.provider || '';
      const dnsToken = ddnsData.config?.token || '';
      const useDns = provider === 'duckdns' && dnsToken;
      const res = await fetch('/api/remote-access/request-ssl', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify({
          domain, email: certEmail,
          method: useDns ? 'dns' : 'standalone',
          provider: useDns ? 'duckdns' : '',
          dnsToken: useDns ? dnsToken : '',
        }),
      });
      const data = await res.json();
      if (data.ok) {
        certMsg = 'Certificado obtenido'; certMsgError = false;
        await loadAll();
      } else {
        certMsg = data.error || 'Error al solicitar. ¿Quizás el cert ya existe? Prueba renovar.';
        certMsgError = true;
      }
    } catch { certMsg = 'Error de conexión'; certMsgError = true; }
    certRequesting = false;
  }

  // Renueva un cert existente con --force-renewal
  async function renewCert() {
    const domain = certDomain || matchedCert?.domain;
    if (!domain) {
      certMsg = 'No hay dominio detectado'; certMsgError = true; return;
    }
    certRequesting = true; certMsg = '';
    try {
      const res = await fetch('/api/certs/renew', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ domain }),
      });
      const data = await res.json();
      if (data.ok) {
        certMsg = 'Certificado renovado correctamente';
        certMsgError = false;
        await loadAll();
      } else {
        certMsg = data.error || 'Error al renovar';
        certMsgError = true;
      }
    } catch {
      certMsg = 'Error de conexión'; certMsgError = true;
    }
    certRequesting = false;
  }

  // ─── Helpers ───
  function fmtRelative(ts) {
    if (!ts) return '—';
    const diff = (Date.now() - new Date(ts).getTime()) / 1000;
    if (diff < 60) return 'hace ' + Math.floor(diff) + 's';
    if (diff < 3600) return 'hace ' + Math.floor(diff / 60) + 'm';
    if (diff < 86400) return 'hace ' + Math.floor(diff / 3600) + 'h';
    return 'hace ' + Math.floor(diff / 86400) + 'd';
  }

  function copyText(text) {
    navigator.clipboard?.writeText(text);
  }

  // ─── Lifecycle ───
  onMount(async () => {
    let attempts = 0;
    while (!$token && attempts < 10) { await new Promise(r => setTimeout(r, 200)); attempts++; }
    await loadAll();
    pollInterval = setInterval(loadAll, 15000);
  });

  onDestroy(() => { if (pollInterval) clearInterval(pollInterval); });
</script>

<AppShell
  appId="network"
  title="Network"
  headerIcon="⚡"
  pathSegments={['network', 'remote-access', activeTab]}
  sections={[
    {
      label: 'Red',
      items: [
        { id: '_iface',    label: 'Interfaces',    keyHint: 'I', disabled: true },
        { id: '_services', label: 'Services',      keyHint: 'S', disabled: true },
        { id: 'remote',    label: 'Remote Access', keyHint: 'R' },
        { id: '_security', label: 'Security',      keyHint: 'F', disabled: true },
      ],
    },
  ]}
  active="remote"
>

  <!-- Page header: título de sección + descripción (debajo del titlebar) -->
  <svelte:fragment slot="page-header">
    <b>Remote Access</b>
    <span class="ph-desc">· exposición y acceso remoto</span>
  </svelte:fragment>

  <!-- Sub-tabs horizontales -->
  <div class="na-subtabs">
    <Tab active={activeTab === 'ports'}  onClick={() => activeTab = 'ports'}>
      Ports
      {#if httpsEnabled}<Badge size="sm" variant="accent">on</Badge>{/if}
    </Tab>
    <Tab active={activeTab === 'router'} onClick={() => activeTab = 'router'}>
      Router
      {#if routerPorts.length > 0}<Badge size="sm">{routerPorts.length}</Badge>{/if}
    </Tab>
    <Tab active={activeTab === 'ddns'}   onClick={() => activeTab = 'ddns'}>
      DDNS
      {#if ddnsActive}<Badge size="sm" variant="accent">on</Badge>{/if}
    </Tab>
    <Tab active={activeTab === 'proxy'}  onClick={() => activeTab = 'proxy'}>
      Proxy
      {#if proxyData.rules?.length > 0}<Badge size="sm">{proxyData.rules.length}</Badge>{/if}
    </Tab>
    <Tab active={activeTab === 'certs'}  onClick={() => activeTab = 'certs'}>
      Certs
      {#if sslValid}<Badge size="sm" variant="accent">valid</Badge>{/if}
    </Tab>
  </div>

  {#if loading}
    <div class="na-loading">
      <Spinner label="Cargando configuración de red..." />
    </div>
  {:else}

  <div class="na-scroll">

    <!-- ═══ PORTS ═══ -->
    {#if activeTab === 'ports'}
      <div class="section">
        <SectionHead>HTTPS Server</SectionHead>

        <div class="status-bar cols-2">
          <div class="status-cell">
            <div class="sc-label">Estado</div>
            <div class="sc-value">
              <LED size={7} variant={httpsEnabled ? 'ok' : 'off'} />
              <span class:tc-accent={httpsEnabled}>{httpsEnabled ? 'Running' : 'Stopped'}</span>
            </div>
          </div>
          <div class="status-cell">
            <div class="sc-label">Puerto</div>
            <div class="sc-value mono">{HTTPS_PORT}</div>
          </div>
        </div>

        <div class="toggle-row">
          <div class="toggle" class:on={httpsEnabled} on:click={() => toggleHttps(!httpsEnabled)} role="button" tabindex="0"
               on:keydown={(e) => e.key === 'Enter' && toggleHttps(!httpsEnabled)}>
            <div class="toggle-track">
              <div class="toggle-thumb"></div>
            </div>
          </div>
          <span class="toggle-label">
            HTTPS {httpsEnabled ? 'activo' : 'inactivo'} en puerto <b class="mono">{HTTPS_PORT}</b>
            {#if httpsSaving}<span class="tc-mute"> · guardando...</span>{/if}
          </span>
        </div>

        {#if !sslValid && !httpsEnabled}
          <div class="msg warn">⚠ Necesitas un certificado SSL válido antes de activar HTTPS. Ve a la pestaña <b>Certs</b>.</div>
        {/if}
      </div>

      <div class="section">
        <SectionHead>Detalles de conexión</SectionHead>

        <div class="detail-rows">
          <div class="detail-row">
            <div class="dr-label">Local</div>
            <div class="dr-value">
              <code>http://{localIp || 'IP_LOCAL'}:{HTTP_PORT}</code>
              <IconButton size="sm" title="Copiar" onClick={() => copyText(`http://${localIp}:${HTTP_PORT}`)}>⎘</IconButton>
            </div>
          </div>
          {#if certDomain}
            <div class="detail-row">
              <div class="dr-label">Remote HTTP</div>
              <div class="dr-value">
                <code>http://{certDomain}:{HTTP_PORT}</code>
                <IconButton size="sm" title="Copiar" onClick={() => copyText(`http://${certDomain}:${HTTP_PORT}`)}>⎘</IconButton>
              </div>
            </div>
            <div class="detail-row">
              <div class="dr-label">Remote HTTPS</div>
              <div class="dr-value">
                <code class="tc-accent">https://{certDomain}:{HTTPS_PORT}</code>
                <IconButton size="sm" title="Copiar" onClick={() => copyText(`https://${certDomain}:${HTTPS_PORT}`)}>⎘</IconButton>
                {#if httpsEnabled}
                  <IconButton size="sm" title="Abrir" onClick={() => window.open(`https://${certDomain}:${HTTPS_PORT}`, '_blank')}>↗</IconButton>
                {/if}
              </div>
            </div>
          {:else}
            <div class="msg">Configura un dominio DDNS en la pestaña <b>DDNS</b> para obtener URL de acceso remoto.</div>
          {/if}
        </div>
      </div>
    {/if}

    <!-- ═══ ROUTER ═══ -->
    {#if activeTab === 'router'}
      <div class="section">
        <SectionHead>Router · UPnP</SectionHead>

        <div class="status-bar cols-3">
          <div class="status-cell">
            <div class="sc-label">Estado</div>
            <div class="sc-value">
              <LED size={7} variant={routerStatus.upnpAvailable ? 'ok' : 'warn'} />
              <span>{routerStatus.upnpAvailable ? 'Detectado' : 'No UPnP'}</span>
            </div>
          </div>
          <div class="status-cell">
            <div class="sc-label">IP Local</div>
            <div class="sc-value mono">{localIp || '—'}</div>
          </div>
          <div class="status-cell">
            <div class="sc-label">IP Externa</div>
            <div class="sc-value mono tc-accent">{externalIp || '—'}</div>
          </div>
        </div>

        {#if routerStatus.manufacturer || routerStatus.model}
          <div class="router-info">
            <span class="k">router</span>
            <span>{routerStatus.manufacturer || ''} {routerStatus.model || ''}</span>
          </div>
        {/if}
      </div>

      <div class="section">
        <SectionHead count="· {routerPorts.length}">Puertos abiertos</SectionHead>

        {#if routerPorts.length === 0}
          <EmptyState icon="◌" title="Sin puertos abiertos" hint="Añade un puerto usando los presets o el formulario" />
        {:else}
          <DenseTable
            columns="80px 60px 1fr 1fr 180px"
            headers={[
              { label: 'Puerto' },
              { label: 'Proto' },
              { label: 'Destino' },
              { label: 'Descripción' },
              { label: 'Acciones', align: 'right' },
            ]}
          >
            {#each routerPorts as p}
              <div class="tr-row">
                <div class="mono tc-accent">{p.externalPort || p.port}</div>
                <div><Badge size="sm" variant={p.protocol === 'TCP' ? 'info' : 'warn'}>{p.protocol || 'TCP'}</Badge></div>
                <div class="dim mono">{p.internalIp || localIp || '—'}:{p.internalPort || p.port}</div>
                <div class="dim">{p.description || '—'}</div>
                <div class="actions-cell">
                  {#if routerTesting[p.externalPort || p.port] === 'testing'}
                    <Badge size="sm" variant="warn">probando...</Badge>
                  {:else if routerTesting[p.externalPort || p.port] === 'ok'}
                    <Badge size="sm" variant="accent">accesible</Badge>
                  {:else if routerTesting[p.externalPort || p.port] === 'fail'}
                    <Badge size="sm" variant="crit">no accesible</Badge>
                  {/if}
                  <IconButton size="sm" title="Probar" onClick={() => testPort(p.externalPort || p.port)}>↻</IconButton>
                  <IconButton size="sm" variant="danger" title="Cerrar" onClick={() => removePort(p.externalPort || p.port, p.protocol || 'TCP')}>×</IconButton>
                </div>
              </div>
            {/each}
          </DenseTable>
        {/if}
      </div>

      <div class="section">
        <SectionHead>Presets rápidos</SectionHead>
        <div class="presets">
          {#each PRESETS as preset}
            <button class="preset-chip" on:click={() => applyPreset(preset)}>
              {preset.label} <span class="mono tc-mute">({preset.port})</span>
            </button>
          {/each}
        </div>

        <div class="port-form">
          <div class="pf-field">
            <label class="form-label">Puerto</label>
            <TextInput bind:value={newPort} placeholder="5009" size="sm" />
          </div>
          <div class="pf-field">
            <label class="form-label">Protocolo</label>
            <div class="input-wrap">
              <select bind:value={newPortProto}>
                <option value="TCP">TCP</option>
                <option value="UDP">UDP</option>
              </select>
              <span class="caret">▾</span>
            </div>
          </div>
          <div class="pf-field wide">
            <label class="form-label">Descripción</label>
            <TextInput bind:value={newPortDesc} placeholder="NimOS" size="sm" />
          </div>
          <div class="pf-field">
            <BevelButton variant="primary" size="sm" onClick={addPort} disabled={!newPort || !routerStatus.upnpAvailable}>
              ▸ Abrir
            </BevelButton>
          </div>
        </div>

        {#if !routerStatus.upnpAvailable}
          <div class="msg warn">⚠ UPnP no disponible. Abre los puertos manualmente desde la interfaz de tu router.</div>
        {/if}

        {#if routerMsg}
          <div class="msg" class:error={routerMsgError}>{routerMsg}</div>
        {/if}
      </div>
    {/if}

    <!-- ═══ DDNS ═══ -->
    {#if activeTab === 'ddns'}

      <!-- Fase: Active (estado actual) -->
      {#if ddnsPhase === 'active' && ddnsActive}
        <div class="section">
          <SectionHead count="· activo">Dynamic DNS</SectionHead>

          <div class="status-bar cols-3">
            <div class="status-cell">
              <div class="sc-label">Proveedor</div>
              <div class="sc-value">{ddnsData.config.provider}</div>
            </div>
            <div class="status-cell">
              <div class="sc-label">Dominio</div>
              <div class="sc-value mono tc-accent" style="font-size:12px">{ddnsData.config.domain}</div>
            </div>
            <div class="status-cell">
              <div class="sc-label">Estado</div>
              <div class="sc-value">
                <LED size={7} variant="ok" />
                <span>Activo</span>
              </div>
            </div>
          </div>

          <div class="toggle-row">
            <div class="toggle" class:on={autoUpdate} on:click={toggleAutoUpdate} role="button" tabindex="0"
                 on:keydown={(e) => e.key === 'Enter' && toggleAutoUpdate()}>
              <div class="toggle-track"><div class="toggle-thumb"></div></div>
            </div>
            <span class="toggle-label">Auto-actualización {autoUpdate ? 'activada' : 'desactivada'}</span>
          </div>

          <div class="detail-rows">
            <div class="detail-row">
              <div class="dr-label">IP externa</div>
              <div class="dr-value"><code>{externalIp || '—'}</code></div>
            </div>
            <div class="detail-row">
              <div class="dr-label">Última act.</div>
              <div class="dr-value tc-mute">{fmtRelative(ddnsData.lastUpdate)}{ddnsData.lastLog ? ' · ' + ddnsData.lastLog : ''}</div>
            </div>
          </div>

          <div class="actions-row">
            <BevelButton size="sm" onClick={editDdns}>✎ Editar</BevelButton>
            <div style="flex:1"></div>
            <BevelButton variant="danger" size="sm" onClick={disableDdns}>
              Desactivar DDNS
            </BevelButton>
          </div>
        </div>
      {/if}

      <!-- Fase: Empty state -->
      {#if ddnsPhase === 'empty'}
        <div class="empty-box">
          <div class="empty-icon">⇄</div>
          <div class="empty-title">Sin dominios DDNS configurados</div>
          <div class="empty-desc">Configura un dominio dinámico para acceder a NimOS desde fuera de tu red local.</div>
          <BevelButton variant="primary" size="sm" onClick={goToSelectProvider}>
            ▸ Añadir dominio
          </BevelButton>
        </div>
      {/if}

      <!-- Fase: Seleccionar proveedor -->
      {#if ddnsPhase === 'select-provider'}
        <div class="section">
          <SectionHead>Selecciona proveedor DDNS</SectionHead>

          <div class="provider-grid">
            {#each PROVIDERS as prov}
              <button
                class="provider-card"
                class:selected={ddnsForm.provider === prov.id}
                on:click={() => selectProvider(prov.id)}
              >
                <div class="pc-name">
                  <span class="pc-dot"></span>
                  {prov.name}
                </div>
                <div class="pc-desc">{prov.desc}</div>
                <div class="pc-fields">campos: {prov.fields}</div>
              </button>
            {/each}
          </div>

          <div class="actions-row" style="margin-top:14px">
            <BevelButton size="sm" onClick={() => ddnsPhase = 'empty'}>Cancelar</BevelButton>
          </div>
        </div>
      {/if}

      <!-- Fase: Formulario -->
      {#if ddnsPhase === 'form'}
        <div class="section">
          <SectionHead>Configurar {ddnsForm.provider}</SectionHead>

          {#if ddnsForm.provider === 'duckdns'}
            <div class="form-group">
              <label class="form-label">Subdominio</label>
              <TextInput bind:value={ddnsForm.domain} placeholder="nimosbarraca.duckdns.org" size="sm" />
              <div class="form-hint">Tu subdominio completo de DuckDNS</div>
            </div>
            <div class="form-group">
              <label class="form-label">Token</label>
              <div class="input-with-eye">
                <TextInput
                  bind:value={ddnsForm.token}
                  type={tokenVisible ? 'text' : 'password'}
                  placeholder="Token de DuckDNS"
                  size="sm"
                />
                <IconButton size="sm" title={tokenVisible ? 'Ocultar' : 'Mostrar'} onClick={() => tokenVisible = !tokenVisible}>
                  {tokenVisible ? '◉' : '○'}
                </IconButton>
              </div>
              <div class="form-hint">Token de tu cuenta DuckDNS · duckdns.org/domains</div>
            </div>

          {:else if ddnsForm.provider === 'noip'}
            <div class="form-group">
              <label class="form-label">Hostname</label>
              <TextInput bind:value={ddnsForm.domain} placeholder="midominio.ddns.net" size="sm" />
              <div class="form-hint">Tu hostname registrado en No-IP</div>
            </div>
            <div class="form-group">
              <label class="form-label">Email</label>
              <TextInput bind:value={ddnsForm.username} placeholder="tu@email.com" size="sm" />
            </div>
            <div class="form-group">
              <label class="form-label">Contraseña</label>
              <TextInput bind:value={ddnsForm.password} type="password" size="sm" />
            </div>

          {:else if ddnsForm.provider === 'dynu'}
            <div class="form-group">
              <label class="form-label">Hostname</label>
              <TextInput bind:value={ddnsForm.domain} placeholder="midominio.dynu.net" size="sm" />
            </div>
            <div class="form-group">
              <label class="form-label">Password</label>
              <TextInput bind:value={ddnsForm.token} type="password" size="sm" />
            </div>

          {:else if ddnsForm.provider === 'freedns'}
            <div class="form-group">
              <label class="form-label">Update Key</label>
              <TextInput bind:value={ddnsForm.token} placeholder="Update key de FreeDNS" size="sm" />
              <div class="form-hint">Obtén tu update key desde freedns.afraid.org</div>
            </div>
          {/if}

          <div class="actions-row">
            <BevelButton size="sm" onClick={testDdns} disabled={ddnsTesting}>
              {ddnsTesting ? 'Probando...' : 'Probar conexión'}
            </BevelButton>
            <BevelButton variant="primary" size="sm" onClick={saveDdns} disabled={ddnsSaving}>
              {ddnsSaving ? '▸ Guardando...' : '▸ Guardar'}
            </BevelButton>
            <BevelButton size="sm" onClick={cancelDdnsForm}>Cancelar</BevelButton>
          </div>

          {#if ddnsMsg}
            <div class="msg" class:ok={!ddnsMsgError} class:error={ddnsMsgError}>{ddnsMsg}</div>
          {/if}
        </div>
      {/if}

    {/if}

    <!-- ═══ PROXY ═══ -->
    {#if activeTab === 'proxy'}
      <div class="section">
        <SectionHead count={proxyData.rules?.length ? `· ${proxyData.rules.length}` : ''}>
          Reverse Proxy
        </SectionHead>

        {#if !proxyData.rules || proxyData.rules.length === 0}
          <EmptyState
            icon="⇄"
            title="Sin reglas de proxy"
            hint="Crea reglas para servir subdominios hacia puertos internos (ej. jellyfin.tu-dominio.duckdns.org → :8096)"
          />
        {:else}
          <div class="proxy-list">
            {#each proxyData.rules as rule}
              <div class="proxy-row">
                <span class="proxy-from mono">{rule.from || rule.subdomain}</span>
                <span class="proxy-arrow">→</span>
                <span class="proxy-to mono">{rule.to || rule.target}</span>
                <IconButton size="sm" variant="danger" title="Eliminar">×</IconButton>
              </div>
            {/each}
          </div>
        {/if}

        <div class="actions-row" style="margin-top:14px">
          <BevelButton size="sm" disabled>+ Añadir regla (próximamente)</BevelButton>
        </div>
      </div>
    {/if}

    <!-- ═══ CERTS ═══ -->
    {#if activeTab === 'certs'}
      <div class="section">
        <SectionHead>SSL Certificate</SectionHead>

        {#if sslValid && certDomain}
          <!-- Cert válido: mostrar info -->
          <div class="cert-card">
            <div class="cert-header">
              <LED size={8} variant="ok" />
              <span class="cert-status">Válido</span>
              <span class="cert-days">{sslExpiryDays || '?'} días restantes</span>
            </div>
            <div class="cert-grid">
              <div class="cert-cell">
                <div class="cc-label">Dominio</div>
                <div class="cc-value">{certDomain}</div>
              </div>
              <div class="cert-cell">
                <div class="cc-label">Expira</div>
                <div class="cc-value">{sslExpiryDate || '—'}</div>
              </div>
              <div class="cert-cell">
                <div class="cc-label">Emisor</div>
                <div class="cc-value">Let's Encrypt</div>
              </div>
              <div class="cert-cell">
                <div class="cc-label">Renovación</div>
                <div class="cc-value tc-accent">Automática</div>
              </div>
            </div>
          </div>

          <div class="actions-row">
            <BevelButton size="sm" onClick={renewCert} disabled={certRequesting}>
              {certRequesting ? 'Renovando...' : '↻ Renovar ahora'}
            </BevelButton>
          </div>

        {:else}
          <!-- No cert: solicitar -->
          <div class="cert-missing">
            <div class="cm-title">Sin certificado SSL</div>
            <div class="cm-desc">
              {#if !certDomain}
                Configura un dominio DDNS antes de solicitar un certificado.
              {:else}
                Emite un certificado Let's Encrypt gratuito para <b class="tc-accent">{certDomain}</b>
              {/if}
            </div>
          </div>

          {#if certDomain}
            <div class="form-group">
              <label class="form-label">Email de contacto</label>
              <TextInput bind:value={certEmail} placeholder="tu@email.com" size="sm" />
              <div class="form-hint">Let's Encrypt usará este email para avisos de expiración</div>
            </div>

            <div class="actions-row">
              <BevelButton
                variant="primary"
                size="sm"
                onClick={requestCert}
                disabled={certRequesting || !certEmail}
              >
                {certRequesting ? '▸ Emitiendo...' : '▸ Emitir certificado'}
              </BevelButton>
            </div>
          {:else}
            <div class="actions-row">
              <BevelButton size="sm" onClick={() => activeTab = 'ddns'}>
                ▸ Ir a DDNS
              </BevelButton>
            </div>
          {/if}
        {/if}

        {#if certMsg}
          <div class="msg" class:ok={!certMsgError} class:error={certMsgError}>{certMsg}</div>
        {/if}
      </div>
    {/if}

  </div>
  {/if}

  <!-- Footer -->
  <svelte:fragment slot="footer">
    <span><span class="k">iface</span> <span class="v">eth0</span></span>
    <span class="sep">·</span>
    <span><span class="k">ip</span> <span class="v">{localIp || '—'}</span></span>
    <span class="sep">·</span>
    <span><span class="k">ssl</span> <span class="v" class:tc-accent={sslValid}>{sslValid ? 'valid' : 'none'}</span></span>
    <span class="sep">·</span>
    <span><span class="k">ddns</span> <span class="v" class:tc-accent={ddnsActive}>{ddnsActive ? 'active' : 'off'}</span></span>
  </svelte:fragment>

  <svelte:fragment slot="footer-right">
    <span><span class="k">https</span> <span class="v" class:tc-accent={httpsEnabled}>:{HTTPS_PORT}</span></span>
  </svelte:fragment>

</AppShell>

<style>
  /* Sub-tabs */
  .na-subtabs {
    display: flex;
    padding: 0 16px;
    background: var(--bg-1);
    border-bottom: 1px solid var(--border);
    gap: 4px;
    flex-shrink: 0;
  }

  .na-loading {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 300px;
  }

  .na-scroll {
    flex: 1;
    overflow-y: auto;
    padding: 22px 28px 24px;
    display: flex;
    flex-direction: column;
    gap: 26px;
  }

  .section {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  /* Status bar (KPI-like pero inline) */
  .status-bar {
    display: grid;
    gap: 1px;
    background: var(--border);
    border: 1px solid var(--border);
  }
  .status-bar.cols-2 { grid-template-columns: 1fr 1fr; }
  .status-bar.cols-3 { grid-template-columns: repeat(3, 1fr); }

  .status-cell {
    background: var(--bg-1);
    padding: 14px 18px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-family: var(--font-mono);
  }
  .sc-label {
    font-size: 9px;
    color: var(--fg-mute);
    text-transform: uppercase;
    letter-spacing: 1.5px;
  }
  .sc-value {
    font-size: 13px;
    color: var(--fg);
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .sc-value.mono { font-family: var(--font-mono); }

  /* Toggle */
  .toggle-row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 14px;
    background: var(--bg-1);
    border: 1px solid var(--border);
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .toggle {
    cursor: pointer;
    display: inline-block;
  }
  .toggle-track {
    width: 36px;
    height: 18px;
    background: var(--bg);
    border: 1px solid var(--border);
    position: relative;
    transition: all 0.15s;
  }
  .toggle-thumb {
    position: absolute;
    top: 1px; left: 1px;
    width: 14px; height: 14px;
    background: var(--fg-mute);
    transition: all 0.15s;
  }
  .toggle.on .toggle-track {
    border-color: var(--accent);
    background: var(--accent-dim);
  }
  .toggle.on .toggle-thumb {
    left: 19px;
    background: var(--accent);
    box-shadow: 0 0 6px rgba(0, 255, 159, 0.35);
  }
  .toggle-label {
    color: var(--fg-dim);
    letter-spacing: 0.3px;
  }
  .toggle-label b {
    color: var(--fg);
    font-weight: 600;
  }

  /* Detail rows */
  .detail-rows {
    display: flex;
    flex-direction: column;
    gap: 2px;
    background: var(--bg-1);
    border: 1px solid var(--border);
    padding: 4px;
    font-family: var(--font-mono);
  }
  .detail-row {
    display: grid;
    grid-template-columns: 140px 1fr;
    gap: 14px;
    padding: 8px 12px;
    align-items: center;
    font-size: 11px;
  }
  .dr-label {
    color: var(--fg-mute);
    text-transform: uppercase;
    letter-spacing: 1.2px;
    font-size: 9px;
  }
  .dr-value {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--fg);
  }
  .dr-value code {
    font-family: var(--font-mono);
    padding: 2px 6px;
    background: var(--bg);
    border: 1px solid var(--border);
    color: var(--fg-dim);
    font-size: 11px;
  }
  .dr-value code.tc-accent { color: var(--accent); border-color: var(--accent); }

  /* Router */
  .router-info {
    padding: 10px 14px;
    background: var(--bg-1);
    border: 1px solid var(--border);
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-dim);
    display: flex;
    gap: 10px;
  }

  /* Table rows */
  .tr-row {
    display: contents;
  }
  .tr-row > * {
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .actions-cell {
    display: flex;
    gap: 4px;
    align-items: center;
    justify-content: flex-end;
  }
  .dim { color: var(--fg-dim); }
  .mono { font-family: var(--font-mono); }

  /* Presets */
  .presets {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    padding: 10px 0;
  }
  .preset-chip {
    font-family: var(--font-mono);
    font-size: 10px;
    padding: 5px 10px;
    background: var(--bg);
    border: 1px solid var(--border-bright);
    color: var(--fg-dim);
    cursor: pointer;
    letter-spacing: 0.5px;
    transition: all 0.12s;
    clip-path: polygon(
      0 0, calc(100% - 5px) 0, 100% 5px,
      100% 100%, 5px 100%, 0 calc(100% - 5px)
    );
  }
  .preset-chip:hover {
    border-color: var(--accent);
    color: var(--accent);
  }

  /* Port form */
  .port-form {
    display: grid;
    grid-template-columns: 100px 110px 1fr auto;
    gap: 10px;
    align-items: end;
    padding: 12px;
    background: var(--bg);
    border: 1px dashed var(--border);
  }
  .pf-field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .pf-field.wide { grid-column: span 1; }

  .form-label {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-mute);
    text-transform: uppercase;
    letter-spacing: 1.3px;
    display: block;
  }

  /* Provider grid */
  .provider-grid {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 10px;
  }
  .provider-card {
    background: var(--bg-1);
    border: 1px solid var(--border);
    padding: 14px 16px;
    cursor: pointer;
    transition: all 0.12s;
    font-family: var(--font-mono);
    text-align: left;
    color: var(--fg-dim);
  }
  .provider-card:hover {
    border-color: var(--border-bright);
    background: var(--bg-2);
  }
  .provider-card.selected {
    border-color: var(--accent);
    background: var(--accent-dim);
    color: var(--fg);
    box-shadow: 0 0 10px rgba(0, 255, 159, 0.15);
  }
  .pc-name {
    font-size: 13px;
    color: var(--fg);
    font-weight: 600;
    margin-bottom: 6px;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .pc-dot {
    width: 8px;
    height: 8px;
    background: var(--fg-mute);
    display: inline-block;
  }
  .provider-card.selected .pc-dot {
    background: var(--accent);
    box-shadow: 0 0 5px var(--accent);
  }
  .pc-desc {
    font-size: 10px;
    color: var(--fg-dim);
    letter-spacing: 0.3px;
    margin-bottom: 4px;
  }
  .pc-fields {
    font-size: 9px;
    color: var(--fg-mute);
    letter-spacing: 0.3px;
  }

  /* Empty state box */
  .empty-box {
    text-align: center;
    padding: 48px 24px;
    background: var(--bg-1);
    border: 1px dashed var(--border-bright);
    display: flex;
    flex-direction: column;
    gap: 12px;
    align-items: center;
  }
  .empty-icon {
    font-size: 36px;
    color: var(--fg-mute);
    font-family: var(--font-mono);
    margin-bottom: 8px;
  }
  .empty-title {
    font-size: 14px;
    color: var(--fg);
    font-weight: 600;
    letter-spacing: 0.3px;
  }
  .empty-desc {
    font-size: 11px;
    color: var(--fg-mute);
    letter-spacing: 0.3px;
    max-width: 400px;
    line-height: 1.5;
    margin-bottom: 8px;
  }

  /* Form groups */
  .form-group {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-bottom: 14px;
  }
  .form-hint {
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--fg-faint);
    letter-spacing: 0.3px;
  }

  .input-with-eye {
    display: flex;
    gap: 6px;
    align-items: stretch;
  }
  .input-with-eye :global(.nimos-text-input) { flex: 1; }

  .input-wrap {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 28px;
    padding: 0 10px;
    border: 1px solid var(--border);
    background: var(--bg);
    clip-path: polygon(
      0 0, calc(100% - 6px) 0, 100% 6px,
      100% 100%, 6px 100%, 0 calc(100% - 6px)
    );
  }
  .input-wrap:focus-within { border-color: var(--accent); }
  .input-wrap select {
    flex: 1;
    background: transparent;
    border: none;
    outline: none;
    color: var(--fg);
    font-family: var(--font-mono);
    font-size: 11px;
    appearance: none;
    cursor: pointer;
  }
  .input-wrap .caret { color: var(--fg-mute); }

  /* Actions row */
  .actions-row {
    display: flex;
    gap: 8px;
    align-items: center;
    padding-top: 12px;
    border-top: 1px solid var(--border);
  }

  /* Proxy */
  .proxy-list {
    display: flex;
    flex-direction: column;
    gap: 1px;
    background: var(--border);
    border: 1px solid var(--border);
  }
  .proxy-row {
    display: grid;
    grid-template-columns: 1fr auto 1fr auto;
    gap: 14px;
    padding: 10px 14px;
    align-items: center;
    background: var(--bg-1);
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .proxy-from { color: var(--accent); }
  .proxy-arrow { color: var(--fg-mute); }
  .proxy-to { color: var(--fg-dim); }

  /* Certs */
  .cert-card {
    background: var(--bg-1);
    border: 1px solid var(--accent);
    clip-path: polygon(
      0 0, 100% 0, 100% calc(100% - 10px),
      calc(100% - 10px) 100%, 0 100%
    );
    box-shadow: 0 0 15px rgba(0, 255, 159, 0.08);
  }
  .cert-header {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px 18px;
    border-bottom: 1px solid var(--border);
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .cert-status {
    color: var(--accent);
    font-weight: 600;
    letter-spacing: 0.5px;
  }
  .cert-days {
    color: var(--fg-dim);
    margin-left: auto;
    font-size: 10px;
  }
  .cert-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
  }
  .cert-cell {
    padding: 12px 18px;
    border-right: 1px solid var(--border);
    border-bottom: 1px solid var(--border);
    font-family: var(--font-mono);
  }
  .cert-cell:nth-child(2n) { border-right: none; }
  .cert-cell:nth-child(n+3) { border-bottom: none; }
  .cc-label {
    font-size: 9px;
    color: var(--fg-mute);
    text-transform: uppercase;
    letter-spacing: 1.5px;
    margin-bottom: 4px;
  }
  .cc-value {
    font-size: 11px;
    color: var(--fg);
    font-weight: 500;
    word-break: break-all;
  }

  .cert-missing {
    padding: 24px;
    background: var(--bg-1);
    border: 1px solid var(--border);
    text-align: center;
  }
  .cm-title {
    font-family: var(--font-mono);
    font-size: 13px;
    color: var(--fg-dim);
    margin-bottom: 6px;
    font-weight: 600;
  }
  .cm-desc {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-mute);
    letter-spacing: 0.3px;
  }

  /* Mensajes */
  .msg {
    font-family: var(--font-mono);
    font-size: 10px;
    padding: 8px 12px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-left: 2px solid var(--accent);
    color: var(--fg-dim);
    letter-spacing: 0.3px;
  }
  .msg.ok {
    border-left-color: var(--accent);
    background: var(--accent-dim);
    color: var(--accent);
  }
  .msg.error {
    border-left-color: var(--crit);
    background: rgba(255, 90, 90, 0.06);
    color: var(--crit);
  }
  .msg.warn {
    border-left-color: var(--warn);
    background: rgba(255, 184, 0, 0.06);
    color: var(--warn);
  }

  /* Utility */
  .tc-accent { color: var(--accent); }
  .tc-mute   { color: var(--fg-mute); }
  .k { color: var(--fg-faint); }
  .v { color: var(--fg-dim); font-feature-settings: "tnum"; }
  .sep { color: var(--fg-faint); }
</style>
