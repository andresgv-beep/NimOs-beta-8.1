<script>
  /**
   * CPMaintenance · Panel de Control · sección Limpieza y Mantenimiento
   * ───────────────────────────────────────────────────────────────────
   * Lista las tareas de mantenimiento registradas en el daemon. Por cada una:
   *   · toggle on/off
   *   · frecuencia configurable: cada X (interval) · diario a hh:mm · semanal
   *     (día + hh:mm) · al arranque
   *   · "ejecutar ahora"
   *   · última / próxima ejecución
   * Debajo, historial de ejecuciones recientes.
   *
   * Consume /api/maintenance/* (la API ya devuelve lastRun/nextRun por tarea).
   * Respuestas del daemon: JSON plano.
   */
  import { onMount, onDestroy } from 'svelte';
  import { hdrs } from '$lib/stores/auth.js';

  let tasks = [];
  let history = [];
  let loading = true;
  let err = '';
  let pollTimer = null;
  // edición local de schedule por tarea (id -> draft)
  let drafts = {};

  const weekdays = [
    { v: 0, l: 'Dom' }, { v: 1, l: 'Lun' }, { v: 2, l: 'Mar' }, { v: 3, l: 'Mié' },
    { v: 4, l: 'Jue' }, { v: 5, l: 'Vie' }, { v: 6, l: 'Sáb' },
  ];

  async function load() {
    try {
      const r = await fetch('/api/maintenance/tasks', { headers: hdrs() });
      if (r.ok) {
        const d = await r.json();
        tasks = d.tasks || [];
        // sembrar drafts desde la config actual (sin pisar ediciones en curso)
        for (const t of tasks) {
          if (!drafts[t.id]) drafts[t.id] = { ...t.config.schedule };
        }
        drafts = drafts;
      }
      const rh = await fetch('/api/maintenance/history?limit=30', { headers: hdrs() });
      if (rh.ok) { const d = await rh.json(); history = d.history || []; }
      err = '';
    } catch (e) {
      err = 'No se pudo cargar el estado de mantenimiento';
    } finally {
      loading = false;
    }
  }

  async function saveTask(t) {
    const draft = drafts[t.id];
    try {
      const r = await fetch('/api/maintenance/tasks/' + t.id, {
        method: 'PUT',
        headers: { ...hdrs(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: t.config.enabled, schedule: draft }),
      });
      if (!r.ok) throw new Error();
      await load();
    } catch {
      err = 'No se pudo guardar la configuración de ' + t.name;
    }
  }

  async function toggleTask(t) {
    t.config.enabled = !t.config.enabled;
    tasks = tasks;
    await saveTask(t);
  }

  async function runNow(t) {
    t._running = true;
    tasks = tasks;
    try {
      const r = await fetch('/api/maintenance/tasks/' + t.id + '/run', {
        method: 'POST', headers: hdrs(),
      });
      if (r.ok) {
        const d = await r.json();
        t._lastResult = d.skipped
          ? ('Saltada: ' + (d.skipReason || ''))
          : ('Liberados ' + fmtBytes(d.bytesFreed) + ' · ' + (d.itemsRemoved || 0) + ' elementos');
      }
      await load();
    } catch {
      err = 'No se pudo ejecutar ' + t.name;
    } finally {
      t._running = false;
      tasks = tasks;
    }
  }

  function fmtBytes(n) {
    if (!n) return '0 B';
    const u = ['B', 'KB', 'MB', 'GB']; let i = 0; let x = n;
    while (x >= 1024 && i < u.length - 1) { x /= 1024; i++; }
    return x.toFixed(i ? 1 : 0) + ' ' + u[i];
  }

  onMount(() => {
    load();
    pollTimer = setInterval(load, 15000);
  });
  onDestroy(() => clearInterval(pollTimer));
</script>

