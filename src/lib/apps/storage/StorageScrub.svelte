<script>
  /**
   * StorageScrub · Vista de scrub manual.
   * ─────────────────────────────────────
   * Lista los pools con botón "Scrub ahora". El scrub es un chequeo de
   * integridad que recorre checksums — puede tardar horas.
   *
   * Props:
   *   · pools     — array de pools del backend
   *   · scrubbing — { [poolName]: boolean } estado por pool
   *   · scrubMsg  — mensaje de feedback del último intento
   *
   * Eventos:
   *   · start — { detail: { poolName } } — el padre dispara la API call
   *             y actualiza scrubbing/scrubMsg
   */
  import { createEventDispatcher } from 'svelte';
  import { SectionHead, BevelButton, EmptyState } from '$lib/ui';
  import { fmtBytes } from './formatters.js';
  import './views-styles.css';

  export let pools = [];
  export let scrubbing = {};
  export let scrubMsg = '';

  const dispatch = createEventDispatcher();

  function onScrub(poolName) {
    dispatch('start', { poolName });
  }
</script>

<div class="st-section">
  <SectionHead>Scrub manual</SectionHead>

  {#if pools.length === 0}
    <EmptyState icon="◇" title="Sin pools" hint="No hay pools para ejecutar scrub" />
  {:else}
    <div class="hint-box">
      <b>¿Qué es scrub?</b> Es un chequeo de integridad que recorre todos los datos del pool
      y verifica checksums. Útil mensualmente para detectar errores silenciosos.
      Puede tardar horas y el sistema irá más lento mientras corre.
    </div>

    <div class="disk-table cols-5-scrub">
      <div class="disk-thead">
        <div>Pool</div>
        <div>Tipo</div>
        <div>Tamaño</div>
        <div>Último scrub</div>
        <div>Acción</div>
      </div>
      {#each pools as pool}
        <div class="disk-row">
          <div class="disk-cell mono">{pool.name}</div>
          <div class="disk-cell">BTRFS</div>
          <div class="disk-cell">{fmtBytes(pool.usage?.total_bytes)}</div>
          <div class="disk-cell tc-mute">—</div>
          <div class="disk-cell">
            <BevelButton
              size="sm"
              onClick={() => onScrub(pool.name)}
              disabled={scrubbing[pool.name]}
            >
              {scrubbing[pool.name] ? '▸ Iniciando...' : '▸ Scrub ahora'}
            </BevelButton>
          </div>
        </div>
      {/each}
    </div>

    {#if scrubMsg}
      <div class="msg">{scrubMsg}</div>
    {/if}
  {/if}
</div>
