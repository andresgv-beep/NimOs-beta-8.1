// network_certs_http.go — HTTP handlers para /api/v4/network/certs.
//
// Endpoint contract:
//
//   GET    /api/v4/network/certs           — lista
//   POST   /api/v4/network/certs           — crea config (NO emite el cert)
//   GET    /api/v4/network/certs/:id       — detalle
//   PUT    /api/v4/network/certs/:id       — actualiza enabled/auto_renew
//   DELETE /api/v4/network/certs/:id       — borra de DB (PEMs en disco se preservan)
//
// Diseño:
//
//   - POST NO emite el certificado inmediatamente. Persiste la config
//     con applied=0 (pending) y el CertReconciler lo recoge en su
//     próxima pasada. Razón: ACME puede tardar 30-60s; un POST que
//     bloquea ese tiempo es mala UX y vulnerable a timeouts del cliente.
//     El cliente puede poll-ear el GET para ver cuando applied==desired.
//
//   - DELETE solo borra la entrada de DB. Los archivos fullchain.pem y
//     privkey.pem en disco NO se tocan — pueden estar en uso por
//     nginx o por otro proceso. Si el admin quiere borrarlos, lo hace
//     manualmente o vía un endpoint futuro `?delete_files=true`.
//
//   - Validación: provider/challenge/dns_provider deben ser
//     coherentes con el CHECK constraint del schema:
//       challenge=NULL     ⇒ dns_provider=NULL  (selfsigned)
//       challenge=http-01  ⇒ dns_provider=NULL
//       challenge=dns-01   ⇒ dns_provider!=NULL

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Routing
// ─────────────────────────────────────────────────────────────────────────────

var certsItemRegex = regexp.MustCompile(`^/api/v4/network/certs/([^/]+)$`)

