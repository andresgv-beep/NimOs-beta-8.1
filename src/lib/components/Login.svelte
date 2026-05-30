<script>
  /**
   * Login · Pantalla de login retro terminal
   * ──────────────────────────────────────────
   */
  import { login } from '$lib/stores/auth.js';
  import BevelButton from '$lib/ui/BevelButton.svelte';
  import LED from '$lib/ui/LED.svelte';

  let username = '';
  let password = '';
  let totpCode = '';
  let error = '';
  let loading = false;
  let needs2FA = false;

  async function handleSubmit(e) {
    e.preventDefault();
    if (!username || !password) return;

    error = '';
    loading = true;

    try {
      const result = await login(username, password, totpCode);
      if (result?.requires2FA) {
        needs2FA = true;
        loading = false;
        return;
      }
      // Login ok → la página se recarga desde el store
    } catch (err) {
      error = err.message || 'Error de autenticación';
      loading = false;
    }
  }
</script>

<div class="login-screen">

  <!-- Boot log simulated arriba -->
  <div class="boot-log">
    <div class="line"><span class="ts">[    0.000000]</span> <span class="lvl ok">OK</span> <span>NimOS v0.8.1-alpha initialized</span></div>
    <div class="line"><span class="ts">[    0.142391]</span> <span class="lvl ok">OK</span> <span>Daemon running on port 5000</span></div>
    <div class="line"><span class="ts">[    0.283412]</span> <span class="lvl info">INFO</span> <span>Design System v3 · Terminal</span></div>
    <div class="line"><span class="ts">[    0.421055]</span> <span class="lvl ok">OK</span> <span>Awaiting authentication<span class="cursor">_</span></span></div>
  </div>

  <div class="login-box">

    <div class="login-header">
      <div class="header-logo"></div>
      <div class="header-text">
        <span class="title">NimOS</span>
        <span class="subtitle">NAS INTERACTIVE MACHINE OS</span>
      </div>
    </div>

    <div class="login-brackets">
      <span>──</span>
      <span class="lbl">AUTH REQUIRED</span>
      <span class="line-fill"></span>
      <LED size={7} />
    </div>

    <form on:submit={handleSubmit}>

      {#if !needs2FA}
        <div class="field">
          <label>
            <span class="lb">user</span>
            <span class="lk">[U]</span>
          </label>
          <input
            type="text"
            bind:value={username}
            placeholder="username"
            autocomplete="username"
            autofocus
            required
          />
        </div>

        <div class="field">
          <label>
            <span class="lb">password</span>
            <span class="lk">[P]</span>
          </label>
          <input
            type="password"
            bind:value={password}
            placeholder="••••••••"
            autocomplete="current-password"
            required
          />
        </div>
      {:else}
        <div class="field">
          <label>
            <span class="lb">2FA code</span>
            <span class="lk">[2]</span>
          </label>
          <input
            type="text"
            bind:value={totpCode}
            placeholder="000000"
            maxlength="6"
            autocomplete="one-time-code"
            autofocus
            required
          />
        </div>
      {/if}

      {#if error}
        <div class="error">
          <span class="err-tag">ERR</span>
          <span>{error}</span>
        </div>
      {/if}

      <div class="actions">
        <BevelButton variant="primary" size="md" disabled={loading} type="submit">
          {loading ? '▸ auth...' : '▸ enter'}
        </BevelButton>
      </div>

    </form>

    <div class="footer-strip">
      <span class="k">build</span> <span class="v">0.8.1-alpha</span>
      <span class="sep">·</span>
      <span class="k">theme</span> <span class="v">retro-v3</span>
    </div>

  </div>

</div>

<style>
  .login-screen {
    width: 100%;
    height: 100vh;
    background: var(--wallpaper);
    display: flex;
    align-items: center;
    justify-content: center;
    font-family: var(--font-mono);
    position: relative;
    overflow: hidden;
  }

  /* Boot log en esquina superior izquierda */
  .boot-log {
    position: absolute;
    top: 20px;
    left: 20px;
    font-size: 10px;
    color: var(--fg-mute);
    line-height: 1.7;
    pointer-events: none;
  }
  .boot-log .line {
    display: flex;
    gap: 8px;
  }
  .boot-log .ts { color: var(--fg-faint); }
  .boot-log .lvl.ok   { color: var(--accent); }
  .boot-log .lvl.info { color: var(--info); }
  .cursor {
    animation: cursor-blink 1s steps(2) infinite;
    color: var(--accent);
  }

  /* Login box */
  .login-box {
    width: 420px;
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
    position: relative;
  }
  .login-box::before, .login-box::after {
    content: '';
    position: absolute;
    width: 14px; height: 14px;
    border-color: var(--accent);
    opacity: 0.6;
  }
  .login-box::before {
    top: 10px; left: 10px;
    border-top: 1px solid; border-left: 1px solid;
  }
  .login-box::after {
    bottom: 10px; right: 10px;
    border-bottom: 1px solid; border-right: 1px solid;
  }

  .login-header {
    display: flex;
    align-items: center;
    gap: 14px;
    padding-bottom: 18px;
    border-bottom: 1px solid var(--border);
  }
  .header-logo {
    width: 28px; height: 28px;
    background: var(--accent);
    clip-path: polygon(50% 0, 100% 50%, 50% 100%, 0 50%);
    box-shadow: 0 0 10px var(--accent-glow);
  }
  .header-text {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .title {
    font-size: 18px;
    font-weight: 700;
    color: var(--fg);
    letter-spacing: 2px;
  }
  .subtitle {
    font-size: 8px;
    color: var(--fg-mute);
    letter-spacing: 2px;
  }

  .login-brackets {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 14px 0 18px;
    font-size: 9px;
    color: var(--fg-mute);
    letter-spacing: 1.8px;
    text-transform: uppercase;
  }
  .login-brackets .lbl { color: var(--accent); }
  .login-brackets .line-fill {
    flex: 1;
    height: 1px;
    background: linear-gradient(to right, var(--border), transparent);
  }

  /* Fields */
  .field {
    margin-bottom: 14px;
  }
  label {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 4px;
    font-size: 9px;
    color: var(--fg-mute);
    letter-spacing: 1.5px;
    text-transform: uppercase;
  }
  label .lk {
    color: var(--fg-faint);
    font-size: 8px;
    border: 1px solid var(--border);
    padding: 0 4px;
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
    transition: border-color 0.12s;
  }
  input:focus {
    border-color: var(--accent);
  }
  input::placeholder {
    color: var(--fg-mute);
  }

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

  .footer-strip {
    margin-top: 22px;
    padding-top: 14px;
    border-top: 1px solid var(--border);
    display: flex;
    gap: 10px;
    font-size: 9px;
    color: var(--fg-mute);
    letter-spacing: 0.8px;
  }
  .footer-strip .k { color: var(--fg-faint); }
  .footer-strip .v { color: var(--fg-dim); }
  .footer-strip .sep { color: var(--fg-faint); }
</style>
