<script>
  /**
   * ServiceWebDAV · Panel de Control · página dedicada de WebDAV
   * ──────────────────────────────────────────────────────────────
   * WebDAV no existe aún en el backend. Página informativa con back.
   */
  import { createEventDispatcher } from 'svelte';
  const dispatch = createEventDispatcher();
</script>

<div class="svc-page">
  <div class="sp-head">
    <button class="sp-back" on:click={() => dispatch('back')} title="Volver">‹</button>
    <span class="sp-led"></span>
    <div class="sp-title">WebDAV <span class="sp-sub">· no disponible</span></div>
  </div>

  <div class="sp-soon">
    <div class="sp-soon-icon"></div>
    <div class="sp-soon-title">WebDAV todavía no está disponible</div>
    <div class="sp-soon-desc">
      El acceso a archivos por HTTP/WebDAV se añadirá en una versión futura. Permitirá montar
      tus carpetas compartidas desde el explorador de archivos de cualquier sistema sin software extra.
    </div>
  </div>
</div>

<style>
  .svc-page { display: flex; flex-direction: column; gap: 18px; max-width: 860px; }
  .sp-head { display: flex; align-items: center; gap: 12px; }
  .sp-back {
    width: 28px; height: 28px;
    border: 1px solid var(--bd-2, #20202a);
    border-radius: 6px;
    background: var(--bg-inner, #101015);
    color: var(--fg-3, #9c9ca4);
    cursor: pointer; font-size: 16px; line-height: 1; flex-shrink: 0;
  }
  .sp-back:hover { color: var(--fg, #f0f0f0); border-color: var(--bd-3, #2a2a32); }
  .sp-led { width: 9px; height: 9px; border-radius: 2.5px; background: var(--fg-5, #5a5a62); }
  .sp-title { font-size: 14px; color: var(--fg, #f0f0f0); font-weight: 600; font-family: var(--font-mono); }
  .sp-sub { color: var(--fg-4, #7a7a82); font-size: 12px; font-weight: 400; }

  .sp-soon {
    display: flex; flex-direction: column; align-items: center; text-align: center;
    gap: 10px; padding: 50px 24px;
  }
  .sp-soon-icon {
    width: 36px; height: 36px; border-radius: 9px;
    background: var(--bg-card, #15151a);
    border: 1px solid var(--bd-3, #2a2a32);
    margin-bottom: 4px;
  }
  .sp-soon-title { font-size: 14px; color: var(--fg-2, #d0d0d4); font-family: var(--font-mono); }
  .sp-soon-desc {
    font-size: 11px; color: var(--fg-5, #5a5a62);
    font-family: var(--font-mono); line-height: 1.6; max-width: 420px;
  }
</style>