// handleNetworkCertsRoutes dispatcher.
func handleNetworkCertsRoutes(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	method := r.Method

	if path == "/api/v4/network/certs" {
		switch method {
		case http.MethodGet:
			certsListHTTP(w, r)
		case http.MethodPost:
			certsCreateHTTP(w, r)
		default:
			jsonError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	matches := certsItemRegex.FindStringSubmatch(path)
	if matches == nil {
		jsonError(w, http.StatusNotFound, "Not found")
		return
	}
	id := matches[1]

	switch method {
	case http.MethodGet:
		certsGetHTTP(w, r, id)
	case http.MethodPut:
		certsUpdateHTTP(w, r, id)
	case http.MethodDelete:
		certsDeleteHTTP(w, r, id)
	default:
		jsonError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// View
// ─────────────────────────────────────────────────────────────────────────────

type certView struct {
	ID            string  `json:"id"`
	Domain        string  `json:"domain"`
	CertProvider  string  `json:"cert_provider"`
	ChallengeType *string `json:"challenge_type,omitempty"`
	DNSProvider   *string `json:"dns_provider,omitempty"`
	FullchainPath string  `json:"fullchain_path"`
	PrivkeyPath   string  `json:"privkey_path"`
	NotBefore     string  `json:"not_before"`
	NotAfter      string  `json:"not_after"`
	Enabled       bool    `json:"enabled"`
	AutoRenew     bool    `json:"auto_renew"`
	IssuedAt      string  `json:"issued_at"`
	LastRenewedAt *string `json:"last_renewed_at,omitempty"`

	Status   string `json:"status"`
	Desired  int64  `json:"desired_generation"`
	Observed int64  `json:"observed_generation"`
	Applied  int64  `json:"applied_generation"`
}

func certToView(c *NetworkCert) certView {
	status := "converged"
	if c.Convergence.IsPending() {
		status = "pending"
	} else if c.Convergence.HasDrifted() {
		status = "drifted"
	}
	v := certView{
		ID:            c.ID,
		Domain:        c.Domain,
		CertProvider:  c.CertProvider,
		ChallengeType: c.ChallengeType,
		DNSProvider:   c.DNSProvider,
		FullchainPath: c.FullchainPath,
		PrivkeyPath:   c.PrivkeyPath,
		NotBefore:     c.NotBefore.UTC().Format("2006-01-02T15:04:05Z"),
		NotAfter:      c.NotAfter.UTC().Format("2006-01-02T15:04:05Z"),
		Enabled:       c.Enabled,
		AutoRenew:     c.AutoRenew,
		IssuedAt:      c.IssuedAt.UTC().Format("2006-01-02T15:04:05Z"),
		Status:        status,
		Desired:       c.Convergence.Desired,
		Observed:      c.Convergence.Observed,
		Applied:       c.Convergence.Applied,
	}
	if c.LastRenewedAt != nil {
		s := c.LastRenewedAt.UTC().Format("2006-01-02T15:04:05Z")
		v.LastRenewedAt = &s
	}
	return v
}

// ─────────────────────────────────────────────────────────────────────────────
// Validation
// ─────────────────────────────────────────────────────────────────────────────

// validateCertCreate aplica las reglas de coherencia que el schema CHECK
// también impone. Las validamos también aquí para devolver 400 con
// mensaje legible en lugar de 500 con "CHECK constraint failed".
func validateCertCreate(req *certCreateRequest) error {
	if req.CertProvider == "" {
		return errors.New("cert_provider is required")
	}
	if err := validateDomain(req.Domain); err != nil {
		return err
	}

	switch req.CertProvider {
	case "selfsigned":
		// challenge_type y dns_provider deben ser nil/vacío.
		if req.ChallengeType != "" {
			return errors.New("selfsigned does not use challenges (challenge_type must be empty)")
		}
		if req.DNSProvider != "" {
			return errors.New("selfsigned does not use dns_provider")
		}
	case "letsencrypt", "letsencrypt_staging", "zerossl":
		// challenge_type es obligatorio.
		if req.ChallengeType == "" {
			return fmt.Errorf("%s requires challenge_type", req.CertProvider)
		}
		switch req.ChallengeType {
		case "http-01":
			if req.DNSProvider != "" {
				return errors.New("http-01 does not need dns_provider")
			}
		case "dns-01":
			if req.DNSProvider == "" {
				return errors.New("dns-01 requires dns_provider")
			}
		default:
			return fmt.Errorf("invalid challenge_type %q (expected http-01 | dns-01)", req.ChallengeType)
		}
	default:
		return fmt.Errorf("invalid cert_provider %q", req.CertProvider)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/v4/network/certs
// ─────────────────────────────────────────────────────────────────────────────

func certsListHTTP(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	if networkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "Network module not initialized")
		return
	}

	all, err := networkRepo.ListCerts(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "Failed to list certs: "+err.Error())
		return
	}
	views := make([]certView, 0, len(all))
	for _, c := range all {
		views = append(views, certToView(c))
	}
	jsonOk(w, map[string]interface{}{"certs": views})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/v4/network/certs
// ─────────────────────────────────────────────────────────────────────────────

type certCreateRequest struct {
	Domain        string `json:"domain"`
	CertProvider  string `json:"cert_provider"`
	ChallengeType string `json:"challenge_type"`
	DNSProvider   string `json:"dns_provider"`
	Enabled       *bool  `json:"enabled"`
	AutoRenew     *bool  `json:"auto_renew"`
}

func certsCreateHTTP(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	if networkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "Network module not initialized")
		return
	}

	var req certCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if err := validateCertCreate(&req); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Defaults.
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	autoRenew := true
	if req.AutoRenew != nil {
		autoRenew = *req.AutoRenew
	}

	// El cert aún NO existe en disco — el reconciler lo emitirá.
	// Para la fila inicial usamos placeholder paths que serán reemplazados
	// cuando el reconciler haga UpdateCertRenewed con paths finales.
	// IssuedAt = clock.Now (orientativo; UpdateCertRenewed lo actualiza
	// con el NotBefore real al emitir).
	now := networkRepo.clock.Now().UTC()
	domain := strings.TrimSpace(req.Domain)

	cert := &NetworkCert{
		Domain:       domain,
		CertProvider: req.CertProvider,
		// Paths placeholder — el reconciler los sobrescribe al emitir.
		FullchainPath: fmt.Sprintf("/etc/ssl/nimos/pending/%s/fullchain.pem", domain),
		PrivkeyPath:   fmt.Sprintf("/etc/ssl/nimos/pending/%s/privkey.pem", domain),
		// NotBefore/NotAfter placeholder; el reconciler los actualiza al
		// emitir el cert real.
		NotBefore: now,
		NotAfter:  now, // no es válido todavía — el reconciler lo arregla.
		Enabled:   enabled,
		AutoRenew: autoRenew,
		IssuedAt:  now,
	}
	if req.ChallengeType != "" {
		ct := req.ChallengeType
		cert.ChallengeType = &ct
	}
	if req.DNSProvider != "" {
		dp := req.DNSProvider
		cert.DNSProvider = &dp
	}

	err := certsWithTx(r.Context(), func(tx *sql.Tx) error {
		return networkRepo.CreateCert(r.Context(), tx, cert)
	})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			jsonError(w, http.StatusConflict, "cert for this domain already exists")
			return
		}
		if strings.Contains(err.Error(), "CHECK constraint failed") {
			jsonError(w, http.StatusBadRequest, "cert config violates schema constraints: "+err.Error())
			return
		}
		jsonError(w, http.StatusInternalServerError, "Failed to create cert: "+err.Error())
		return
	}

	certsEmitAudit(r.Context(), cert.ID, "created",
		fmt.Sprintf("Cert config created for %s (provider=%s) by user:%s",
			cert.Domain, cert.CertProvider, session.Username))

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(certToView(cert))
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/v4/network/certs/:id
// ─────────────────────────────────────────────────────────────────────────────

func certsGetHTTP(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	if networkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "Network module not initialized")
		return
	}
	c, err := networkRepo.GetCert(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrCertNotFound) {
			jsonError(w, http.StatusNotFound, "Cert not found: "+id)
			return
		}
		jsonError(w, http.StatusInternalServerError, "Failed to get cert: "+err.Error())
		return
	}
	jsonOk(w, certToView(c))
}

