<script>
  /**
   * CreatePoolWizard · Wizard to create a new storage pool
   * ─────────────────────────────────────────────────────────
   * 4 pasos: tipo → discos → nombre → confirmación.
   *
   * Filosofía: solo layouts seguros recomendados. El usuario NO elige layout,
   * se calcula automáticamente según tipo y número de discos seleccionados.
   *   ZFS:   1 disk → single, 2 → mirror, 3 → raidz1, 4+ → raidz2
   *   BTRFS: 1 disk → single, 2 → raid1, 3 → raid1, 4+ → raid10
   *
   * Backend:
   *   POST /api/storage/pool { type, name, vdevType|profile, disks: [paths] }
   *
   * Validación de nombre idéntica a la del backend:
   *   ^[a-zA-Z0-9-]{1,32}$ + reserved list
   *
   * Usage:
   *   <CreatePoolWizard
   *     capabilities={{ zfs: true, btrfs: true }}
   *     eligibleDisks={disks.eligible || []}
   *     on:done
   *     on:cancel
   *   />
   */
  import { createEventDispatcher } from 'svelte';
  import { jsonHdrs } from '$lib/stores/auth.js';
  import WizardModal from '$lib/ui/WizardModal.svelte';
  import LED from '$lib/ui/LED.svelte';
  import Badge from '$lib/ui/Badge.svelte';

  export let capabilities = { zfs: false, btrfs: false };
  export let eligibleDisks = [];
  // Bloque C3.3: props para indicar estado de los discos
  //
  // pools             — lista de pools managed (para detectar in-use)
  // orphanFilesystems — filesystems BTRFS huérfanos (para detectar datos preservables)
  //
  // En teoría eligibleDisks YA filtra los managed, pero los orphans pueden
  // aparecer aquí si están desmontados. Necesitamos avisar al usuario.
  export let pools = [];
  export let orphanFilesystems = [];

  // Helper local idéntico al de StorageApp (Bloque C3.3).
  // Determina el estado real de un disco para mostrar avisos.
  function diskStatusLocal(diskPath) {
    if (!diskPath) return { kind: 'free', label: 'disponible', variant: 'accent' };

    for (const pool of pools) {
      const poolDevices = pool.devices || [];
      for (const d of poolDevices) {
        const dPath = typeof d === 'string' ? d : (d.current_path || '');
        if (dPath === diskPath) {
          return {
            kind: 'managed',
            label: `pool ${pool.name}`,
            variant: 'success',
            tooltip: `En uso por pool "${pool.name}"`,
          };
        }
      }
    }

    for (const fs of orphanFilesystems) {
      for (const dev of (fs.devices || [])) {
        if (dev.path === diskPath) {
          return {
            kind: 'orphan',
            label: 'BTRFS huérfano',
            variant: 'warn',
            fsUuid: fs.uuid,
            fsLabel: fs.label,
            tooltip: `Tiene BTRFS no gestionado (${fs.label || fs.uuid}). ` +
                     `Datos preservables si lo importas desde "Observados".`,
          };
        }
      }
    }

    return { kind: 'free', label: 'disponible', variant: 'accent' };
  }

  const dispatch = createEventDispatcher();

  // ─── State ───
  let step = 1;                // 1 = tipo · 2 = discos · 3 = nombre · 4 = confirmar
  let fsType = '';             // 'zfs' | 'btrfs'
  let selectedDisks = new Set(); // paths de discos seleccionados
  let poolName = '';
  let nameError = '';
  let confirmInput = '';
  let processing = false;
  let errorMsg = '';

  // Nombres reservados (espejo exacto del backend)
  const RESERVED_NAMES_ZFS   = ['system', 'config', 'temp', 'swap', 'root', 'boot', 'rpool'];
  const RESERVED_NAMES_BTRFS = ['system', 'config', 'temp', 'swap', 'root', 'boot'];
  $: reservedNames = fsType === 'zfs' ? RESERVED_NAMES_ZFS : RESERVED_NAMES_BTRFS;

  // ─── Derived ───

  // Default fsType cuando el usuario aterriza en paso 1
  $: if (step === 1 && !fsType) {
    if (capabilities.zfs) fsType = 'zfs';
    else if (capabilities.btrfs) fsType = 'btrfs';
  }

  $: diskCount = selectedDisks.size;

  // Calcular layout seguro según tipo + número de discos
  $: layout = computeLayout(fsType, diskCount);

  // Capacidad útil estimada (en bytes)
  // ZFS mirror: size * 1 (el menor) | raidz1: (n-1) * size_menor | raidz2: (n-2) * size_menor
  // BTRFS raid1: total / 2 | raid10: total / 2 | single: suma de todos
  $: selectedDisksArr = eligibleDisks.filter(d => selectedDisks.has(d.path || `/dev/${d.name}`));
  $: usableCapacity = computeUsableCapacity(fsType, layout, selectedDisksArr);

  // ¿El nombre es válido?
  $: {
    nameError = '';
    if (poolName.length > 0) {
      if (poolName.length > 32) {
        nameError = 'Máximo 32 caracteres.';
      } else if (!/^[a-zA-Z0-9-]+$/.test(poolName)) {
        nameError = 'Solo letras, números y guiones.';
      } else if (reservedNames.includes(poolName.toLowerCase())) {
        nameError = `"${poolName}" es un nombre reservado.`;
      }
    }
  }

  $: canAdvance = processing ? false
                : step === 1 ? (fsType === 'zfs' && capabilities.zfs) || (fsType === 'btrfs' && capabilities.btrfs)
                : step === 2 ? diskCount >= 1
                : step === 3 ? poolName.length > 0 && nameError === ''
                : step === 4 ? confirmInput === 'CREAR'
                : false;

  $: nextLabel = step === 4 ? (processing ? 'Creando...' : 'Crear pool') : 'Continuar →';
  $: nextVariant = step === 4 ? 'primary' : 'primary';

  // ─── Layout computation ───
  function computeLayout(fs, n) {
    if (!fs || n < 1) return { id: '', label: '—', redundancy: 'none', desc: '' };
    if (fs === 'zfs') {
      if (n === 1) return { id: 'single',  label: 'Single',        redundancy: 'none',   desc: 'Sin redundancia · toda la capacidad disponible' };
      if (n === 2) return { id: 'mirror',  label: 'Mirror (RAID1)', redundancy: 'n-1',    desc: 'Tolera fallo de 1 disco · capacidad = disco menor' };
      if (n === 3) return { id: 'raidz1',  label: 'RAIDZ1',        redundancy: '1 parity', desc: 'Tolera fallo de 1 disco · capacidad = (n-1) × disco menor' };
      return                { id: 'raidz2',  label: 'RAIDZ2',        redundancy: '2 parity', desc: 'Tolera fallo de 2 discos · capacidad = (n-2) × disco menor' };
    }
    // btrfs
    if (n === 1) return { id: 'single', label: 'Single',       redundancy: 'none', desc: 'Sin redundancia · toda la capacidad disponible' };
    if (n === 2) return { id: 'raid1',  label: 'RAID1',        redundancy: 'n-1',  desc: 'Duplica cada bloque · capacidad = total / 2' };
    if (n === 3) return { id: 'raid1',  label: 'RAID1',        redundancy: 'n-1',  desc: 'BTRFS distribuye copias entre discos · capacidad ~ total / 2' };
    return                { id: 'raid10', label: 'RAID10',       redundancy: 'n-1',  desc: 'Stripe + mirror · capacidad = total / 2 · mejor rendimiento' };
  }

  function computeUsableCapacity(fs, lay, disks) {
    if (disks.length === 0) return 0;
    const sizes = disks.map(d => d.size || 0).filter(s => s > 0);
    if (sizes.length === 0) return 0;
    const smallest = Math.min(...sizes);
    const total = sizes.reduce((a, b) => a + b, 0);
    const n = sizes.length;

    if (lay.id === 'single' || lay.id === 'stripe') return total;
    if (lay.id === 'mirror') return smallest;
    if (lay.id === 'raidz1') return smallest * (n - 1);
    if (lay.id === 'raidz2') return smallest * (n - 2);
    if (lay.id === 'raid1')  return Math.floor(total / 2);
    if (lay.id === 'raid10') return Math.floor(total / 2);
    return total;
  }

  // ─── Handlers ───
  function selectFsType(t) {
    if (t === 'zfs' && !capabilities.zfs) return;
    if (t === 'btrfs' && !capabilities.btrfs) return;
    fsType = t;
  }

  function toggleDisk(path) {
    if (selectedDisks.has(path)) selectedDisks.delete(path);
    else selectedDisks.add(path);
    selectedDisks = selectedDisks; // trigger reactivity
  }

  function handleNext() {
    if (step === 4) {
      submitCreate();
      return;
    }
    step += 1;
    errorMsg = '';
  }

  function handleBack() {
    if (step > 1) {
      step -= 1;
      errorMsg = '';
    }
  }

  function handleCancel() {
    if (processing) return;
    dispatch('cancel');
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

  // ─── Bloque C3.4: Estado del diálogo de doble intención ────────────────
  //
  // Cuando el backend devuelve DISK_HAS_FILESYSTEM, mostramos un diálogo
  // que explica qué se encontró y le da al usuario tres opciones claras:
  //   1. Importar el pool existente (preserva datos)
  //   2. Continuar de todos modos (formatea + destruye)
  //   3. Cancelar
  //
  // El detalle del FS detectado lo envía el backend en err.details:
  //   { disk, fs_type, fs_uuid, fs_label, fs_profile,
  //     is_managed, pool_name, observation_health, size_bytes, used_bytes }
  let collisionDetected = null;     // ErrDiskHasFilesystem details
  let collisionAck = '';            // "DESTRUIR" para confirmar destrucción

  // ─── Create real ───
  async function submitCreate() {
    processing = true;
    errorMsg = '';
    collisionDetected = null;
    collisionAck = '';

    // Beta 8.1 · v2 endpoint /api/storage/pools
    //   · BTRFS-only (no más `type` field; ZFS eliminado)
    //   · v2 acepta `disks: [paths]` o `device_ids: [uuids]`. Usamos paths
    //     porque es lo que la UI maneja naturalmente; el backend resuelve.
    //   · Wrapper de respuesta {data, error}. unwrapV2 extrae o lanza.
    const body = {
      name: poolName,
      profile: layout.id,           // single | raid1 | raid1c3 | raid10
      disks: Array.from(selectedDisks),
    };

    try {
      const res = await fetch('/api/storage/v2/pools', {
        method: 'POST',
        headers: jsonHdrs(),
        body: JSON.stringify(body),
      });
      await unwrapV2(res, 'create pool');
      processing = false;
      dispatch('done', { poolName });
    } catch (err) {
      console.error('create pool error:', err);
      // Bloque C3.4: capturar error tipado de colisión con filesystem existente
      if (err.code === 'DISK_HAS_FILESYSTEM' && err.details) {
        collisionDetected = err.details;
        processing = false;
        return;
      }
      errorMsg = err.message || 'Error al crear el pool';
      processing = false;
    }
  }

  // ─── Acciones del diálogo de colisión ──────────────────────────────────

  // Importar el pool existente. Emite evento para que el StorageApp abra
  // su propio modal de import (que ya existe en C3.2). El wizard se cierra.
  function chooseImport() {
    if (!collisionDetected) return;
    dispatch('request-import', {
      uuid: collisionDetected.fs_uuid,
      label: collisionDetected.fs_label,
      details: collisionDetected,
    });
    // Reset y cerrar
    collisionDetected = null;
    dispatch('cancel');
  }

  // El usuario insiste en crear destruyendo. Confirmación fuerte "DESTRUIR".
  // Tras typed-confirm, reintentamos el create pero antes wipefs los discos.
  async function chooseDestroyAndContinue() {
    if (collisionAck !== 'DESTRUIR') {
      errorMsg = 'Escribe "DESTRUIR" para confirmar la operación destructiva';
      return;
    }
    processing = true;
    errorMsg = '';
    try {
      // Wipefs cada disco seleccionado para limpiar el filesystem detectado
      for (const path of selectedDisks) {
        const wipeRes = await fetch('/api/storage/v2/wipe', {
          method: 'POST',
          headers: jsonHdrs(),
          body: JSON.stringify({ disk: path }),
        });
        await unwrapV2(wipeRes, `wipe ${path}`);
      }

      // Re-lanzar el create (ya con discos limpios)
      collisionDetected = null;
      collisionAck = '';
      await submitCreate();
    } catch (err) {
      errorMsg = err.message || 'Error al wipear discos';
      processing = false;
    }
  }

  function dismissCollision() {
    collisionDetected = null;
    collisionAck = '';
  }

  // ─── Helpers ───
  function fmtBytes(b) {
    if (!b || b === 0) return '0 B';
    if (b >= 1e12) return (b / 1e12).toFixed(1) + ' TB';
    if (b >= 1e9)  return (b / 1e9).toFixed(1)  + ' GB';
    if (b >= 1e6)  return (b / 1e6).toFixed(0)  + ' MB';
    return b + ' B';
  }

  function diskPath(d) {
    return d.path || `/dev/${d.name}`;
  }

  // Detectar si hay tamaños distintos entre los discos seleccionados
  $: hasMixedSizes = (() => {
    if (selectedDisksArr.length < 2) return false;
    const sizes = selectedDisksArr.map(d => d.size || 0);
    const min = Math.min(...sizes);
    const max = Math.max(...sizes);
    // Consideramos "mezclados" si difieren más del 5%
    return max > 0 && (max - min) / max > 0.05;
  })();
</script>

<WizardModal
  open={true}
  title="Crear pool"
  tag={fsType ? fsType.toUpperCase() : ''}
  tagColor="accent"
  currentStep={step}
  totalSteps={4}
  {canAdvance}
  canGoBack={step > 1 && !processing}
  {nextLabel}
  {nextVariant}
  cancelLabel={processing ? 'Procesando...' : 'Cancelar'}
  on:next={handleNext}
  on:back={handleBack}
  on:cancel={handleCancel}
>

  <!-- PASO 1 · Tipo de pool -->
  {#if step === 1}
    <div class="pretitle">PASO 1 · SISTEMA DE ARCHIVOS</div>
    <div class="h">¿ZFS o BTRFS?</div>
    <div class="desc">
      Los dos son sistemas modernos con snapshots e integridad de datos.
      Elige según tus necesidades.
    </div>

    <div class="fs-options">
      <button
        class="fs-card"
        class:selected={fsType === 'zfs'}
        class:disabled={!capabilities.zfs}
        on:click={() => selectFsType('zfs')}
        disabled={!capabilities.zfs}
      >
        <div class="fs-head">
          <div class="fs-name">ZFS</div>
          {#if !capabilities.zfs}
            <Badge size="sm" variant="warn">no disponible</Badge>
          {:else if fsType === 'zfs'}
            <LED size={7} variant="ok" />
          {/if}
        </div>
        <div class="fs-desc">
          Más maduro y robusto. Snapshots instantáneos, replicación,
          compresión automática. Ideal para datos críticos.
        </div>
        <div class="fs-tags">
          <span class="fs-tag">RAIDZ1/Z2</span>
          <span class="fs-tag">ARC cache</span>
          <span class="fs-tag">dedup</span>
        </div>
      </button>

      <button
        class="fs-card"
        class:selected={fsType === 'btrfs'}
        class:disabled={!capabilities.btrfs}
        on:click={() => selectFsType('btrfs')}
        disabled={!capabilities.btrfs}
      >
        <div class="fs-head">
          <div class="fs-name">BTRFS</div>
          {#if !capabilities.btrfs}
            <Badge size="sm" variant="warn">no disponible</Badge>
          {:else if fsType === 'btrfs'}
            <LED size={7} variant="ok" />
          {/if}
        </div>
        <div class="fs-desc">
          Más flexible. Permite añadir discos de uno en uno, mezclar
          tamaños y cambiar el perfil RAID en caliente.
        </div>
        <div class="fs-tags">
          <span class="fs-tag">RAID1/10</span>
          <span class="fs-tag">balance</span>
          <span class="fs-tag">resize</span>
        </div>
      </button>
    </div>
  {/if}

  <!-- PASO 2 · Selección de discos -->
  {#if step === 2}
    <div class="pretitle">PASO 2 · DISCOS</div>
    <div class="h">Selecciona los discos del pool</div>
    <div class="desc">
      Los datos existentes en estos discos se <b>borrarán</b> al crear el pool.
      {#if fsType === 'zfs'}
        ZFS usará el tamaño del <b>disco menor</b> si mezclas capacidades.
      {:else}
        BTRFS puede mezclar capacidades sin desperdiciar espacio.
      {/if}
    </div>

    {#if eligibleDisks.length === 0}
      <div class="no-disks">
        No hay discos libres elegibles. Ve a la vista Discos y formatea
        algún disco primero.
      </div>
    {:else}
      <div class="disk-select-list">
        {#each eligibleDisks as d}
          {@const path = diskPath(d)}
          {@const dStatus = diskStatusLocal(path)}
          <button
            class="disk-select-row"
            class:selected={selectedDisks.has(path)}
            class:has-orphan={dStatus.kind === 'orphan'}
            on:click={() => toggleDisk(path)}
            title={dStatus.tooltip || ''}
          >
            <div class="ds-check">
              {#if selectedDisks.has(path)}✓{/if}
            </div>
            <div class="ds-info">
              <div class="ds-path mono">{path}</div>
              <div class="ds-model">{d.model || '—'}</div>
              {#if dStatus.kind === 'orphan'}
                <div class="ds-orphan-hint">
                  ⚠ Tiene BTRFS huérfano · datos preservables
                </div>
              {/if}
            </div>
            <div class="ds-size">{d.sizeH || fmtBytes(d.size)}</div>
            <Badge size="sm" variant={d.rotational ? 'default' : 'info'}>
              {d.rotational ? 'HDD' : 'SSD'}
            </Badge>
            {#if dStatus.kind === 'orphan'}
              <Badge size="sm" variant="warn">
                {dStatus.label}
              </Badge>
            {/if}
          </button>
        {/each}
      </div>

      <!--
        Bloque C3.3: si hay algún disco con BTRFS huérfano seleccionado,
        mostrar aviso prominente.
      -->
      {#if [...selectedDisks].some(p => diskStatusLocal(p).kind === 'orphan')}
        <div class="orphan-warning">
          <strong>⚠ Atención:</strong> Al menos uno de los discos seleccionados
          tiene un filesystem BTRFS no gestionado. Si continúas, esos datos se
          <strong>borrarán</strong> al crear el nuevo pool.
          <br/>
          Si quieres preservarlos, cancela y usa "Importar como pool" desde la
          sección "Observados".
        </div>
      {/if}
    {/if}

    <!-- Layout recomendado al vuelo -->
    {#if diskCount > 0}
      <div class="layout-preview">
        <div class="lp-head">
          <span class="lp-label">Layout recomendado</span>
          <span class="lp-name">{layout.label}</span>
        </div>
        <div class="lp-desc">{layout.desc}</div>
        <div class="lp-cap">
          <span class="lp-cap-label">Capacidad útil estimada:</span>
          <span class="lp-cap-val">{fmtBytes(usableCapacity)}</span>
        </div>
        {#if hasMixedSizes && fsType === 'zfs'}
          <div class="lp-warn">
            ⚠ Los discos tienen tamaños distintos. ZFS usará el del disco
            menor y desaprovechará el resto.
          </div>
        {/if}
      </div>
    {/if}
  {/if}

  <!-- PASO 3 · Nombre -->
  {#if step === 3}
    <div class="pretitle">PASO 3 · NOMBRE</div>
    <div class="h">Dale un nombre al pool</div>
    <div class="desc">
      Este nombre se usará en la ruta de montaje (<span class="mono">/nimos/pools/{poolName || 'nombre'}</span>)
      y en los shares. Elige algo corto y descriptivo.
    </div>

    <div class="name-input-row">
      <input
        class="name-input mono"
        class:err={nameError !== ''}
        class:ok={poolName.length > 0 && nameError === ''}
        type="text"
        bind:value={poolName}
        placeholder="ej: datos, media, backup"
        autocomplete="off"
        autocorrect="off"
        autocapitalize="off"
        spellcheck="false"
        maxlength="32"
      />
    </div>

    <div class="name-hint">
      <span class:err={nameError !== ''}>
        {#if nameError}
          {nameError}
        {:else if poolName.length === 0}
          Máximo 32 caracteres · letras, números y guiones · sin espacios
        {:else}
          ✓ Nombre válido
        {/if}
      </span>
    </div>

    <!-- Resumen del pool -->
    <div class="summary-box">
      <div class="summary-row">
        <span class="summary-label">Sistema</span>
        <span class="summary-val">{fsType.toUpperCase()}</span>
      </div>
      <div class="summary-row">
        <span class="summary-label">Layout</span>
        <span class="summary-val">{layout.label}</span>
      </div>
      <div class="summary-row">
        <span class="summary-label">Discos</span>
        <span class="summary-val">{diskCount}</span>
      </div>
      <div class="summary-row">
        <span class="summary-label">Capacidad útil</span>
        <span class="summary-val">{fmtBytes(usableCapacity)}</span>
      </div>
    </div>
  {/if}

  <!-- PASO 4 · Confirmación -->
  {#if step === 4}
    <div class="pretitle">PASO 4 · CONFIRMACIÓN</div>
    <div class="h">Última comprobación</div>
    <div class="desc">
      Vas a crear el pool <b class="mono">{poolName}</b> con
      {diskCount} disco{diskCount === 1 ? '' : 's'} en layout <b>{layout.label}</b>.
    </div>

    <ul class="bullets">
      <li>Los datos existentes en los discos se <b>borrarán</b></li>
      <li>El pool se montará en <span class="mono">/nimos/pools/{poolName}</span></li>
      {#if fsType === 'zfs'}
        <li>El zpool interno se llamará <span class="mono">nimos-{poolName}</span></li>
      {/if}
      <li>Podrás gestionar shares, snapshots y apps desde NimOS</li>
    </ul>

    <div class="disks-preview">
      <div class="dp-head">Discos incluidos:</div>
      {#each selectedDisksArr as d}
        <div class="dp-row mono">
          <span>{diskPath(d)}</span>
          <span class="tc-mute">· {d.model || '—'} · {d.sizeH || fmtBytes(d.size)}</span>
        </div>
      {/each}
    </div>

    <div class="confirm-label">Escribe <b>CREAR</b> para confirmar:</div>
    <input
      class="confirm-input"
      class:ok={confirmInput === 'CREAR'}
      type="text"
      bind:value={confirmInput}
      placeholder="CREAR"
      autocomplete="off"
      autocorrect="off"
      autocapitalize="off"
      spellcheck="false"
      disabled={processing}
    />

    {#if errorMsg}
      <div class="err-box">{errorMsg}</div>
    {/if}
  {/if}

</WizardModal>

<!--
  Bloque C3.4 — Diálogo de doble intención
  
  Se muestra cuando el backend devuelve DISK_HAS_FILESYSTEM. El usuario ve
  exactamente qué hay en el disco y decide:
    1. Importar pool existente (preserva datos)
    2. Continuar destruyendo (con typed-confirm "DESTRUIR")
    3. Cancelar
-->
{#if collisionDetected}
  <div class="collision-backdrop" on:click={dismissCollision}>
    <div class="collision-card" on:click|stopPropagation>
      <div class="collision-head">
        <h3>⚠ Filesystem detectado en el disco</h3>
        <button class="collision-close" on:click={dismissCollision}>×</button>
      </div>

      <div class="collision-body">
        <p class="collision-intro">
          El disco <span class="mono">{collisionDetected.disk}</span> contiene un
          filesystem existente. <strong>Antes de continuar, decide qué hacer:</strong>
        </p>

        <div class="collision-info">
          <div class="ci-row">
            <span class="ci-label">Tipo:</span>
            <span class="mono">{collisionDetected.fs_type}{collisionDetected.fs_profile ? ' · ' + collisionDetected.fs_profile : ''}</span>
          </div>
          {#if collisionDetected.fs_label}
            <div class="ci-row">
              <span class="ci-label">Label:</span>
              <span class="mono">{collisionDetected.fs_label}</span>
            </div>
          {/if}
          {#if collisionDetected.fs_uuid}
            <div class="ci-row">
              <span class="ci-label">UUID:</span>
              <span class="mono sm">{collisionDetected.fs_uuid}</span>
            </div>
          {/if}
          {#if collisionDetected.is_managed}
            <div class="ci-row ci-row-warn">
              <span class="ci-label">Pool gestionado:</span>
              <span class="mono"><strong>{collisionDetected.pool_name}</strong></span>
            </div>
          {/if}
          {#if collisionDetected.observation_health}
            <div class="ci-row">
              <span class="ci-label">Estado:</span>
              <span class="mono">{collisionDetected.observation_health}</span>
            </div>
          {/if}
          {#if collisionDetected.size_bytes > 0}
            <div class="ci-row">
              <span class="ci-label">Capacidad:</span>
              <span class="mono">{fmtBytes(collisionDetected.size_bytes)}{collisionDetected.used_bytes > 0 ? ' · ' + fmtBytes(collisionDetected.used_bytes) + ' usados' : ''}</span>
            </div>
          {/if}
        </div>

        <!-- Opción 1: Importar (recomendado) -->
        {#if !collisionDetected.is_managed}
          <div class="collision-option ci-option-import">
            <div class="ci-option-head">
              <span class="ci-option-icon">⬇</span>
              <strong>Importar este pool</strong>
              <span class="ci-option-tag">recomendado</span>
            </div>
            <p class="ci-option-desc">
              Registrar el filesystem existente como pool gestionado por NimOS.
              <strong>Los datos se preservan al 100%</strong>.
            </p>
            <BevelButton variant="primary" size="sm" onClick={chooseImport}>
              Importar como pool
            </BevelButton>
          </div>
        {:else}
          <div class="collision-option ci-option-managed">
            <p class="ci-option-desc">
              Este disco ya pertenece a un pool gestionado. No puedes crear otro
              pool encima sin destruir el actual primero.
            </p>
          </div>
        {/if}

        <!-- Opción 2: Destruir y continuar (peligro) -->
        <div class="collision-option ci-option-destroy">
          <div class="ci-option-head">
            <span class="ci-option-icon">⚠</span>
            <strong>Continuar destruyendo datos</strong>
            <span class="ci-option-tag ci-tag-warn">irreversible</span>
          </div>
          <p class="ci-option-desc">
            Se borrarán <strong>todos los datos</strong> del filesystem actual
            y se creará uno nuevo encima. Esta acción <strong>no se puede deshacer</strong>.
          </p>
          <label class="ci-confirm">
            <span class="ci-confirm-label">Escribe <strong>DESTRUIR</strong> para confirmar:</span>
            <input
              type="text"
              bind:value={collisionAck}
              placeholder="DESTRUIR"
              disabled={processing}
            />
          </label>
          <BevelButton
            size="sm"
            onClick={chooseDestroyAndContinue}
            disabled={processing || collisionAck !== 'DESTRUIR'}
          >
            {processing ? '▸ Procesando...' : 'Destruir y crear pool nuevo'}
          </BevelButton>
        </div>

        {#if errorMsg}
          <div class="ci-error">{errorMsg}</div>
        {/if}
      </div>

      <div class="collision-actions">
        <BevelButton size="sm" onClick={dismissCollision} disabled={processing}>
          Cancelar
        </BevelButton>
      </div>
    </div>
  </div>
{/if}

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
  .mono { font-family: var(--font-mono); }
  .tc-mute { color: var(--fg-mute); }

  /* Paso 1 · Filesystem cards */
  .fs-options {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .fs-card {
    background: var(--bg);
    border: 1px solid var(--border);
    padding: 14px 14px 12px;
    text-align: left;
    cursor: pointer;
    font-family: inherit;
    display: flex;
    flex-direction: column;
    gap: 8px;
    transition: border-color 0.15s, background 0.15s;
  }
  .fs-card:hover:not(.disabled) {
    border-color: var(--accent);
  }
  .fs-card.selected {
    border-color: var(--accent);
    background: rgba(255, 130, 0, 0.04);
  }
  .fs-card.disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .fs-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  .fs-name {
    font-size: 16px;
    color: var(--fg);
    font-weight: 700;
    font-family: var(--font-mono);
    letter-spacing: 1px;
  }
  .fs-desc {
    font-size: 11px;
    color: var(--fg-dim);
    line-height: 1.5;
    font-family: var(--font-sans, inherit);
  }
  .fs-tags {
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
  }
  .fs-tag {
    font-size: 9px;
    padding: 2px 6px;
    background: var(--bg-1);
    color: var(--fg-mute);
    letter-spacing: 0.5px;
    font-family: var(--font-mono);
    border: 1px solid var(--border);
  }

  /* Paso 2 · Disk selection */
  .no-disks {
    padding: 14px;
    background: rgba(255, 184, 0, 0.05);
    border-left: 3px solid var(--warn);
    font-size: 12px;
    color: var(--fg-dim);
    font-family: var(--font-sans, inherit);
    line-height: 1.5;
  }
  .disk-select-list {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border);
  }
  .disk-select-row {
    display: grid;
    grid-template-columns: 22px 1fr auto auto;
    align-items: center;
    gap: 12px;
    padding: 10px 14px;
    background: var(--bg);
    border: none;
    border-bottom: 1px solid var(--border);
    cursor: pointer;
    text-align: left;
    font-family: inherit;
    transition: background 0.1s;
  }
  .disk-select-row:last-child { border-bottom: none; }
  .disk-select-row:hover { background: var(--bg-1); }
  .disk-select-row.selected {
    background: rgba(255, 130, 0, 0.04);
  }
  .ds-check {
    width: 16px;
    height: 16px;
    border: 1px solid var(--border-bright);
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--accent);
    font-size: 12px;
    font-weight: 700;
    font-family: var(--font-mono);
  }
  .disk-select-row.selected .ds-check {
    border-color: var(--accent);
  }
  .ds-info {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .ds-path {
    font-size: 12px;
    color: var(--fg);
  }
  .ds-model {
    font-size: 10px;
    color: var(--fg-mute);
    font-family: var(--font-mono);
  }
  .ds-size {
    font-size: 12px;
    color: var(--fg);
    font-family: var(--font-mono);
  }

  /* Layout preview (paso 2) */
  .layout-preview {
    padding: 12px 14px;
    background: var(--bg-1);
    border: 1px solid var(--border);
    border-left: 3px solid var(--accent);
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-top: 6px;
  }
  .lp-head {
    display: flex;
    align-items: baseline;
    gap: 8px;
  }
  .lp-label {
    font-size: 9px;
    color: var(--fg-faint);
    letter-spacing: 1.5px;
    text-transform: uppercase;
    font-family: var(--font-mono);
  }
  .lp-name {
    font-size: 14px;
    color: var(--accent);
    font-weight: 600;
    font-family: var(--font-mono);
    letter-spacing: 0.5px;
  }
  .lp-desc {
    font-size: 11px;
    color: var(--fg-dim);
    line-height: 1.5;
  }
  .lp-cap {
    display: flex;
    gap: 6px;
    align-items: baseline;
    padding-top: 6px;
    border-top: 1px solid var(--border);
    margin-top: 2px;
  }
  .lp-cap-label {
    font-size: 10px;
    color: var(--fg-mute);
    letter-spacing: 0.5px;
    text-transform: uppercase;
    font-family: var(--font-mono);
  }
  .lp-cap-val {
    font-size: 14px;
    color: var(--fg);
    font-weight: 700;
    font-family: var(--font-mono);
  }
  .lp-warn {
    font-size: 10px;
    color: var(--warn);
    padding: 6px 8px;
    background: rgba(255, 184, 0, 0.05);
    border-left: 2px solid var(--warn);
    font-family: var(--font-mono);
    letter-spacing: 0.3px;
    line-height: 1.4;
  }

  /* Paso 3 · Name */
  .name-input-row { display: flex; }
  .name-input {
    flex: 1;
    padding: 10px 14px;
    background: var(--bg);
    border: 1px solid var(--border-bright);
    color: var(--fg);
    font-size: 14px;
    letter-spacing: 1px;
    outline: none;
    transition: border-color 0.15s, color 0.15s;
  }
  .name-input:focus { border-color: var(--accent); }
  .name-input.ok    { border-color: var(--ok, #00d97e); color: var(--ok, #00d97e); }
  .name-input.err   { border-color: var(--crit); }
  .name-hint {
    font-size: 10px;
    color: var(--fg-mute);
    letter-spacing: 0.3px;
    font-family: var(--font-mono);
  }
  .name-hint .err { color: var(--crit); }

  .summary-box {
    display: flex;
    flex-direction: column;
    background: var(--bg);
    border: 1px solid var(--border);
    margin-top: 4px;
  }
  .summary-row {
    display: grid;
    grid-template-columns: 130px 1fr;
    padding: 8px 14px;
    border-bottom: 1px solid var(--border);
    font-size: 11px;
  }
  .summary-row:last-child { border-bottom: none; }
  .summary-label {
    color: var(--fg-faint);
    font-size: 9px;
    letter-spacing: 1px;
    text-transform: uppercase;
    font-family: var(--font-mono);
  }
  .summary-val {
    color: var(--fg);
    font-family: var(--font-mono);
  }

  /* Paso 4 · Bullets + confirm */
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

  .disks-preview {
    background: var(--bg);
    border: 1px solid var(--border);
    padding: 10px 14px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .dp-head {
    font-size: 9px;
    color: var(--fg-faint);
    letter-spacing: 1.5px;
    text-transform: uppercase;
    font-family: var(--font-mono);
    margin-bottom: 4px;
  }
  .dp-row {
    font-size: 11px;
    color: var(--fg);
    display: flex;
    gap: 4px;
  }

  .confirm-label {
    font-size: 10px;
    color: var(--fg-dim);
    letter-spacing: 1px;
    text-transform: uppercase;
    font-family: var(--font-mono);
    margin-top: 4px;
  }
  .confirm-label :global(b) {
    color: var(--accent);
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

  .err-box {
    padding: 10px 12px;
    background: rgba(255, 90, 90, 0.08);
    border-left: 3px solid var(--crit);
    font-size: 11px;
    color: var(--crit);
    font-family: var(--font-mono);
    letter-spacing: 0.3px;
    line-height: 1.5;
  }

  /* ─── Bloque C3.3: indicadores de estado en wizard ─── */

  .disk-select-row.has-orphan {
    border-left: 2px solid var(--warn);
  }

  .ds-orphan-hint {
    margin-top: 4px;
    font-size: 11px;
    color: var(--warn);
    font-weight: 500;
  }

  .orphan-warning {
    margin-top: 12px;
    padding: 12px 16px;
    background: var(--bg-1);
    border-left: 3px solid var(--warn);
    color: var(--fg);
    font-size: 13px;
    line-height: 1.5;
  }

  .orphan-warning strong {
    color: var(--warn);
  }

  /* ─── Bloque C3.4: Modal de colisión ─── */

  .collision-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.65);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 2000;
    padding: 20px;
  }

  .collision-card {
    background: var(--bg-0);
    border: 1px solid var(--warn);
    max-width: 580px;
    width: 100%;
    max-height: 92vh;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
  }

  .collision-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 16px 20px;
    border-bottom: 1px solid var(--border);
    background: var(--bg-1);
  }

  .collision-head h3 {
    margin: 0;
    font-size: 16px;
    font-weight: 600;
    color: var(--warn);
  }

  .collision-close {
    background: none;
    border: none;
    color: var(--fg-mute);
    font-size: 22px;
    cursor: pointer;
    line-height: 1;
    padding: 0;
    width: 24px;
    height: 24px;
  }

  .collision-close:hover {
    color: var(--fg);
  }

  .collision-body {
    padding: 20px;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .collision-intro {
    margin: 0;
    font-size: 13px;
    line-height: 1.5;
    color: var(--fg-dim);
  }

  .collision-info {
    background: var(--bg-1);
    padding: 12px 14px;
    border: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .ci-row {
    display: flex;
    gap: 10px;
    font-size: 13px;
  }

  .ci-row-warn {
    color: var(--warn);
  }

  .ci-label {
    min-width: 120px;
    color: var(--fg-mute);
  }

  .collision-option {
    padding: 14px 16px;
    border: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .ci-option-import {
    border-left: 3px solid var(--success, #4caf50);
    background: var(--bg-1);
  }

  .ci-option-destroy {
    border-left: 3px solid var(--crit);
    background: var(--bg-1);
  }

  .ci-option-managed {
    border-left: 3px solid var(--fg-mute);
    background: var(--bg-1);
  }

  .ci-option-head {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 14px;
  }

  .ci-option-icon {
    font-size: 16px;
  }

  .ci-option-tag {
    margin-left: auto;
    font-size: 10px;
    padding: 2px 8px;
    background: var(--success, #4caf50);
    color: white;
    letter-spacing: 0.5px;
    text-transform: uppercase;
  }

  .ci-tag-warn {
    background: var(--crit);
  }

  .ci-option-desc {
    margin: 0;
    font-size: 12px;
    line-height: 1.5;
    color: var(--fg-dim);
  }

  .ci-confirm {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .ci-confirm-label {
    font-size: 11px;
    color: var(--fg-mute);
  }

  .ci-confirm input {
    background: var(--bg-2);
    border: 1px solid var(--border);
    color: var(--fg);
    padding: 6px 10px;
    font-family: var(--font-mono);
    font-size: 13px;
  }

  .ci-confirm input:focus {
    border-color: var(--crit);
    outline: none;
  }

  .ci-error {
    color: var(--crit);
    font-size: 13px;
    padding: 8px 10px;
    background: var(--bg-1);
    border-left: 2px solid var(--crit);
  }

  .collision-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    padding: 14px 20px;
    border-top: 1px solid var(--border);
    background: var(--bg-1);
  }
</style>
