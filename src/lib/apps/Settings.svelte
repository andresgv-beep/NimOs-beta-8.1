<script>
  /**
   * NimSettings · Panel de Control de NimOS Beta 8.1
   * ──────────────────────────────────────────────────
   * Portado desde Beta 5/7 con la estética NimOS Beta 8.1:
   *  · Bisel firma · Cubo 45° · Path nimos:// · LEDs C2
   *  · Theme cards con preview REAL del sistema
   *  · Accent picker con 6 predefinidos + hex custom
   *  · Wallpapers sistema + uploads del user
   *  · 8 secciones: Monitor / Users / Shares / Permissions /
   *    Portal (2FA) / Updates / Appearance / About
   *
   * Endpoints (mismos que Beta 5):
   *  GET/PUT /api/user/preferences
   *  GET     /api/users
   *  POST    /api/users
   *  DELETE  /api/users/:username
   *  GET     /api/shares
   *  ...etc
   */
  import { onMount, onDestroy } from 'svelte';
  import { prefs, setPref, ACCENT_COLORS } from '$lib/stores/theme.js';
  import { user, getToken, hdrs } from '$lib/stores/auth.js';
  import AppShell from '$lib/components/AppShell.svelte';

  // ───── Navegación ─────
  let activeView = 'appearance';
  let appearanceTab = 'tema'; // tema | taskbar | escala

  // Sidebar sections (agrupadas como espera AppShell)
  const sections = [
    {
      label: 'Sistema',
      items: [
        { id: 'monitor',     label: 'Monitor',              keyHint: 'M' },
        { id: 'users',       label: 'Usuarios',             keyHint: 'U' },
        { id: 'shares',      label: 'Compartidas',          keyHint: 'C' },
        { id: 'permissions', label: 'Permisos de apps',     keyHint: 'P' },
        { id: 'portal',      label: 'Portal',               keyHint: 'O' },
        { id: 'updates',     label: 'Actualizaciones',      keyHint: 'A' },
      ],
    },
    {
      label: 'Preferencias',
      items: [
        { id: 'appearance',  label: 'Apariencia',           keyHint: 'T' },
        { id: 'about',       label: 'Acerca de',            keyHint: 'I' },
      ],
    },
  ];

  // ───── Theme state ─────
  $: currentTheme = $prefs.theme || 'dark';
  $: currentAccent = $prefs.accentColor || 'green';
  $: customAccent = $prefs.customAccentColor || '';
  $: currentWallpaper = $prefs.wallpaper || '';

  let customHexInput = '';
  $: customHexInput = customAccent;

  function selectTheme(t) {
    setPref('theme', t);
  }

  function selectAccent(name) {
    setPref('accentColor', name);
  }

  function applyCustomHex() {
    const v = (customHexInput || '').trim();
    if (!/^#?[0-9a-fA-F]{6}$/.test(v.replace('#',''))) {
      customHexErr = 'Formato hex inválido. Ej: #00ff9f';
      return;
    }
    const hex = v.startsWith('#') ? v : '#' + v;
    setPref('customAccentColor', hex);
    setPref('accentColor', 'custom');
    customHexErr = '';
  }
  let customHexErr = '';

  function selectWallpaper(wp) {
    setPref('wallpaper', wp);
  }

  // ───── Wallpaper system + user ─────
  // Sistema: definidos en /usr/share/nimos/wallpapers/ (servidos por el daemon)
  // User: subidos por el user, en /var/lib/nimos/wallpapers/<user>/
  let systemWallpapers = [];
  let userWallpapers = [];
  let wallpapersLoading = false;

  async function loadWallpapers() {
    wallpapersLoading = true;
    try {
      const r = await fetch('/api/wallpapers', { headers: hdrs() });
      if (r.ok) {
        const d = await r.json();
        systemWallpapers = d.system || [];
        userWallpapers   = d.user || [];
      }
    } catch { /* silent */ }
    wallpapersLoading = false;
  }

  let wallUploadInput;
  async function uploadWallpaper(e) {
    const f = e.target.files?.[0];
    if (!f) return;
    if (!f.type.startsWith('image/')) {
      wallUploadMsg = 'El archivo debe ser una imagen';
      wallUploadErr = true;
      return;
    }
    if (f.size > 10 * 1024 * 1024) {
      wallUploadMsg = 'Máximo 10MB';
      wallUploadErr = true;
      return;
    }
    wallUploadMsg = 'Subiendo...';
    wallUploadErr = false;

    // Convertir imagen a base64 (formato esperado por el daemon)
    const reader = new FileReader();
    reader.onload = async () => {
      const base64 = reader.result; // data:image/jpeg;base64,/9j...
      try {
        const r = await fetch('/api/user/wallpaper', {
          method: 'POST',
          headers: { ...hdrs(), 'Content-Type': 'application/json' },
          body: JSON.stringify({
            filename: f.name,
            data: base64,
          }),
        });
        if (r.ok) {
          const d = await r.json().catch(() => ({}));
          wallUploadMsg = '✓ Subido correctamente';
          wallUploadErr = false;
          // Auto-seleccionar el wallpaper recién subido si el daemon devuelve url
          if (d.url) {
            setPref('wallpaper', d.url);
          }
          await loadWallpapers();
          // Limpiar mensaje tras 3 segundos
          setTimeout(() => { wallUploadMsg = ''; }, 3000);
        } else {
          const err = await r.json().catch(() => ({}));
          wallUploadMsg = err.error || 'Error al subir';
          wallUploadErr = true;
        }
      } catch (e) {
        wallUploadMsg = 'Error de red: ' + e.message;
        wallUploadErr = true;
      }
      if (wallUploadInput) wallUploadInput.value = '';
    };
    reader.onerror = () => {
      wallUploadMsg = 'Error al leer el archivo';
      wallUploadErr = true;
    };
    reader.readAsDataURL(f);
  }
  let wallUploadMsg = '';
  let wallUploadErr = false;

  async function deleteUserWallpaper(wp) {
    if (!confirm('¿Eliminar este wallpaper?')) return;
    try {
      const r = await fetch('/api/wallpapers/' + encodeURIComponent(wp), {
        method: 'DELETE',
        headers: hdrs(),
      });
      if (r.ok) {
        if (currentWallpaper === wp) setPref('wallpaper', '');
        await loadWallpapers();
      }
    } catch {}
  }

  // ───── Users ─────
  let usersList = [];
  let editingUser = null;
  let userMsg = '';
  let userMsgError = false;
  let savingUser = false;

  async function loadUsers() {
    try {
      const r = await fetch('/api/users', { headers: hdrs() });
      if (r.ok) usersList = await r.json();
    } catch {}
  }

  function startNewUser() {
    editingUser = { username: '', password: '', role: 'user', isNew: true };
    userMsg = '';
  }

  function startEditUser(u) {
    editingUser = { ...u, password: '', isNew: false };
    userMsg = '';
  }

  async function saveUser() {
    if (savingUser) return;
    savingUser = true;
    userMsg = '';
    try {
      const url = editingUser.isNew
        ? '/api/users'
        : '/api/users/' + encodeURIComponent(editingUser.username);
      const method = editingUser.isNew ? 'POST' : 'PUT';
      const body = { username: editingUser.username, role: editingUser.role };
      if (editingUser.password) body.password = editingUser.password;
      const r = await fetch(url, {
        method,
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (r.ok) {
        editingUser = null;
        await loadUsers();
      } else {
        const e = await r.json().catch(() => ({}));
        userMsg = e.error || 'Error al guardar';
        userMsgError = true;
      }
    } catch {
      userMsg = 'Error de red';
      userMsgError = true;
    }
    savingUser = false;
  }

  async function deleteUser(username) {
    if (!confirm(`¿Eliminar usuario "${username}"?`)) return;
    try {
      await fetch('/api/users/' + encodeURIComponent(username), {
        method: 'DELETE',
        headers: hdrs(),
      });
      await loadUsers();
    } catch {}
  }

  // ───── Shares ─────
  let shares = [];
  let pools = [];

  // Modal de nueva carpeta
  let creatingShare = false;
  let newShareForm = null; // { name, pool, smb, nfs, ftp, public }
  let savingShare = false;
  let shareMsg = '';
  let shareMsgError = false;

  async function loadShares() {
    try {
      const [rs, rp] = await Promise.all([
        fetch('/api/shares', { headers: hdrs() }),
        // Beta 8.1 · solo BTRFS · usar V2 stack
        fetch('/api/storage/v2/pools', { headers: hdrs() }),
      ]);
      if (rs.ok) shares = await rs.json();
      if (rp.ok) {
        const pd = await rp.json();
        // V2 stack devuelve { data: [...pools] } envuelto en apiResponse
        pools = pd.data || pd.pools || (Array.isArray(pd) ? pd : []);
      }
    } catch {}
  }

  function startNewShare() {
    if (pools.length === 0) {
      shareMsg = 'Necesitas crear un pool de storage primero';
      shareMsgError = true;
      return;
    }
    newShareForm = {
      name: '',
      pool: pools[0]?.name || '',
      smb: true,
      nfs: false,
      ftp: false,
      public: false,
    };
    creatingShare = true;
    shareMsg = '';
  }

  function cancelNewShare() {
    creatingShare = false;
    newShareForm = null;
    shareMsg = '';
  }

  async function saveNewShare() {
    if (!newShareForm || !newShareForm.name) return;
    if (savingShare) return;
    savingShare = true;
    shareMsg = '';
    try {
      const r = await fetch('/api/shares', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify(newShareForm),
      });
      if (r.ok) {
        creatingShare = false;
        newShareForm = null;
        await loadShares();
      } else {
        const e = await r.json().catch(() => ({}));
        shareMsg = e.error || 'Error al crear';
        shareMsgError = true;
      }
    } catch {
      shareMsg = 'Error de red';
      shareMsgError = true;
    }
    savingShare = false;
  }

  // ───── Updates ─────
  let updateData = {};
  let checking = false;
  let applying = false;
  let updateMsg = '';
  let updateMsgError = false;

  async function loadUpdateInfo() {
    try {
      const r = await fetch('/api/updates/info', { headers: hdrs() });
      if (r.ok) updateData = await r.json();
    } catch {}
  }

  async function checkForUpdates() {
    if (checking) return;
    checking = true;
    updateMsg = '';
    try {
      const r = await fetch('/api/updates/check', { method: 'POST', headers: hdrs() });
      if (r.ok) updateData = await r.json();
      else updateMsg = 'Error al comprobar', updateMsgError = true;
    } catch {
      updateMsg = 'Error de red';
      updateMsgError = true;
    }
    checking = false;
  }

  async function applyUpdate() {
    if (applying) return;
    if (!confirm('¿Aplicar la actualización? El sistema puede reiniciarse.')) return;
    applying = true;
    updateMsg = '';
    try {
      const r = await fetch('/api/updates/apply', { method: 'POST', headers: hdrs() });
      if (r.ok) updateMsg = 'Actualización en curso...';
      else updateMsg = 'Error al actualizar', updateMsgError = true;
    } catch {
      updateMsg = 'Error de red';
      updateMsgError = true;
    }
    applying = false;
  }

  // ───── 2FA ─────
  let twofa = { loading: true, enabled: false };
  let twofaSetup = null;
  let twofaQrSvg = '';
  let twofaCode = '';
  let twofaBackupCodes = null;
  let twofaSaving = false;
  let twofaMsg = '';
  let twofaMsgError = false;
  let showDisableConfirm = false;
  let twofaDisablePassword = '';

  async function loadTwoFA() {
    twofa.loading = true;
    try {
      const r = await fetch('/api/auth/2fa/status', { headers: hdrs() });
      if (r.ok) {
        const d = await r.json();
        twofa = { loading: false, enabled: !!d.enabled };
      } else twofa.loading = false;
    } catch { twofa.loading = false; }
  }

  async function twofa_startSetup() {
    twofaSaving = true;
    twofaMsg = '';
    try {
      const r = await fetch('/api/auth/2fa/setup', { method: 'POST', headers: hdrs() });
      if (r.ok) {
        const d = await r.json();
        twofaSetup = { secret: d.secret };
        twofaQrSvg = d.qr || '';
      } else twofaMsg = 'Error al iniciar', twofaMsgError = true;
    } catch { twofaMsg = 'Error de red'; twofaMsgError = true; }
    twofaSaving = false;
  }

  async function twofa_verify() {
    if (!twofaCode || twofaCode.length !== 6) return;
    twofaSaving = true;
    twofaMsg = '';
    try {
      const r = await fetch('/api/auth/2fa/verify', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ code: twofaCode }),
      });
      if (r.ok) {
        const d = await r.json();
        twofaBackupCodes = d.backupCodes || [];
        twofa.enabled = true;
        twofaSetup = null;
        twofaQrSvg = '';
        twofaCode = '';
      } else twofaMsg = 'Código incorrecto', twofaMsgError = true;
    } catch { twofaMsg = 'Error de red'; twofaMsgError = true; }
    twofaSaving = false;
  }

  async function twofa_disable() {
    twofaSaving = true;
    twofaMsg = '';
    try {
      const r = await fetch('/api/auth/2fa/disable', {
        method: 'POST',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: twofaDisablePassword }),
      });
      if (r.ok) {
        twofa.enabled = false;
        showDisableConfirm = false;
        twofaDisablePassword = '';
      } else twofaMsg = 'Contraseña incorrecta', twofaMsgError = true;
    } catch { twofaMsg = 'Error de red'; twofaMsgError = true; }
    twofaSaving = false;
  }

  // ───── About / System info ─────
  let sysInfo = {};
  async function loadSysInfo() {
    try {
      const r = await fetch('/api/system/info', { headers: hdrs() });
      if (r.ok) sysInfo = await r.json();
    } catch {}
  }

  // ───── Lazy loading por sección ─────
  $: if (activeView === 'users' && usersList.length === 0) loadUsers();
  $: if (activeView === 'shares' && shares.length === 0) loadShares();
  $: if (activeView === 'updates' && !updateData.currentVersion) loadUpdateInfo();
  $: if (activeView === 'portal' && twofa.loading) loadTwoFA();
  $: if (activeView === 'about' && !sysInfo.kernel) loadSysInfo();
  $: if (activeView === 'appearance' && systemWallpapers.length === 0 && !wallpapersLoading) loadWallpapers();

  onMount(() => {
    loadWallpapers();
  });

  // Date format helper
  function fmtUptime(s) {
    if (!s) return '—';
    const days = Math.floor(s / 86400);
    const hrs = Math.floor((s % 86400) / 3600);
    return `${days}d ${hrs}h`;
  }

  // Path segments dinámicos
  $: pathSegments = ['nimsettings', activeView];

  // Encontrar el label del item activo (buscar en todos los grupos)
  $: activeLabel = sections
    .flatMap(g => g.items)
    .find(it => it.id === activeView)?.label || 'Settings';

  // Accent colors disponibles (los 6 predefinidos + custom)
  const ACCENT_PRESETS = [
    { id: 'green',   hex: '#00ff9f', label: 'Verde fósforo' },
    { id: 'amber',   hex: '#ffb800', label: 'Ámbar' },
    { id: 'cyan',    hex: '#4db8ff', label: 'Cian' },
    { id: 'magenta', hex: '#e873ff', label: 'Magenta' },
    { id: 'orange',  hex: '#ff8c3f', label: 'Naranja' },
    { id: 'red',     hex: '#ff5a5a', label: 'Rojo' },
  ];