// ─────────────────────────────────────────────────────────────────────────────
// PUT /api/v4/network/certs/:id
// ─────────────────────────────────────────────────────────────────────────────

// certUpdateRequest representa los campos actualizables de un cert.
// challenge_type / dns_provider / cert_provider NO se pueden cambiar
// post-create (cambiaría toda la lógica de emisión). Si el usuario
// quiere cambiar provider → borrar y crear de nuevo.
type certUpdateRequest struct {
	Enabled   *bool `json:"enabled"`
	AutoRenew *bool `json:"auto_renew"`
}

func certsUpdateHTTP(w http.ResponseWriter, r *http.Request, id string) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	if networkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "Network module not initialized")
		return
	}

	var req certUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if req.Enabled == nil && req.AutoRenew == nil {
		jsonError(w, http.StatusBadRequest, "at least one of enabled/auto_renew must be set")
		return
	}

	// Composición de updates en una tx. Usamos los métodos existentes
	// del repo (SetCertAutoRenew) y un UPDATE inline para enabled (no
	// hay método dedicado pero un UPDATE explícito mantiene la simetría
	// y no añade nuevo método al repo solo por esto).
	err := certsWithTx(r.Context(), func(tx *sql.Tx) error {
		// enabled
		if req.Enabled != nil {
			res, err := tx.ExecContext(r.Context(), `
				UPDATE network_certs
				SET enabled = ?,
				    desired_generation = desired_generation + 1
				WHERE id = ?
			`, intFromBool(*req.Enabled), id)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				return ErrCertNotFound
			}
		}
		// auto_renew
		if req.AutoRenew != nil {
			if err := networkRepo.SetCertAutoRenew(r.Context(), tx, id, *req.AutoRenew); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrCertNotFound) {
			jsonError(w, http.StatusNotFound, "Cert not found: "+id)
			return
		}
		jsonError(w, http.StatusInternalServerError, "Failed to update cert: "+err.Error())
		return
	}

	certsEmitAudit(r.Context(), id, "config_updated",
		fmt.Sprintf("Cert %s config updated by user:%s", id, session.Username))

	c, _ := networkRepo.GetCert(r.Context(), id)
	jsonOk(w, certToView(c))
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /api/v4/network/certs/:id
// ─────────────────────────────────────────────────────────────────────────────

func certsDeleteHTTP(w http.ResponseWriter, r *http.Request, id string) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	if networkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "Network module not initialized")
		return
	}

	// Comprobar existencia antes de borrar: el repo.DeleteCert no
	// distingue "no encontrado" de "borrado", y queremos devolver 404
	// para mantener el contrato HTTP REST estándar.
	if _, err := networkRepo.GetCert(r.Context(), id); err != nil {
		if errors.Is(err, ErrCertNotFound) {
			jsonError(w, http.StatusNotFound, "Cert not found: "+id)
			return
		}
		jsonError(w, http.StatusInternalServerError, "Failed to read cert: "+err.Error())
		return
	}

	err := certsWithTx(r.Context(), func(tx *sql.Tx) error {
		return networkRepo.DeleteCert(r.Context(), tx, id)
	})
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "Failed to delete cert: "+err.Error())
		return
	}

	// NOTA explícita: NO borramos los PEMs del disco. Pueden estar en
	// uso por nginx o por otro proceso. Si el admin quiere borrarlos,
	// lo hace manualmente o vía un endpoint futuro con ?delete_files=true.
	certsEmitAudit(r.Context(), id, "deleted",
		fmt.Sprintf("Cert %s deleted by user:%s (PEM files preserved on disk)",
			id, session.Username))

	w.WriteHeader(http.StatusNoContent)
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func certsWithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
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

func certsEmitAudit(ctx context.Context, id, event, message string) {
	if networkEventEmitter == nil {
		return
	}
	targetID := id
	if _, err := networkEventEmitter.Emit(ctx, EventInput{
		Category: CategoryCert,
		Event:    event,
		TargetID: &targetID,
		Level:    EventLevelInfo,
		Message:  message,
	}); err != nil {
		logMsg("certs audit emit %s: %v", event, err)
	}
}
