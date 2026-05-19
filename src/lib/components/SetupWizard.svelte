<script>
  /**
   * SetupWizard · Setup inicial primera vez
   * ─────────────────────────────────────────
   * Se muestra cuando el daemon reporta setup=false.
   * Crea usuario admin y password inicial.
   */
  import { completeSetup } from '$lib/stores/auth.js';
  import BevelButton from '$lib/ui/BevelButton.svelte';
  import LED from '$lib/ui/LED.svelte';

  let username = '';
  let password = '';
  let confirm = '';
  let error = '';
  let loading = false;

  async function handleSubmit(e) {
    e.preventDefault();
    if (password !== confirm) {
      error = 'Las contraseñas no coinciden';
      return;
    }
    if (password.length < 8) {
      error = 'La contraseña debe tener al menos 8 caracteres';
      return;
    }

    error = '';
    loading = true;

    try {
      await completeSetup(username, password);
    } catch (err) {
      error = err.message || 'Error en setup';
      loading = false;
    }
  }
</script>

<div class="wizard-screen">
  <div class="wizard-box">

    <div class="header">
      <div class="logo"></div>
      <div>
        <div class="title">NimOS · Setup</div>
        <div class="subtitle">PRIMER ARRANQUE · CREAR ADMIN</div>
      </div>
      <div class="led-wrap">
        <LED variant="warn" size={10} />
      </div>
    </div>

    <div class="steps">
      <span class="step active">[1] Admin</span>
      <span class="step">[2] Pools</span>
      <span class="step">[3] Red</span>
    </div>

    <form on:submit={handleSubmit}>

      <div class="field">
        <label><span class="lb">admin username</span></label>
        <input
          type="text"
          bind:value={username}
          placeholder="admin"
          autocomplete="username"
          autofocus
          required
          minlength="3"
        />
      </div>

      <div class="field">
        <label><span class="lb">password</span></label>
        <input
          type="password"
          bind:value={password}
          placeholder="mínimo 8 caracteres"
          autocomplete="new-password"
          required
          minlength="8"
        />
      </div>

      <div class="field">
        <label><span class="lb">confirm password</span></label>
        <input
          type="password"
          bind:value={confirm}
          placeholder="repite la contraseña"
          autocomplete="new-password"
          required
          minlength="8"
        />
      </div>

      {#if error}
        <div class="error">
          <span class="err-tag">ERR</span>
          <span>{error}</span>
        </div>
      {/if}

      <div class="actions">
        <BevelButton variant="primary" disabled={loading} type="submit">
          {loading ? '▸ creando...' : '▸ Crear admin'}
        </BevelButton>
      </div>

    </form>

  </div>
</div>

<style>
  .wizard-screen {
    width: 100vw;
    height: 100vh;
    background: var(--wallpaper);
    display: flex;
    align-items: center;
    justify-content: center;
    font-family: var(--font-mono);
  }
  .wizard-box {
    width: 480px;
    background: var(--bg);
    border: 1px solid var(--accent);
    box-shadow:
      4px 4px 0 rgba(0, 0, 0, 0.7),
      0 0 30px rgba(0, 255, 159, 0.1);
    padding: 28px 32px;
    clip-path: polygon(
      0 0, calc(100% - var(--bev-md)) 0, 100% var(--bev-md),
      100% 100%, var(--bev-md) 100%, 0 calc(100% - var(--bev-md))
    );
  }

  .header {
    display: flex;
    align-items: center;
    gap: 14px;
    padding-bottom: 18px;
    border-bottom: 1px solid var(--border);
  }
  .logo {
    width: 28px; height: 28px;
    background: var(--accent);
    clip-path: polygon(50% 0, 100% 50%, 50% 100%, 0 50%);
  }
  .title {
    font-size: 16px;
    font-weight: 700;
    color: var(--fg);
    letter-spacing: 1.5px;
  }
  .subtitle {
    font-size: 8px;
    color: var(--fg-mute);
    letter-spacing: 2px;
    margin-top: 2px;
  }
  .led-wrap {
    margin-left: auto;
  }

  .steps {
    display: flex;
    gap: 14px;
    padding: 16px 0;
    font-size: 10px;
    color: var(--fg-mute);
    letter-spacing: 1.5px;
    text-transform: uppercase;
    border-bottom: 1px solid var(--border);
    margin-bottom: 18px;
  }
  .step.active { color: var(--accent); }

  .field { margin-bottom: 14px; }
  label {
    display: block;
    margin-bottom: 4px;
    font-size: 9px;
    color: var(--fg-mute);
    letter-spacing: 1.5px;
    text-transform: uppercase;
  }
  input {
    width: 100%;
    background: var(--bg-1);
    border: 1px solid var(--border);
    padding: 9px 12px;
    color: var(--fg);
    font-family: inherit;
    font-size: 12px;
    letter-spacing: 1px;
    outline: none;
  }
  input:focus { border-color: var(--accent); }
  input::placeholder { color: var(--fg-mute); }

  .error {
    margin: 14px 0;
    padding: 9px 12px;
    background: rgba(255, 90, 90, 0.08);
    border: 1px solid rgba(255, 90, 90, 0.4);
    color: var(--crit);
    font-size: 10px;
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .err-tag {
    background: var(--crit);
    color: var(--bg);
    padding: 1px 6px;
    font-size: 9px;
    font-weight: 700;
    letter-spacing: 1px;
  }

  .actions {
    margin-top: 20px;
    display: flex;
    justify-content: flex-end;
  }
</style>