</script>

<AppShell
  appId="nimsettings"
  title="NimSettings"
  headerIcon="⚙"
  {sections}
  bind:active={activeView}
  pathSegments={pathSegments}
  bodyPadding={false}
>
  <svelte:fragment slot="page-header">
    <b>{activeLabel}</b>
  </svelte:fragment>

  <div class="settings-content">

    {#if activeView === 'monitor'}
      <div class="section-label">Monitor del sistema</div>
      <div class="coming-soon">Dashboard de métricas — coming soon</div>

    {:else if activeView === 'users'}
      <div class="section-label">Usuarios del sistema</div>

      {#if editingUser}
        <div class="form-card">
          <div class="form-title">{editingUser.isNew ? 'Nuevo usuario' : `Editar ${editingUser.username}`}</div>
          <div class="form-field">
            <label>Usuario</label>
            <input
              type="text"
              class="form-input"
              bind:value={editingUser.username}
              disabled={!editingUser.isNew}
            />
          </div>
          <div class="form-field">
            <label>Contraseña {editingUser.isNew ? '' : '(dejar en blanco para no cambiar)'}</label>
            <input type="password" class="form-input" bind:value={editingUser.password} />
          </div>
          <div class="form-field">
            <label>Rol</label>
            <div class="setting-options">
              <button class="opt-btn" class:active={editingUser.role === 'user'} on:click={() => editingUser.role = 'user'}>Usuario</button>
              <button class="opt-btn" class:active={editingUser.role === 'admin'} on:click={() => editingUser.role = 'admin'}>Admin</button>
            </div>
          </div>
          {#if userMsg}
            <div class="form-msg" class:error={userMsgError}>{userMsg}</div>
          {/if}
          <div class="form-actions">
            <button class="btn-accent" on:click={saveUser} disabled={savingUser}>
              {savingUser ? 'Guardando...' : 'Guardar'}
            </button>
            <button class="btn-secondary" on:click={() => editingUser = null}>Cancelar</button>
          </div>
        </div>
      {:else}
        {#if usersList.length === 0}
          <div class="coming-soon">Cargando usuarios...</div>
        {:else}
          <div class="user-list">
            {#each usersList as u}
              <div class="user-row">
                <div class="user-avatar">{(u.username || '?')[0].toUpperCase()}</div>
                <span class="user-name">{u.username}</span>
                <div class="user-badge" class:admin={u.role === 'admin'}>{u.role || 'user'}</div>
                <div class="row-actions">
                  <button class="action-btn" on:click={() => startEditUser(u)} title="Editar">
                    <svg viewBox="0 0 24 24"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4z"/></svg>
                  </button>
                  {#if u.username !== $user?.username}
                    <button class="action-btn danger" on:click={() => deleteUser(u.username)} title="Eliminar">
                      <svg viewBox="0 0 24 24"><polyline points="3 6 5 6 21 6"/><path d="M19 6l-1 14H6L5 6"/><path d="M10 11v6M14 11v6"/><path d="M9 6V4h6v2"/></svg>
                    </button>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
          <button class="btn-accent" style="margin-top: 16px" on:click={startNewUser}>+ Nuevo usuario</button>
        {/if}
      {/if}

    {:else if activeView === 'shares'}
      <div class="section-header-row">
        <div class="section-label" style="margin-bottom: 0">Carpetas compartidas</div>
        <button class="btn-secondary" style="padding: 6px 14px" on:click={startNewShare}>+ Nueva carpeta</button>
      </div>

      {#if shareMsg}
        <div class="form-msg" class:error={shareMsgError} style="margin-bottom: 14px">{shareMsg}</div>
      {/if}

      {#if creatingShare && newShareForm}
        <div class="form-card" style="margin-bottom: 16px">
          <div class="form-title">Nueva carpeta compartida</div>

          <div class="form-field">
            <label>Nombre</label>
            <input type="text" class="form-input" bind:value={newShareForm.name} placeholder="ej: media" />
          </div>

          <div class="form-field">
            <label>Pool</label>
            <select class="form-input" bind:value={newShareForm.pool}>
              {#each pools as p}
                <option value={p.name}>{p.name}</option>
              {/each}
            </select>
          </div>

          <div class="form-field">
            <label>Protocolos</label>
            <div class="proto-toggles">
              <label class="proto-toggle">
                <input type="checkbox" bind:checked={newShareForm.smb} />
                <span>SMB</span>
              </label>
              <label class="proto-toggle">
                <input type="checkbox" bind:checked={newShareForm.nfs} />
                <span>NFS</span>
              </label>
              <label class="proto-toggle">
                <input type="checkbox" bind:checked={newShareForm.ftp} />
                <span>FTP</span>
              </label>
            </div>
          </div>

          <div class="form-field">
            <label class="proto-toggle">
              <input type="checkbox" bind:checked={newShareForm.public} />
              <span>Acceso público (sin autenticación)</span>
            </label>
          </div>

          <div class="form-actions">
            <button class="btn-accent" on:click={saveNewShare} disabled={savingShare || !newShareForm.name}>
              {savingShare ? 'Creando...' : 'Crear carpeta'}
            </button>
            <button class="btn-secondary" on:click={cancelNewShare}>Cancelar</button>
          </div>
        </div>
      {/if}

      {#if shares.length === 0 && !creatingShare}
        <div class="coming-soon">No hay carpetas compartidas. Crea la primera con el botón "+ Nueva carpeta".</div>
      {:else if shares.length > 0}
        <div class="share-list">
          {#each shares as s}
            <div class="share-pill">
              <div class="share-head">
                <div class="share-icon">📁</div>
                <div class="share-ident">
                  <div class="share-name">{s.displayName || s.name}</div>
                  <div class="share-sub">{s.pool || '—'} · {Object.keys(s.permissions || {}).length} usuarios</div>
                </div>
                <div class="share-protocols">
                  <span class="proto" class:on={s.smb}>SMB</span>
                  <span class="proto" class:on={s.nfs}>NFS</span>
                  <span class="proto" class:on={s.ftp}>FTP</span>
                </div>
              </div>
            </div>
          {/each}
        </div>
      {/if}

    {:else if activeView === 'permissions'}
      <div class="section-label">Permisos de apps</div>
      <div class="coming-soon">Sistema de permisos de aplicaciones</div>

    {:else if activeView === 'portal'}
      <div class="section-label">Autenticación en dos pasos (2FA)</div>

      {#if twofa.loading}
        <div class="coming-soon">Cargando...</div>

      {:else if twofaBackupCodes}
        <div class="twofa-success">
          <div class="twofa-success-icon">
            <svg viewBox="0 0 24 24"><polyline points="20 6 9 17 4 12"/></svg>
          </div>
          <div class="twofa-success-title">2FA activado correctamente</div>
          <p class="twofa-success-desc">
            Guarda estos códigos de recuperación en un lugar seguro. Son de un solo uso y te permitirán acceder si pierdes tu dispositivo.
          </p>
          <div class="backup-codes-grid">
            {#each twofaBackupCodes as code}
              <div class="backup-code">{code}</div>
            {/each}
          </div>
          <button class="btn-secondary" style="margin-top: 14px" on:click={() => twofaBackupCodes = null}>
            Ya los he guardado
          </button>
        </div>

      {:else if twofaSetup}
        <div class="form-card">
          <div class="form-title">Configurar 2FA</div>
          <p class="form-desc">1. Escanea el QR con Google Authenticator, Authy, o app compatible TOTP</p>
          <div class="qr-wrap">
            {#if twofaQrSvg}
              {@html twofaQrSvg}
            {/if}
          </div>
          <div class="form-field">
            <label>Clave manual</label>
            <code class="hex-display">{twofaSetup.secret}</code>
          </div>
          <p class="form-desc" style="margin-top: 18px">2. Introduce el código de 6 dígitos</p>
          <div class="form-row">
            <input
              type="text"
              class="form-input"
              placeholder="000000"
              maxlength="6"
              bind:value={twofaCode}
              on:input={() => twofaCode = twofaCode.replace(/\D/g, '')}
            />
            <button class="btn-accent" on:click={twofa_verify} disabled={twofaSaving || twofaCode.length !== 6}>
              {twofaSaving ? 'Verificando...' : 'Verificar'}
            </button>
            <button class="btn-secondary" on:click={() => { twofaSetup = null; twofaCode = ''; }}>Cancelar</button>
          </div>
          {#if twofaMsg}<div class="form-msg" class:error={twofaMsgError}>{twofaMsg}</div>{/if}
        </div>

      {:else if twofa.enabled}
        <div class="twofa-status-card enabled">
          <div class="twofa-status-icon enabled">
            <svg viewBox="0 0 24 24"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>
          </div>
          <div class="twofa-status-info">
            <div class="twofa-status-title">2FA activado</div>
            <div class="twofa-status-desc">Tu cuenta está protegida con autenticación en dos pasos.</div>
          </div>
          <div class="twofa-status-badge enabled">Activo</div>
        </div>

        {#if !showDisableConfirm}
          <button class="btn-danger-outline" style="margin-top: 14px" on:click={() => showDisableConfirm = true}>
            Desactivar 2FA
          </button>
        {:else}
          <div class="form-card" style="margin-top: 14px">
            <div class="form-title">Confirma tu contraseña para desactivar 2FA</div>
            <div class="form-row">
              <input type="password" class="form-input" placeholder="Contraseña actual" bind:value={twofaDisablePassword} />
              <button class="btn-danger-outline" on:click={twofa_disable} disabled={twofaSaving}>
                {twofaSaving ? '...' : 'Desactivar'}
              </button>
              <button class="btn-secondary" on:click={() => { showDisableConfirm = false; twofaDisablePassword = ''; }}>Cancelar</button>
            </div>
            {#if twofaMsg}<div class="form-msg" class:error={twofaMsgError}>{twofaMsg}</div>{/if}
          </div>
        {/if}

      {:else}
        <div class="twofa-status-card">
          <div class="twofa-status-icon">
            <svg viewBox="0 0 24 24"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 9.9-1"/></svg>
          </div>
          <div class="twofa-status-info">
            <div class="twofa-status-title">2FA desactivado</div>
            <div class="twofa-status-desc">Añade una capa extra de seguridad con Google Authenticator u otra app TOTP compatible.</div>
          </div>
          <div class="twofa-status-badge">Inactivo</div>
        </div>
        <button class="btn-accent" style="margin-top: 14px" on:click={twofa_startSetup} disabled={twofaSaving}>
          {twofaSaving ? 'Configurando...' : 'Configurar 2FA'}
        </button>
      {/if}

    {:else if activeView === 'updates'}
      <div class="section-label">Actualizaciones del sistema</div>
      <div class="field-group">
        <div class="field-row">
          <span class="field-label">Versión actual</span>
          <span class="field-value">{updateData.currentVersion || updateData.current || updateData.version || '—'}</span>
        </div>
        <div class="field-row">
          <span class="field-label">Última versión</span>
          <span class="field-value">{updateData.latestVersion || updateData.latest || '—'}</span>
        </div>
        <div class="field-row">
          <span class="field-label">Estado</span>
          <span class="field-value" class:warn={updateData.updateAvailable} class:ok={!updateData.updateAvailable}>
            ▸ {updateData.updateAvailable ? 'Actualización disponible' : 'Al día'}
          </span>
        </div>
      </div>
      <div class="update-actions">
        <button class="btn-secondary" on:click={checkForUpdates} disabled={checking || applying}>
          {checking ? 'Comprobando...' : 'Comprobar actualizaciones'}
        </button>
        {#if updateData.updateAvailable}
          <button class="btn-accent" on:click={applyUpdate} disabled={applying}>
            {applying ? 'Actualizando...' : 'Aplicar actualización'}
          </button>
        {/if}
      </div>
      {#if updateMsg}<div class="form-msg" class:error={updateMsgError} style="margin-top: 14px">{updateMsg}</div>{/if}

    {:else if activeView === 'appearance'}
      <!-- Tab nav inline -->
      <div class="tab-nav">
        <button class="tab" class:active={appearanceTab === 'tema'} on:click={() => appearanceTab = 'tema'}>Tema</button>
        <button class="tab" class:active={appearanceTab === 'taskbar'} on:click={() => appearanceTab = 'taskbar'}>Taskbar</button>
        <button class="tab" class:active={appearanceTab === 'escala'} on:click={() => appearanceTab = 'escala'}>Escala</button>
      </div>

      {#if appearanceTab === 'tema'}
        <!-- ────── TEMA DEL SISTEMA ────── -->
        <div class="section-label">Tema del sistema</div>
        <div class="theme-row">
          {#each ['dark', 'cream'] as t}
            <div class="theme-card" class:active={currentTheme === t} on:click={() => selectTheme(t)} on:keydown={(e) => e.key === 'Enter' && selectTheme(t)} role="button" tabindex="0">
              <div class="tp-frame {t}">
                <div class="tp-window">
                  <div class="tp-tb">
                    <span class="tp-cube"></span>
                    <span class="tp-path">nimos://<b>storage</b></span>
                    <span class="tp-leds">
                      <span class="l min"></span><span class="l max"></span><span class="l close"></span>
                    </span>
                  </div>
                  <div class="tp-body">
                    <div class="tp-card">
                      <span class="tp-tab-pz">POOLS</span>
                      <div class="tp-card-body">2<div class="sub">▸ ONLINE</div></div>
                    </div>
                    <div class="tp-card">
                      <span class="tp-tab-pz">USO</span>
                      <div class="tp-card-body">58%<div class="sub">▸ 5.2 TB</div></div>
                    </div>
                  </div>
                </div>
                <div class="tp-clock-led">
                  <span class="d"></span><span class="d"></span>
                  <span class="d" style="width:2px"></span>
                  <span class="d"></span><span class="d"></span>
                </div>
                <div class="tp-taskbar">
                  <svg class="logo" viewBox="-15 0 200 185" fill="none">
                    <rect x="5" y="45" width="80" height="80" rx="16" transform="rotate(-30 45 85)" fill={t === 'cream' ? '#1a1a1a' : '#fff'}/>
                    <rect x="108" y="12" width="60" height="60" rx="10" fill={t === 'cream' ? '#1a1a1a' : '#fff'}/>
                    <rect x="108" y="98" width="60" height="60" rx="10" fill={t === 'cream' ? '#1a1a1a' : '#fff'}/>
                  </svg>
                  <span class="clock">13:06</span>
                </div>
              </div>
              <div class="theme-card-label">
                <span>{t === 'dark' ? 'Dark' : 'Cream'}</span>
                <span class="check"></span>
              </div>
            </div>
          {/each}
        </div>

        <!-- ────── COLOR DE ACENTO ────── -->
        <div class="section-label" style="margin-top: 32px">Color de acento</div>
        <div class="accent-row">
          {#each ACCENT_PRESETS as preset}
            <div
              class="accent-dot"
              class:active={currentAccent === preset.id}
              style="background: {preset.hex}; color: {preset.hex}"
              title={preset.label}
              on:click={() => selectAccent(preset.id)}
              on:keydown={(e) => e.key === 'Enter' && selectAccent(preset.id)}
              role="button"
              tabindex="0"
            ></div>
          {/each}
        </div>

        <!-- Custom hex input -->
        <div class="custom-hex">
          <span class="custom-hex-label">Hex personalizado</span>
          <div class="custom-hex-row">
            <div class="hex-preview" style="background: {customAccent || '#00ff9f'}"></div>
            <input
              type="text"
              class="form-input hex-input"
              placeholder="#00ff9f"
              bind:value={customHexInput}
              maxlength="7"
            />
            <button class="btn-secondary" on:click={applyCustomHex}>Aplicar</button>
          </div>
          {#if customHexErr}<div class="form-msg error" style="margin-top: 8px">{customHexErr}</div>{/if}
        </div>

        <!-- ────── WALLPAPERS ────── -->
        <div class="wall-header">
          <div class="section-label" style="margin-bottom: 0">Fondo de escritorio</div>
          <label class="wall-add-btn">
            <svg viewBox="0 0 24 24"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
            Añadir imagen
            <input type="file" accept="image/*" on:change={uploadWallpaper} bind:this={wallUploadInput} style="display: none"/>
          </label>
        </div>
        {#if wallUploadMsg}<div class="form-msg" class:error={wallUploadErr} style="margin-bottom: 12px">{wallUploadMsg}</div>{/if}
        <div class="wall-grid">
          <!-- Default · sin wallpaper (usa --wallpaper NimOS del CSS) -->
          <div class="wall-item" class:active={!currentWallpaper} on:click={() => selectWallpaper('')} on:keydown={(e) => e.key === 'Enter' && selectWallpaper('')} role="button" tabindex="0">
            <div class="wall-none">NimOS</div>
            {#if !currentWallpaper}<div class="wall-check">✓</div>{/if}
          </div>

          <!-- Wallpapers del sistema -->
          {#each systemWallpapers as wp}
            <div class="wall-item" class:active={currentWallpaper === wp.url} on:click={() => selectWallpaper(wp.url)} on:keydown={(e) => e.key === 'Enter' && selectWallpaper(wp.url)} role="button" tabindex="0">
              <img src={wp.url} alt={wp.name} class="wall-thumb" loading="lazy" />
              <span class="wall-tag-sys">SISTEMA</span>
              {#if currentWallpaper === wp.url}<div class="wall-check">✓</div>{/if}
            </div>
          {/each}

          <!-- Wallpapers subidos por el user -->
          {#each userWallpapers as wp}
            <div class="wall-item user-wp" class:active={currentWallpaper === wp.url}>
              <img src={wp.url} alt={wp.name} class="wall-thumb" loading="lazy" on:click={() => selectWallpaper(wp.url)}/>
              <span class="wall-tag-user">MI</span>
              {#if currentWallpaper === wp.url}<div class="wall-check">✓</div>{/if}
              <button class="wall-delete" on:click|stopPropagation={() => deleteUserWallpaper(wp.url)} title="Eliminar">×</button>
            </div>
          {/each}
        </div>

      {:else if appearanceTab === 'taskbar'}
        <div class="section-label">Estilo del taskbar</div>
        <div class="setting-row">
          <span class="setting-label">Modo</span>
          <div class="setting-options">
            <button class="opt-btn" class:active={$prefs.taskbarMode === 'classic'} on:click={() => setPref('taskbarMode', 'classic')}>Clásico</button>
            <button class="opt-btn" class:active={$prefs.taskbarMode === 'dock'} on:click={() => setPref('taskbarMode', 'dock')}>Dock</button>
          </div>
        </div>
        <div class="setting-row">
          <span class="setting-label">Posición</span>
          <div class="setting-options">
            {#each [{v:'bottom', l:'Abajo'}, {v:'top', l:'Arriba'}, {v:'left', l:'Izquierda'}] as opt}
              <button class="opt-btn"
                class:active={$prefs.taskbarPosition === opt.v}
                disabled={$prefs.taskbarMode === 'dock' && opt.v === 'left'}
                on:click={() => setPref('taskbarPosition', opt.v)}
              >{opt.l}</button>
            {/each}
          </div>
        </div>
        <div class="setting-row">
          <span class="setting-label">Tamaño</span>
          <div class="setting-options">
            {#each [{v:'small', l:'Pequeño'}, {v:'medium', l:'Medio'}, {v:'large', l:'Grande'}] as opt}
              <button class="opt-btn"
                class:active={$prefs.taskbarSize === opt.v}
                on:click={() => setPref('taskbarSize', opt.v)}
              >{opt.l}</button>
            {/each}
          </div>
        </div>

      {:else if appearanceTab === 'escala'}
        <div class="section-label">Escala de interfaz</div>
        <div class="setting-row">
          <span class="setting-label">Escala UI</span>
          <div class="setting-options">
            {#each [{v:'auto', l:'Auto'}, {v:85, l:'85%'}, {v:100, l:'100%'}, {v:115, l:'115%'}, {v:125, l:'125%'}, {v:150, l:'150%'}] as opt}
              <button class="opt-btn"
                class:active={$prefs.uiScale === opt.v}
                on:click={() => setPref('uiScale', opt.v)}
              >{opt.l}</button>
            {/each}
          </div>
        </div>
        <div class="info-strip">
          ▸ Pantalla: {typeof window !== 'undefined' ? `${window.screen.width}×${window.screen.height}` : '—'}
          · DPR: {typeof window !== 'undefined' ? window.devicePixelRatio?.toFixed(2) : '—'}
          · CSS: {typeof window !== 'undefined' ? `${window.innerWidth}×${window.innerHeight}` : '—'}
        </div>
      {/if}

    {:else if activeView === 'about'}
      <div class="section-label">Información del sistema</div>
      <div class="about-hero">
        <svg class="about-logo" viewBox="-15 0 200 185" fill="none">
          <rect x="5" y="45" width="80" height="80" rx="16" transform="rotate(-30 45 85)" fill="currentColor"/>
          <rect x="108" y="12" width="60" height="60" rx="10" fill="currentColor"/>
          <rect x="108" y="98" width="60" height="60" rx="10" fill="currentColor"/>
        </svg>
        <div class="about-info">
          <div class="about-name">NimOS</div>
          <div class="about-version">Beta 8.1.0 · {sysInfo.buildDate || 'dev'}</div>
        </div>
      </div>
      <div class="field-group">
        <div class="field-row"><span class="field-label">Kernel</span><span class="field-value">{sysInfo.kernel || '—'}</span></div>
        <div class="field-row"><span class="field-label">Arquitectura</span><span class="field-value">{sysInfo.arch || '—'}</span></div>
        <div class="field-row"><span class="field-label">Hostname</span><span class="field-value">{sysInfo.hostname || '—'}</span></div>
        <div class="field-row"><span class="field-label">Uptime</span><span class="field-value">{fmtUptime(sysInfo.uptime)}</span></div>
      </div>
    {/if}

  </div>
</AppShell>

<style>
  /* ═══════════════════════════════════════════════════════════
     SETTINGS · estilos completos
     ═══════════════════════════════════════════════════════════ */
  .settings-content {
    padding: 22px 28px;
  }
  .section-label {
    font-size: 10px;
    color: var(--ink-trace);
    letter-spacing: 2px;
    text-transform: uppercase;
    font-weight: 700;
    margin-bottom: 14px;
  }
  .section-header-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 16px;
  }
  .coming-soon {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--ink-mute);
    padding: 16px;
    border: 1px dashed var(--line-bright);
    text-align: center;
    letter-spacing: 1px;
    max-width: 720px;
  }

  /* ─── Tab nav inline ─── */
  .tab-nav {
    display: flex;
    gap: 2px;
    margin-bottom: 22px;
    border-bottom: 1px solid var(--line);
  }
  .tab {
    padding: 8px 14px;
    background: transparent;
    border: none;
    color: var(--ink-mute);
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 1.5px;
    text-transform: uppercase;
    font-weight: 700;
    cursor: pointer;
    border-bottom: 2px solid transparent;
    margin-bottom: -1px;
  }
  .tab:hover { color: var(--ink); }
  .tab.active {
    color: var(--ink);
    border-bottom-color: var(--signal);
    text-shadow: 0 0 4px var(--accent-glow-soft);
  }

  /* ═══════════════════════════════════════════════════════════
     THEME CARDS · preview real del sistema
     ═══════════════════════════════════════════════════════════ */
  .theme-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 18px;
    max-width: 720px;
  }
  .theme-card {
    border: 1px solid var(--line);
    cursor: pointer;
    transition: border-color 0.18s;
    overflow: hidden;
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 12px), calc(100% - 12px) 100%, 0 100%);
  }
  .theme-card:hover { border-color: var(--line-bright); }
  .theme-card.active {
    border-color: var(--signal);
    box-shadow: 0 0 0 1px var(--signal), 0 0 20px var(--signal-glow);
  }
  .tp-frame {
    height: 180px;
    position: relative;
    overflow: hidden;
  }
  .tp-frame.dark {
    --tp-canvas: #050505;
    --tp-panel: #161616;
    --tp-panel-elev: #1c1c1c;
    --tp-ink: #f2f2f5;
    --tp-ink-mute: #9a9aa3;
    --tp-line: rgba(255,255,255,0.08);
    --tp-line-bright: rgba(255,255,255,0.14);
    background:
      linear-gradient(rgba(0, 255, 159, 0.015) 1px, transparent 1px) 0 0 / 8px 8px,
      linear-gradient(90deg, rgba(0, 255, 159, 0.015) 1px, transparent 1px) 0 0 / 8px 8px,
      linear-gradient(rgba(0, 255, 159, 0.04) 1px, transparent 1px) 0 0 / 30px 30px,
      linear-gradient(90deg, rgba(0, 255, 159, 0.04) 1px, transparent 1px) 0 0 / 30px 30px,
      radial-gradient(ellipse 50% 45% at 20% 25%, rgba(0, 255, 159, 0.06) 0%, transparent 60%),
      var(--tp-canvas);
  }
  .tp-frame.cream {
    --tp-canvas: #f5f5f0;
    --tp-panel: #fdfdf7;
    --tp-panel-elev: #ffffff;
    --tp-ink: #1a1a1a;
    --tp-ink-mute: #6a6a72;
    --tp-line: rgba(0,0,0,0.10);
    --tp-line-bright: rgba(0,0,0,0.18);
    background:
      linear-gradient(rgba(0, 0, 0, 0.015) 1px, transparent 1px) 0 0 / 8px 8px,
      linear-gradient(90deg, rgba(0, 0, 0, 0.015) 1px, transparent 1px) 0 0 / 8px 8px,
      linear-gradient(rgba(0, 0, 0, 0.03) 1px, transparent 1px) 0 0 / 30px 30px,
      linear-gradient(90deg, rgba(0, 0, 0, 0.03) 1px, transparent 1px) 0 0 / 30px 30px,
      radial-gradient(ellipse 50% 45% at 20% 25%, rgba(0, 200, 130, 0.06) 0%, transparent 60%),
      var(--tp-canvas);
  }
  .tp-window {
    position: absolute;
    top: 12px;
    left: 12px;
    width: 160px;
    background: var(--tp-panel);
    border: 1px solid var(--tp-line-bright);
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 10px), calc(100% - 10px) 100%, 0 100%);
    font-family: var(--font-mono);
    font-size: 7px;
  }
  .tp-tb {
    height: 14px;
    background: var(--tp-panel-elev);
    border-bottom: 1px solid var(--tp-line);
    display: flex;
    align-items: center;
    padding: 0 6px;
    gap: 4px;
  }
  .tp-cube { width: 4px; height: 4px; background: var(--tp-ink); transform: rotate(45deg); }
  .tp-path { flex: 1; color: var(--tp-ink-mute); font-size: 6px; }
  .tp-path :global(b) { color: var(--tp-ink); }
  .tp-leds { display: flex; gap: 3px; }
  .tp-leds .l { width: 4px; height: 4px; }
  .tp-leds .min { background: var(--warn); }
  .tp-leds .max { background: var(--signal); }
  .tp-leds .close { background: var(--crit); }
  .tp-body {
    padding: 6px;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 3px;
  }
  .tp-card { position: relative; padding-top: 8px; }
  .tp-tab-pz {
    position: absolute;
    top: 0; left: 0;
    background: var(--tp-panel-elev);
    font-size: 5px;
    padding: 1px 4px;
    color: var(--tp-ink);
    letter-spacing: 0.5px;
    font-weight: 700;
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 3px), calc(100% - 3px) 100%, 0 100%);
  }
  .tp-card-body {
    background: var(--tp-canvas);
    padding: 6px 4px 4px;
    border: 1px solid var(--tp-line);
    font-size: 9px;
    color: var(--tp-ink);
    font-weight: 700;
  }
  .tp-card-body .sub { font-size: 5px; color: var(--tp-ink-mute); margin-top: 1px; font-weight: 400; }
  .tp-clock-led {
    position: absolute;
    bottom: 22px;
    right: 14px;
    display: flex;
    gap: 1px;
    background: rgba(0,0,0,0.5);
    padding: 2px;
  }
  .tp-clock-led .d { width: 6px; height: 9px; background: rgba(255,255,255,0.9); }
  .tp-frame.cream .tp-clock-led { background: rgba(255,255,255,0.7); }
  .tp-frame.cream .tp-clock-led .d { background: rgba(0,0,0,0.85); }
  .tp-taskbar {
    position: absolute;
    left: 0; right: 0; bottom: 0;
    height: 16px;
    background: var(--tp-panel);
    border-top: 1px solid var(--tp-line-bright);
    display: flex;
    align-items: center;
    padding: 0 6px;
  }
  .tp-taskbar .logo { width: 10px; height: 10px; }
  .tp-taskbar .clock {
    margin-left: auto;
    font-family: var(--font-mono);
    font-size: 7px;
    color: var(--tp-ink);
    font-weight: 700;
  }
  .theme-card-label {
    padding: 10px 14px;
    background: var(--panel-elev);
    border-top: 1px solid var(--line);
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 1.5px;
    color: var(--ink);
    font-weight: 700;
    text-transform: uppercase;
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .theme-card.active .theme-card-label {
    background: var(--signal-soft);
    color: var(--signal);
  }
  .theme-card-label .check {
    width: 14px;
    height: 14px;
    border: 1px solid var(--line-bright);
  }
  .theme-card.active .check {
    background: var(--signal);
    border-color: var(--signal);
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .theme-card.active .check::after {
    content: '✓';
    color: #000;
    font-size: 10px;
    font-weight: 900;
  }

  /* ═══════════════════════════════════════════════════════════
     ACCENT PICKER · 6 dots + custom hex
     ═══════════════════════════════════════════════════════════ */
  .accent-row {
    display: flex;
    gap: 12px;
    margin-bottom: 18px;
  }
  .accent-dot {
    width: 36px;
    height: 36px;
    cursor: pointer;
    position: relative;
    transition: transform 0.15s, box-shadow 0.15s;
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 6px), calc(100% - 6px) 100%, 0 100%);
  }
  .accent-dot.active {
    transform: scale(1.1);
    box-shadow: 0 0 0 2px var(--panel), 0 0 0 4px currentColor;
  }
  .accent-dot.active::after {
    content: '✓';
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    color: #000;
    font-size: 14px;
    font-weight: 900;
  }

  .custom-hex {
    margin-bottom: 14px;
    max-width: 460px;
  }
  .custom-hex-label {
    font-size: 11px;
    color: var(--ink-mute);
    display: block;
    margin-bottom: 6px;
    font-family: var(--font-mono);
    letter-spacing: 0.5px;
  }
  .custom-hex-row {
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .hex-preview {
    width: 32px;
    height: 32px;
    border: 1px solid var(--line-bright);
    flex-shrink: 0;
  }
  .hex-input {
    flex: 1;
    font-family: var(--font-mono);
    text-transform: uppercase;
  }

  /* ═══════════════════════════════════════════════════════════
     WALLPAPERS
     ═══════════════════════════════════════════════════════════ */
  .wall-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-top: 32px;
    margin-bottom: 14px;
  }
  .wall-add-btn {
    background: transparent;
    border: 1px solid var(--line-bright);
    color: var(--ink-dim);
    padding: 6px 12px;
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 1.5px;
    text-transform: uppercase;
    font-weight: 700;
    display: flex;
    align-items: center;
    gap: 6px;
    cursor: pointer;
    transition: background 0.15s, color 0.15s, border-color 0.15s;
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 6px), calc(100% - 6px) 100%, 0 100%);
  }
  .wall-add-btn:hover {
    background: var(--signal-soft);
    border-color: var(--signal);
    color: var(--signal);
  }
  .wall-add-btn svg { width: 11px; height: 11px; stroke: currentColor; fill: none; stroke-width: 2.5; stroke-linecap: round; }
  .wall-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
    gap: 12px;
  }
  .wall-item {
    position: relative;
    height: 100px;
    border: 1px solid var(--line);
    cursor: pointer;
    overflow: hidden;
    background: var(--canvas-soft);
    transition: border-color 0.15s;
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 8px), calc(100% - 8px) 100%, 0 100%);
  }
  .wall-item:hover { border-color: var(--line-bright); }
  .wall-item.active {
    border-color: var(--signal);
    box-shadow: 0 0 0 1px var(--signal);
  }
  .wall-thumb {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }
  .wall-none {
    width: 100%;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--ink-mute);
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 1.5px;
    text-transform: uppercase;
    background:
      linear-gradient(rgba(0, 255, 159, 0.04) 1px, transparent 1px) 0 0 / 12px 12px,
      linear-gradient(90deg, rgba(0, 255, 159, 0.04) 1px, transparent 1px) 0 0 / 12px 12px,
      #0a0a0a;
  }
  .wall-check {
    position: absolute;
    top: 8px;
    right: 8px;
    width: 22px;
    height: 22px;
    background: var(--signal);
    display: flex;
    align-items: center;
    justify-content: center;
    color: #000;
    font-weight: 900;
    font-size: 12px;
  }
  .wall-tag-sys, .wall-tag-user {
    position: absolute;
    bottom: 8px;
    left: 8px;
    padding: 2px 6px;
    background: rgba(0, 0, 0, 0.7);
    color: var(--ink);
    font-family: var(--font-mono);
    font-size: 8px;
    letter-spacing: 1.5px;
    font-weight: 700;
  }
  .wall-tag-user {
    background: var(--signal);
    color: #000;
  }
  .wall-delete {
    position: absolute;
    bottom: 8px;
    right: 8px;
    width: 22px;
    height: 22px;
    background: rgba(0, 0, 0, 0.7);
    color: var(--ink);
    border: none;
    cursor: pointer;
    font-size: 16px;
    line-height: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    transition: background 0.15s;
  }
  .wall-delete:hover {
    background: var(--crit);
  }

  /* ═══════════════════════════════════════════════════════════
     SETTING ROWS · option buttons
     ═══════════════════════════════════════════════════════════ */
  .setting-row {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 10px 0;
  }
  .setting-label {
    font-size: 12px;
    color: var(--ink-dim);
    min-width: 100px;
  }
  .setting-options {
    display: flex;
    gap: 2px;
    border: 1px solid var(--line);
    padding: 2px;
  }
  .opt-btn {
    padding: 6px 14px;
    background: transparent;
    border: none;
    color: var(--ink-mute);
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 1.5px;
    text-transform: uppercase;
    font-weight: 700;
    cursor: pointer;
    transition: background 0.12s, color 0.12s;
  }
  .opt-btn:hover { color: var(--ink); }
  .opt-btn.active {
    background: var(--signal-soft);
    color: var(--signal);
    text-shadow: 0 0 4px var(--signal-glow);
  }
  .opt-btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .info-strip {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--ink-mute);
    margin-top: 14px;
    padding: 10px 14px;
    background: var(--canvas-soft);
    border: 1px solid var(--line);
    max-width: 540px;
  }

  /* ═══════════════════════════════════════════════════════════
     USERS · CRUD list
     ═══════════════════════════════════════════════════════════ */
  .user-list { display: flex; flex-direction: column; gap: 4px; max-width: 720px; }
  .user-row {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 12px 14px;
    background: var(--canvas-soft);
    border: 1px solid var(--line);
  }
  .user-avatar {
    width: 32px;
    height: 32px;
    background: var(--signal-dim);
    color: var(--signal);
    display: flex;
    align-items: center;
    justify-content: center;
    font-weight: 700;
    font-size: 14px;
    text-shadow: 0 0 4px var(--signal-glow);
  }
  .user-name { color: var(--ink); font-weight: 500; font-size: 13px; flex: 1; }
  .user-badge {
    padding: 3px 10px;
    font-family: var(--font-mono);
    font-size: 9px;
    letter-spacing: 1.5px;
    text-transform: uppercase;
    font-weight: 700;
    background: var(--line);
    color: var(--ink-mute);
  }
  .user-badge.admin {
    background: var(--signal-soft);
    color: var(--signal);
  }
  .row-actions { display: flex; gap: 4px; }
  .action-btn {
    width: 28px;
    height: 28px;
    background: transparent;
    border: 1px solid transparent;
    color: var(--ink-mute);
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    transition: all 0.12s;
  }
  .action-btn:hover {
    background: var(--line);
    color: var(--ink);
    border-color: var(--line-bright);
  }
  .action-btn.danger:hover {
    background: rgba(248,113,113,0.1);
    color: var(--crit);
    border-color: rgba(248,113,113,0.3);
  }
  .action-btn svg { width: 14px; height: 14px; stroke: currentColor; fill: none; stroke-width: 2; stroke-linecap: round; stroke-linejoin: round; }

  /* ═══════════════════════════════════════════════════════════
     FORM CARDS · modales/edit inline
     ═══════════════════════════════════════════════════════════ */
  .form-card {
    background: var(--canvas-soft);
    border: 1px solid var(--line);
    padding: 18px 20px;
    max-width: 540px;
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 12px), calc(100% - 12px) 100%, 0 100%);
  }
  .form-title {
    font-size: 13px;
    font-weight: 600;
    color: var(--ink);
    margin-bottom: 16px;
  }
  .form-desc {
    font-size: 11px;
    color: var(--ink-mute);
    margin-bottom: 14px;
    line-height: 1.5;
  }
  .form-field {
    margin-bottom: 14px;
  }
  .form-field label {
    display: block;
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--ink-mute);
    letter-spacing: 1px;
    text-transform: uppercase;
    margin-bottom: 6px;
    font-weight: 700;
  }
  .form-input {
    width: 100%;
    padding: 8px 12px;
    background: var(--panel);
    border: 1px solid var(--line-bright);
    color: var(--ink);
    font-family: var(--font-sans);
    font-size: 13px;
  }
  .form-input:focus {
    outline: none;
    border-color: var(--signal);
    box-shadow: 0 0 0 1px var(--signal);
  }
  .form-row {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .form-row .form-input {
    flex: 1;
    min-width: 200px;
  }
  .form-actions {
    display: flex;
    gap: 10px;
    margin-top: 16px;
  }
  .form-msg {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--ink-mute);
    padding: 8px 12px;
    background: var(--canvas-soft);
    border: 1px solid var(--line);
    margin-top: 10px;
  }
  .form-msg.error {
    color: var(--crit);
    border-color: rgba(248,113,113,0.3);
    background: rgba(248,113,113,0.05);
  }

  /* ═══════════════════════════════════════════════════════════
     BUTTONS
     ═══════════════════════════════════════════════════════════ */
  .btn-accent {
    padding: 8px 18px;
    background: var(--signal);
    color: #000;
    border: none;
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 1.5px;
    text-transform: uppercase;
    font-weight: 700;
    cursor: pointer;
    transition: filter 0.15s;
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 6px), calc(100% - 6px) 100%, 0 100%);
  }
  .btn-accent:hover { filter: brightness(1.15); }
  .btn-accent:disabled { opacity: 0.5; cursor: not-allowed; }

  .btn-secondary {
    padding: 8px 18px;
    background: transparent;
    border: 1px solid var(--line-bright);
    color: var(--ink);
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 1.5px;
    text-transform: uppercase;
    font-weight: 700;
    cursor: pointer;
    transition: background 0.15s, border-color 0.15s;
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 6px), calc(100% - 6px) 100%, 0 100%);
  }
  .btn-secondary:hover {
    background: var(--line);
    border-color: var(--line-strong);
  }

  .btn-danger-outline {
    padding: 8px 18px;
    background: transparent;
    border: 1px solid rgba(248,113,113,0.4);
    color: var(--crit);
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 1.5px;
    text-transform: uppercase;
    font-weight: 700;
    cursor: pointer;
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 6px), calc(100% - 6px) 100%, 0 100%);
  }
  .btn-danger-outline:hover {
    background: rgba(248,113,113,0.1);
    border-color: var(--crit);
  }

  /* ═══════════════════════════════════════════════════════════
     SHARES
     ═══════════════════════════════════════════════════════════ */
  .proto-toggles {
    display: flex;
    gap: 14px;
  }
  .proto-toggle {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    color: var(--ink-dim);
    font-size: 13px;
  }
  .proto-toggle input[type="checkbox"] {
    width: 16px;
    height: 16px;
    accent-color: var(--signal);
    cursor: pointer;
  }
  .share-list { display: flex; flex-direction: column; gap: 6px; max-width: 760px; }
  .share-pill {
    background: var(--canvas-soft);
    border: 1px solid var(--line);
    transition: border-color 0.12s;
  }
  .share-pill:hover { border-color: var(--line-bright); }
  .share-head {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 12px 14px;
    cursor: pointer;
  }
  .share-icon {
    width: 28px;
    height: 28px;
    background: var(--signal-dim);
    color: var(--signal);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 16px;
  }
  .share-ident { flex: 1; min-width: 0; }
  .share-name { color: var(--ink); font-weight: 500; font-size: 13px; }
  .share-sub {
    color: var(--ink-mute);
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 0.5px;
    margin-top: 2px;
  }
  .share-protocols { display: flex; gap: 4px; }
  .share-protocols .proto {
    padding: 3px 8px;
    font-family: var(--font-mono);
    font-size: 9px;
    letter-spacing: 1.5px;
    font-weight: 700;
    background: var(--line);
    color: var(--ink-trace);
  }
  .share-protocols .proto.on {
    background: var(--signal-soft);
    color: var(--signal);
    text-shadow: 0 0 4px var(--signal-glow);
  }

  /* ═══════════════════════════════════════════════════════════
     PORTAL · 2FA
     ═══════════════════════════════════════════════════════════ */
  .twofa-status-card {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 18px 20px;
    background: var(--canvas-soft);
    border: 1px solid var(--line);
    max-width: 720px;
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 10px), calc(100% - 10px) 100%, 0 100%);
  }
  .twofa-status-card.enabled {
    border-color: var(--signal);
    background: var(--signal-soft);
  }
  .twofa-status-icon {
    width: 40px;
    height: 40px;
    background: var(--line);
    color: var(--ink-mute);
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .twofa-status-icon.enabled {
    background: var(--signal);
    color: #000;
  }
  .twofa-status-icon svg { width: 20px; height: 20px; stroke: currentColor; fill: none; stroke-width: 2; }
  .twofa-status-info { flex: 1; }
  .twofa-status-title { color: var(--ink); font-weight: 600; font-size: 14px; }
  .twofa-status-desc { color: var(--ink-mute); font-size: 11px; margin-top: 3px; }
  .twofa-status-badge {
    padding: 4px 12px;
    font-family: var(--font-mono);
    font-size: 9px;
    letter-spacing: 1.5px;
    font-weight: 700;
    text-transform: uppercase;
    background: var(--line);
    color: var(--ink-mute);
  }
  .twofa-status-badge.enabled {
    background: var(--signal);
    color: #000;
    box-shadow: 0 0 8px var(--signal-glow);
  }
  .twofa-success {
    padding: 20px;
    border: 1px solid var(--signal);
    background: var(--signal-soft);
    max-width: 640px;
    clip-path: polygon(0 0, 100% 0, 100% calc(100% - 12px), calc(100% - 12px) 100%, 0 100%);
  }
  .twofa-success-icon {
    width: 48px;
    height: 48px;
    background: var(--signal);
    color: #000;
    display: flex;
    align-items: center;
    justify-content: center;
    margin-bottom: 14px;
  }
  .twofa-success-icon svg { width: 24px; height: 24px; stroke: currentColor; fill: none; stroke-width: 3; stroke-linecap: round; }
  .twofa-success-title { font-size: 15px; font-weight: 600; color: var(--ink); }
  .twofa-success-desc { font-size: 12px; color: var(--ink-dim); margin-top: 6px; margin-bottom: 14px; line-height: 1.5; }
  .backup-codes-grid {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 6px;
  }
  .backup-code {
    font-family: var(--font-mono);
    font-size: 13px;
    padding: 8px 10px;
    background: var(--panel);
    border: 1px solid var(--line);
    color: var(--ink);
    letter-spacing: 1.5px;
    font-weight: 700;
    text-align: center;
  }
  .qr-wrap {
    width: 180px;
    height: 180px;
    padding: 10px;
    background: #fff;
    margin: 0 auto 14px;
  }
  .qr-wrap :global(svg) {
    width: 100%;
    height: 100%;
  }
  .hex-display {
    display: block;
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--ink);
    padding: 8px 12px;
    background: var(--panel);
    border: 1px solid var(--line);
    word-break: break-all;
    letter-spacing: 1px;
  }

  /* ═══════════════════════════════════════════════════════════
     UPDATES · field group
     ═══════════════════════════════════════════════════════════ */
  .field-group {
    background: var(--canvas-soft);
    border: 1px solid var(--line);
    max-width: 540px;
    margin-bottom: 16px;
  }
  .field-row {
    display: flex;
    align-items: center;
    padding: 12px 16px;
    border-bottom: 1px solid var(--line);
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .field-row:last-child { border-bottom: none; }
  .field-label { color: var(--ink-mute); flex: 1; letter-spacing: 0.5px; }
  .field-value { color: var(--ink); font-weight: 500; }
  .field-value.ok { color: var(--signal); text-shadow: 0 0 4px var(--signal-glow); }
  .field-value.warn { color: var(--warn); }
  .update-actions { display: flex; gap: 10px; }

  /* ═══════════════════════════════════════════════════════════
     ABOUT
     ═══════════════════════════════════════════════════════════ */
  .about-hero {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 18px;
    background: var(--canvas-soft);
    border: 1px solid var(--line);
    max-width: 540px;
    margin-bottom: 16px;
  }
  .about-logo {
    width: 60px;
    height: 60px;
    color: var(--ink);
    filter: drop-shadow(0 0 8px var(--accent-glow-soft));
  }
  .about-info { flex: 1; }
  .about-name {
    color: var(--ink);
    font-size: 18px;
    font-weight: 700;
    text-shadow: 0 0 6px var(--accent-glow-soft);
  }
  .about-version {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--ink-mute);
    letter-spacing: 0.5px;
    margin-top: 2px;
  }
</style>
