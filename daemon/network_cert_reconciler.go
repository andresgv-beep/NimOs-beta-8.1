// network_cert_reconciler.go — Reconciler que mantiene los certs vigentes.
//
// Una pasada (Reconcile):
//
//   1. Listar certs enabled=true.
//   2. Para cada cert decidir si renovar:
//      - applied < desired (config cambió) → renovar.
//      - auto_renew && NotAfter < now + renewWindow → renovar.
//      - sin razón → skip.
//   3. Para cada cert que renueva:
//      a. Resolver CertProvider por nombre.
//      b. Si dns-01: resolver DNSChallengeProvider y cargar token DDNS
//         del nimos_secrets (lookup por provider:domain).
//      c. Abrir network_operations (triggered_by=reconciler:cert_renewer).
//      d. Llamar provider.Issue.
//      e. Si OK: escribir PEMs atómicamente, persistir, emitir info.
//      f. Si fallo: persistir failed, clasificar evento.
//      g. Cerrar operation.
//
// Tier=Critical, interval=60s. ACME puede tardar 30-60s en una emisión,
// por eso el interval es ligeramente más alto — un reconciler no debe
// volver a ejecutarse mientras el anterior aún corre (el scheduler ya
// serializa los Reconcile, así que no es un problema correcto pero sí
// de overhead).
//
// IMPORTANTE: el provider concreto (SelfSigned/LetsEncrypt) no toca
// filesystem. Quien escribe fullchain.pem y privkey.pem es ESTE
// reconciler — centralizamos paths, permisos, atomic writes.

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Config
// ─────────────────────────────────────────────────────────────────────────────

// CertReconcilerConfig agrupa parámetros del constructor.
type CertReconcilerConfig struct {
	// Interval del Reconciler (cuán a menudo se ejecuta su Reconcile).
	// Default 60s. NO es la frecuencia de renovación de un cert (eso
	// depende de NotAfter); es el ritmo con el que MIRAMOS si hay
	// algún cert que necesita atención.
	Interval time.Duration

	// RenewWindow es el margen antes de NotAfter en el que iniciamos
	// renovación. Default 30 días — recomendación estándar de la
	// industria (Let's Encrypt sugiere 30, otros 45).
	RenewWindow time.Duration

	// CertsBaseDir es donde se escriben los archivos fullchain.pem y
	// privkey.pem. Cada cert vive en <base>/<id>/. Tests usan tmpdir.
	// Default /etc/ssl/nimos.
	CertsBaseDir string
}