<div class="cp-maint">
  {#if loading}
    <div class="m-hint">Cargando tareas de mantenimiento…</div>
  {:else}
    {#if err}<div class="m-err">{err}</div>{/if}

    <!-- LÍNEA ROJA recordatorio -->
    <div class="m-note">
      El mantenimiento solo limpia temporales, cachés, logs y registros internos.
      Nunca toca tus datos, carpetas ni descargas.
    </div>

    {#each tasks as t (t.id)}
      <div class="m-task">
        <div class="m-head">
          <span class="m-led" class:on={t.config.enabled}></span>
          <div class="m-titlewrap">
            <div class="m-title">{t.name}</div>
            <div class="m-desc">{t.description}</div>
          </div>
          <button class="m-toggle" class:on={t.config.enabled} on:click={() => toggleTask(t)}>
            {t.config.enabled ? 'Activa' : 'Inactiva'}
          </button>
        </div>

        {#if drafts[t.id]}
          <div class="m-cfg">
            <label class="m-field">
              <span>Frecuencia</span>
              <select bind:value={drafts[t.id].kind}>
                <option value="interval">Cada X tiempo</option>
                <option value="daily">Diario</option>
                <option value="weekly">Semanal</option>
                <option value="at_boot">Al arrancar</option>
              </select>
            </label>

            {#if drafts[t.id].kind === 'interval'}
              <label class="m-field">
                <span>Cada (minutos)</span>
                <input type="number" min="5" bind:value={drafts[t.id].intervalMinutes} />
              </label>
            {/if}

            {#if drafts[t.id].kind === 'daily' || drafts[t.id].kind === 'weekly'}
              {#if drafts[t.id].kind === 'weekly'}
                <label class="m-field">
                  <span>Día</span>
                  <select bind:value={drafts[t.id].atWeekday}>
                    {#each weekdays as d}<option value={d.v}>{d.l}</option>{/each}
                  </select>
                </label>
              {/if}
              <label class="m-field">
                <span>Hora</span>
                <input type="number" min="0" max="23" bind:value={drafts[t.id].atHour} />
              </label>
              <label class="m-field">
                <span>Min</span>
                <input type="number" min="0" max="59" bind:value={drafts[t.id].atMinute} />
              </label>
            {/if}

            <button class="m-save" on:click={() => saveTask(t)}>Guardar</button>
            <button class="m-run" disabled={t._running} on:click={() => runNow(t)}>
              {t._running ? 'Ejecutando…' : 'Ejecutar ahora'}
            </button>
          </div>

          <div class="m-meta">
            {#if t.lastRun}<span>Última: {t.lastRun}</span>{/if}
            {#if t.nextRun}<span>Próxima: {t.nextRun}</span>{/if}
            {#if t._lastResult}<span class="m-result">{t._lastResult}</span>{/if}
          </div>
        {/if}
      </div>
    {/each}

    <!-- Historial -->
    {#if history.length}
      <div class="m-hist">
        <div class="m-hist-title">Historial reciente</div>
        <table>
          <thead>
            <tr><th>Tarea</th><th>Cuándo</th><th>Resultado</th><th>Dur.</th></tr>
          </thead>
          <tbody>
            {#each history as h}
              <tr>
                <td>{h.taskId}</td>
                <td>{h.ranAt}</td>
                <td>
                  {#if h.error}<span class="r-err">error</span>
                  {:else if h.skipped}<span class="r-skip">saltada · {h.skipReason || ''}</span>
                  {:else}{h.itemsRemoved || 0} elem · {fmtBytes(h.bytesFreed)}{/if}
                </td>
                <td>{h.durationMs} ms</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
</div>

<style>
  .cp-maint { display: flex; flex-direction: column; gap: 12px; font-family: var(--font-mono); }
  .m-hint, .m-err, .m-note { font-size: 13px; }
  .m-err { color: var(--st-crit, #ff5470); }
  .m-note {
    color: var(--fg-4, #7a7a82); background: var(--bg-inner, #1a1a1f);
    border: 1px solid var(--bd-2, #2a2a33); border-radius: 6px; padding: 8px 10px;
  }

  .m-task {
    background: var(--bg-card, #16161a); border: 1px solid var(--bd-2, #2a2a33);
    border-radius: 8px; padding: 12px;
  }
  .m-head { display: flex; align-items: center; gap: 10px; }
  .m-led {
    width: 11px; height: 11px; border-radius: 3px;
    background: var(--st-crit, #ff5470); flex: 0 0 auto;
  }
  .m-led.on { background: var(--nim-green, #00ff9f); }
  .m-titlewrap { flex: 1 1 auto; }
  .m-title { color: var(--fg, #e4e4e7); font-size: 14px; }
  .m-desc { color: var(--fg-4, #7a7a82); font-size: 12px; margin-top: 2px; }

  .m-toggle {
    background: transparent; border: 1px solid var(--bd-3, #3a3a44);
    color: var(--fg-3, #9a9aa2); border-radius: 5px; padding: 4px 10px;
    font-family: var(--font-mono); font-size: 12px; cursor: pointer;
  }
  .m-toggle.on { border-color: var(--nim-green, #00ff9f); color: var(--nim-green, #00ff9f); }

  .m-cfg {
    display: flex; flex-wrap: wrap; align-items: flex-end; gap: 10px;
    margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--bd-2, #2a2a33);
  }
  .m-field { display: flex; flex-direction: column; gap: 3px; }
  .m-field span { color: var(--fg-4, #7a7a82); font-size: 11px; }
  .m-field select, .m-field input {
    background: var(--bg-inner, #1a1a1f); border: 1px solid var(--bd-3, #3a3a44);
    color: var(--fg, #e4e4e7); border-radius: 5px; padding: 5px 8px;
    font-family: var(--font-mono); font-size: 13px; min-width: 80px;
  }
  .m-save, .m-run {
    border-radius: 5px; padding: 6px 12px; font-family: var(--font-mono);
    font-size: 12px; cursor: pointer; border: 1px solid var(--bd-3, #3a3a44);
    background: transparent; color: var(--fg-3, #9a9aa2);
  }
  .m-run { border-color: var(--nim-green, #00ff9f); color: var(--nim-green, #00ff9f); }
  .m-run:disabled { opacity: 0.5; cursor: default; }

  .m-meta {
    display: flex; flex-wrap: wrap; gap: 14px; margin-top: 10px;
    color: var(--fg-4, #7a7a82); font-size: 12px;
  }
  .m-result { color: var(--nim-green, #00ff9f); }

  .m-hist { margin-top: 8px; }
  .m-hist-title { color: var(--fg-3, #9a9aa2); font-size: 13px; margin-bottom: 6px; }
  table { width: 100%; border-collapse: collapse; font-size: 12px; }
  th, td { text-align: left; padding: 5px 8px; border-bottom: 1px solid var(--bd-2, #2a2a33); }
  th { color: var(--fg-4, #7a7a82); font-weight: normal; }
  td { color: var(--fg-3, #9a9aa2); }
  .r-err { color: var(--st-crit, #ff5470); }
  .r-skip { color: var(--fg-4, #7a7a82); }
</style>