// DefaultCertReconcilerConfig devuelve la configuración de producción.
func DefaultCertReconcilerConfig() CertReconcilerConfig {
	return CertReconcilerConfig{
		Interval:     60 * time.Second,
		RenewWindow:  30 * 24 * time.Hour,
		CertsBaseDir: "/etc/ssl/nimos",
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Reconciler
// ─────────────────────────────────────────────────────────────────────────────

// CertReconciler orquesta CertProviders y DNSChallengeProviders.
type CertReconciler struct {
	repo    *NetworkRepo
	secrets *SecretsStore
	emitter *EventEmitter
	clock   Clock
	config  CertReconcilerConfig

	// Factory de DNSChallengeProvider. El reconciler no construye los
	// challengers directamente porque necesita el token (que viene de
	// nimos_secrets, distinto por cert). Esta factory recibe el nombre
	// del provider y el token plaintext y devuelve la implementación.
	//
	// Inyectable para tests (mock que no toca DNS real).
	dnsChallengerFactory DNSChallengerFactory

	mu        sync.RWMutex
	providers map[string]CertProvider
}

// DNSChallengerFactory crea un DNSChallengeProvider concreto por nombre
// y con el token apropiado. Devuelve nil si el nombre no se reconoce.
type DNSChallengerFactory func(name, token string) (DNSChallengeProvider, error)

// NewCertReconciler construye el reconciler. Todos los args excepto
// config son obligatorios.
func NewCertReconciler(
	repo *NetworkRepo,
	secrets *SecretsStore,
	emitter *EventEmitter,
	clock Clock,
	dnsChallengerFactory DNSChallengerFactory,
	config CertReconcilerConfig,
) (*CertReconciler, error) {
	if repo == nil {
		return nil, errors.New("NewCertReconciler: repo is nil")
	}
	if secrets == nil {
		return nil, errors.New("NewCertReconciler: secrets is nil")
	}
	if emitter == nil {
		return nil, errors.New("NewCertReconciler: emitter is nil")
	}
	if dnsChallengerFactory == nil {
		return nil, errors.New("NewCertReconciler: dnsChallengerFactory is nil")
	}
	if clock == nil {
		clock = NewRealClock()
	}
	defaults := DefaultCertReconcilerConfig()
	if config.Interval == 0 {
		config.Interval = defaults.Interval
	}
	if config.RenewWindow == 0 {
		config.RenewWindow = defaults.RenewWindow
	}
	if config.CertsBaseDir == "" {
		config.CertsBaseDir = defaults.CertsBaseDir
	}

	return &CertReconciler{
		repo:                 repo,
		secrets:              secrets,
		emitter:              emitter,
		clock:                clock,
		config:               config,
		dnsChallengerFactory: dnsChallengerFactory,
		providers:            make(map[string]CertProvider),
	}, nil
}

// RegisterProvider añade un CertProvider concreto. Sobrescribe si ya existe.
func (r *CertReconciler) RegisterProvider(p CertProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
}

// ─────────────────────────────────────────────────────────────────────────────
// Reconciler interface impl
// ─────────────────────────────────────────────────────────────────────────────

func (r *CertReconciler) Name() string            { return "cert_renewer" }
func (r *CertReconciler) Tier() ReconcilerTier    { return TierCritical }
func (r *CertReconciler) Interval() time.Duration { return r.config.Interval }

// Reconcile ejecuta una pasada completa.
func (r *CertReconciler) Reconcile(ctx context.Context) error {
	all, err := r.repo.ListCerts(ctx)
	if err != nil {
		return fmt.Errorf("list certs: %w", err)
	}
	for _, c := range all {
		if !c.Enabled {
			continue
		}
		if !r.needsRenewal(c) {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.processOne(ctx, c)
	}
	return nil
}

// needsRenewal decide si un cert necesita atención en esta pasada.
func (r *CertReconciler) needsRenewal(c *NetworkCert) bool {
	// Config pendiente de aplicar.
	if c.Convergence.IsPending() {
		return true
	}
	// Sin auto_renew y ya aplicado: no tocar.
	if !c.AutoRenew {
		return false
	}
	// Auto-renew: ver si NotAfter está dentro de la ventana.
	deadline := r.clock.Now().Add(r.config.RenewWindow)
	return c.NotAfter.Before(deadline)
}

// ─────────────────────────────────────────────────────────────────────────────
// processOne
// ─────────────────────────────────────────────────────────────────────────────

// processOne ejecuta el ciclo completo para un cert.
// Errores NO se propagan — un cert fallido no debe parar al resto.
func (r *CertReconciler) processOne(ctx context.Context, c *NetworkCert) {
	// Resolver CertProvider.
	r.mu.RLock()
	provider, found := r.providers[c.CertProvider]
	r.mu.RUnlock()
	if !found {
		r.emitEvent(ctx, EventLevelWarn, c.ID, "provider_unknown",
			fmt.Sprintf("Cert provider %q not registered for %s", c.CertProvider, c.Domain))
		return
	}

	// Abrir operation.
	opID, err := r.openOperation(ctx, c)
	if err != nil {
		logMsg("cert reconciler: open operation for %s: %v", c.Domain, err)
		return
	}

	// Construir CertRequest.
	req := CertRequest{Domain: c.Domain}

	// Si el cert usa challenge, validar consistencia + crear DNS challenger.
	if c.ChallengeType != nil && *c.ChallengeType != "" {
		req.ChallengeType = *c.ChallengeType
		if !provider.SupportsChallenge(req.ChallengeType) {
			_ = r.closeOperation(ctx, opID, "failed", "CHALLENGE_UNSUPPORTED",
				fmt.Sprintf("provider %s does not support challenge %s", c.CertProvider, req.ChallengeType))
			r.emitEventOp(ctx, opID, EventLevelError, c.ID, "challenge_unsupported",
				fmt.Sprintf("Provider %s does not support %s for %s",
					c.CertProvider, req.ChallengeType, c.Domain))
			return
		}

		if req.ChallengeType == "dns-01" {
			if c.DNSProvider == nil || *c.DNSProvider == "" {
				_ = r.closeOperation(ctx, opID, "failed", "DNS_PROVIDER_MISSING", "dns_provider not set")
				r.emitEventOp(ctx, opID, EventLevelError, c.ID, "dns_provider_missing",
					fmt.Sprintf("dns-01 selected but no dns_provider configured for %s", c.Domain))
				return
			}
			challenger, err := r.buildDNSChallenger(*c.DNSProvider, c.Domain)
			if err != nil {
				_ = r.closeOperation(ctx, opID, "failed", "DNS_CHALLENGER", err.Error())
				r.emitEventOp(ctx, opID, EventLevelError, c.ID, "dns_challenger_failed",
					fmt.Sprintf("Could not build DNS challenger %s for %s: %v",
						*c.DNSProvider, c.Domain, err))
				return
			}
			req.DNSChallenger = challenger
		}
	}

	// Llamar Issue.
	material, callErr := provider.Issue(ctx, req)

	switch {
	case callErr == nil:
		r.handleSuccess(ctx, c, opID, material)
	case errors.Is(callErr, ErrCertChallengeFailed):
		r.handleChallengeFailed(ctx, c, opID, callErr)
	case errors.Is(callErr, ErrCertProviderRateLimited):
		r.handleRateLimited(ctx, c, opID, callErr)
	case errors.Is(callErr, ErrCertProviderTransient):
		r.handleTransient(ctx, c, opID, callErr)
	case errors.Is(callErr, context.Canceled), errors.Is(callErr, context.DeadlineExceeded):
		_ = r.closeOperation(ctx, opID, "failed", "CANCELLED", callErr.Error())
		// No emitimos evento — cancelación no es error real.
	default:
		r.handleOtherError(ctx, c, opID, callErr)
	}
}

// buildDNSChallenger carga el token DDNS desde nimos_secrets y construye
// el DNSChallengeProvider correspondiente. El nombre del secret se
// busca por label "ddns_token" con label "<provider>:<domain>" — misma
// convención que usa el DDNS reconciler.
func (r *CertReconciler) buildDNSChallenger(dnsProvider, domain string) (DNSChallengeProvider, error) {
	label := dnsProvider + ":" + domain
	secret, err := r.secrets.GetSecretByLabel("ddns_token", label)
	if err != nil {
		return nil, fmt.Errorf("could not find DDNS token (category=ddns_token label=%s): %w", label, err)
	}
	token := string(secret.Plaintext)
	return r.dnsChallengerFactory(dnsProvider, token)
}

// ─────────────────────────────────────────────────────────────────────────────
// Success/failure handlers
// ─────────────────────────────────────────────────────────────────────────────

func (r *CertReconciler) handleSuccess(ctx context.Context, c *NetworkCert, opID string, material *CertMaterial) {
	// Determinar paths.
	dir := filepath.Join(r.config.CertsBaseDir, c.ID)
	fullchainPath := filepath.Join(dir, "fullchain.pem")
	privkeyPath := filepath.Join(dir, "privkey.pem")

	// Escribir archivos atómicamente.
	if err := r.writeCertFiles(dir, fullchainPath, privkeyPath, material); err != nil {
		_ = r.closeOperation(ctx, opID, "failed", "FS_WRITE", err.Error())
		r.emitEventOp(ctx, opID, EventLevelError, c.ID, "write_failed",
			fmt.Sprintf("Cert issued but writing to disk failed: %v", err))
		return
	}

	// Persistir en DB.
	notBefore := time.Unix(material.NotBefore, 0).UTC()
	notAfter := time.Unix(material.NotAfter, 0).UTC()
	persistErr := r.withTx(ctx, func(tx *sql.Tx) error {
		if err := r.repo.UpdateCertRenewed(ctx, tx, c.ID, fullchainPath, privkeyPath, notBefore, notAfter); err != nil {
			return err
		}
		return r.repo.MarkCertApplied(ctx, tx, c.ID)
	})
	if persistErr != nil {
		_ = r.closeOperation(ctx, opID, "failed", "PERSIST", persistErr.Error())
		r.emitEventOp(ctx, opID, EventLevelError, c.ID, "persist_failed",
			fmt.Sprintf("Cert for %s written to disk but DB persist failed: %v", c.Domain, persistErr))
		return
	}

	// Evento de éxito: distinguir primera emisión vs renovación.
	level := EventLevelInfo
	event := "cert_renewed"
	msg := fmt.Sprintf("Cert renewed for %s (expires %s)", c.Domain, notAfter.Format("2006-01-02"))
	if c.LastRenewedAt == nil {
		event = "cert_issued"
		msg = fmt.Sprintf("Cert issued for %s (expires %s)", c.Domain, notAfter.Format("2006-01-02"))
	}
	_ = r.closeOperation(ctx, opID, "completed", "", "")
	r.emitEventOp(ctx, opID, level, c.ID, event, msg)
}

func (r *CertReconciler) handleChallengeFailed(ctx context.Context, c *NetworkCert, opID string, err error) {
	_ = r.closeOperation(ctx, opID, "failed", "CHALLENGE", err.Error())
	r.emitEventOp(ctx, opID, EventLevelError, c.ID, "challenge_failed",
		fmt.Sprintf("Cert challenge failed for %s (check DNS resolution)", c.Domain))
}

func (r *CertReconciler) handleRateLimited(ctx context.Context, c *NetworkCert, opID string, err error) {
	_ = r.closeOperation(ctx, opID, "failed", "RATE_LIMITED", err.Error())
	// Warn no error: es esperable y temporal.
	r.emitEventOp(ctx, opID, EventLevelWarn, c.ID, "rate_limited",
		fmt.Sprintf("Cert provider rate-limited for %s; will retry later", c.Domain))
}

func (r *CertReconciler) handleTransient(ctx context.Context, c *NetworkCert, opID string, err error) {
	_ = r.closeOperation(ctx, opID, "failed", "TRANSIENT", err.Error())
	r.emitEventOp(ctx, opID, EventLevelWarn, c.ID, "transient_failure",
		fmt.Sprintf("Cert provider transient failure for %s", c.Domain))
}

func (r *CertReconciler) handleOtherError(ctx context.Context, c *NetworkCert, opID string, err error) {
	_ = r.closeOperation(ctx, opID, "failed", "UNKNOWN", err.Error())
	r.emitEventOp(ctx, opID, EventLevelError, c.ID, "issue_failed",
		fmt.Sprintf("Cert issuance failed for %s: %v", c.Domain, err))
}

// ─────────────────────────────────────────────────────────────────────────────
// Filesystem
// ─────────────────────────────────────────────────────────────────────────────

// writeCertFiles escribe fullchain y privkey atómicamente.
//
// Estrategia:
//   1. Crear directorio con 0o755 (fullchain debe ser legible por
//      nginx/usuario web; privkey lleva su propio chmod 0600).
//   2. Escribir a <path>.tmp con permisos finales.
//   3. Renombrar (atomic on POSIX).
//   4. Verificar permisos finales (defensive).
//
// Si fullchain se renombra OK pero privkey falla, el cert queda en
// estado inconsistente (cert nuevo + key vieja). NO restauramos el
// fullchain anterior porque podría ser legítimo (e.g. usuario corrigió
// algo). El próximo run renovará y dejará todo consistente.
func (r *CertReconciler) writeCertFiles(dir, fullchainPath, privkeyPath string, material *CertMaterial) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// Fullchain: chmod 0644 (legible por todos).
	if err := atomicWriteFile(fullchainPath, material.FullchainPEM, 0o644); err != nil {
		return fmt.Errorf("write fullchain: %w", err)
	}

	// Privkey: chmod 0600 (solo root).
	if err := atomicWriteFile(privkeyPath, material.PrivkeyPEM, 0o600); err != nil {
		return fmt.Errorf("write privkey: %w", err)
	}
	return nil
}

// atomicWriteFile escribe data a path atómicamente:
//   1. Crea path.tmp con los permisos pedidos.
//   2. Rename a path.
//   3. Re-chmod defensivo por si umask.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Re-chmod por si umask interfirió (WriteFile aplica umask).
	_ = os.Chmod(path, perm)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Operations + Events helpers
// ─────────────────────────────────────────────────────────────────────────────

func (r *CertReconciler) openOperation(ctx context.Context, c *NetworkCert) (string, error) {
	op := &NetworkOperation{
		Type:        "cert_renew",
		TargetID:    &c.ID,
		Status:      "in_progress",
		TriggeredBy: "reconciler:cert_renewer",
	}
	if err := r.withTx(ctx, func(tx *sql.Tx) error {
		return r.repo.CreateOperation(ctx, tx, op)
	}); err != nil {
		return "", err
	}
	return op.ID, nil
}

func (r *CertReconciler) closeOperation(ctx context.Context, opID, status, errCode, errMsg string) error {
	var codePtr, msgPtr *string
	if errCode != "" {
		codePtr = &errCode
	}
	if errMsg != "" {
		msgPtr = &errMsg
	}
	return r.withTx(ctx, func(tx *sql.Tx) error {
		return r.repo.UpdateOperationStatus(ctx, tx, opID, status, codePtr, msgPtr)
	})
}

func (r *CertReconciler) emitEvent(ctx context.Context, level EventLevel, targetID, event, message string) {
	r.emitEventOp(ctx, "", level, targetID, event, message)
}

func (r *CertReconciler) emitEventOp(ctx context.Context, opID string, level EventLevel, targetID, event, message string) {
	in := EventInput{
		Category: CategoryCert,
		Event:    event,
		TargetID: &targetID,
		Level:    level,
		Message:  message,
	}
	if opID != "" {
		in.OperationID = &opID
	}
	if _, err := r.emitter.Emit(ctx, in); err != nil {
		logMsg("cert reconciler: emit event %s: %v", event, err)
	}
}

func (r *CertReconciler) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := r.repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
