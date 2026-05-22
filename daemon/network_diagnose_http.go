// network_diagnose_http.go — Endpoint de diagnóstico para preparar
// emisión de certificados.
//
// Contract:
//
//   GET /api/v4/network/diagnose/cert?domain=X&cert_provider=Y&challenge_type=Z&dns_provider=W
//
//   → Devuelve checklist de checks, cada uno con status (ok/warn/fail)
//     y hint específico.
//
// Filosofía:
//
//   - Los checks reflejan EL STACK REAL DE NimOS v4, no recetas
//     genéricas. NimOS usa golang.org/x/crypto/acme (no certbot),
//     soporta solo DNS-01 (no http-01), y el reconciler resuelve
//     providers vía nombre. Por tanto los checks son:
//
//     · "cert_provider está registrado en el cert reconciler"
//     · "challenge_type es soportado por ese provider"
//     · "dns_provider tiene factory en el cert reconciler"
//     · "ACME account key cargable"
//     · "DDNS token existe en nimos_secrets para ese (provider, domain)"
//     · "openssl_installed y dig_installed según capabilities"
//
//   - El diagnose NO hace llamadas HTTP a Let's Encrypt ni resuelve
//     DNS. Es offline y rápido — su valor es predecir fallos ANTES de
//     gastar rate limits de ACME, no validar la emisión en sí.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// ─────────────────────────────────────────────────────────────────────────────
// Tipos
// ─────────────────────────────────────────────────────────────────────────────

// DiagnoseCheckStatus es uno de:
//   - "ok":   el check pasó.
//   - "warn": el check sugiere un problema potencial pero no bloquea.
//   - "fail": el check identifica un problema que sí bloquea.
type DiagnoseCheckStatus string

const (
	CheckStatusOK   DiagnoseCheckStatus = "ok"
	CheckStatusWarn DiagnoseCheckStatus = "warn"
	CheckStatusFail DiagnoseCheckStatus = "fail"
)

// DiagnoseCheck representa un check individual del checklist.
type DiagnoseCheck struct {
	ID     string              `json:"id"`     // identificador estable para el frontend
	Label  string              `json:"label"`  // descripción legible
	Status DiagnoseCheckStatus `json:"status"`
	Detail string              `json:"detail,omitempty"` // contexto adicional
	Hint   string              `json:"hint,omitempty"`   // acción sugerida si status != ok
}

// DiagnoseCertResponse es la respuesta completa.
type DiagnoseCertResponse struct {
	Domain        string          `json:"domain"`
	CertProvider  string          `json:"cert_provider"`
	ChallengeType string          `json:"challenge_type,omitempty"`
	DNSProvider   string          `json:"dns_provider,omitempty"`

	// OverallStatus resume el conjunto:
	//   - "ok":   ningún check en fail.
	//   - "warn": al menos un warn pero ningún fail.
	//   - "fail": al menos un check en fail.
	OverallStatus DiagnoseCheckStatus `json:"overall_status"`
	Checks        []DiagnoseCheck     `json:"checks"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Routing
// ─────────────────────────────────────────────────────────────────────────────

func handleNetworkDiagnoseRoutes(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v4/network/diagnose/cert" {
		jsonError(w, http.StatusNotFound, "Not found")
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	diagnoseCertHTTP(w, r)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/v4/network/diagnose/cert
// ─────────────────────────────────────────────────────────────────────────────

func diagnoseCertHTTP(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	if networkCertReconciler == nil || networkSecretsStore == nil || networkCapabilities == nil {
		jsonError(w, http.StatusServiceUnavailable, "Network module not initialized")
		return
	}

	// Parse query params.
	q := r.URL.Query()
	domain := q.Get("domain")
	certProvider := q.Get("cert_provider")
	challengeType := q.Get("challenge_type")
	dnsProvider := q.Get("dns_provider")

	// Validación mínima de inputs (no exhaustiva: el diagnose es
	// best-effort, no API estricta).
	if domain == "" {
		jsonError(w, http.StatusBadRequest, "domain query param is required")
		return
	}
	if certProvider == "" {
		jsonError(w, http.StatusBadRequest, "cert_provider query param is required")
		return
	}
	if err := validateDomain(domain); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	res := DiagnoseCertResponse{
		Domain:        domain,
		CertProvider:  certProvider,
		ChallengeType: challengeType,
		DNSProvider:   dnsProvider,
		Checks:        make([]DiagnoseCheck, 0, 8),
	}

	// ── Check 1: cert_provider registrado en el reconciler.
	res.Checks = append(res.Checks, checkCertProviderRegistered(certProvider))

	// ── Check 2: si hay challenge_type, debe ser soportado por el provider.
	if challengeType != "" {
		res.Checks = append(res.Checks, checkChallengeSupported(certProvider, challengeType))
	}

	// ── Check 3: si dns-01, debe haber dns_provider y un token DDNS.
	if challengeType == "dns-01" {
		res.Checks = append(res.Checks, checkDNSProviderSpecified(dnsProvider))
		if dnsProvider != "" {
			res.Checks = append(res.Checks, checkDDNSTokenExists(dnsProvider, domain))
		}
	}

	// ── Check 4: ACME account key existe si el provider es ACME.
	if isACMEProvider(certProvider) {
		res.Checks = append(res.Checks, checkACMEAccountKey())
	}

	// ── Check 5: capabilities útiles para troubleshooting (no bloqueantes).
	caps, _ := networkCapabilities.Get(MaxCapabilitiesAge)
	if caps != nil {
		res.Checks = append(res.Checks, checkCapabilityOpenSSL(*caps))
		if challengeType == "dns-01" {
			res.Checks = append(res.Checks, checkCapabilityDig(*caps))
		}
	}

	// Overall status.
	res.OverallStatus = aggregateStatus(res.Checks)

	w.Header().Set("Cache-Control", "no-cache")
	json.NewEncoder(w).Encode(res)
}

// ─────────────────────────────────────────────────────────────────────────────
// Checks individuales
// ─────────────────────────────────────────────────────────────────────────────

func checkCertProviderRegistered(name string) DiagnoseCheck {
	c := DiagnoseCheck{
		ID:    "cert_provider_registered",
		Label: fmt.Sprintf("Cert provider %q is registered", name),
	}
	networkCertReconciler.mu.RLock()
	_, ok := networkCertReconciler.providers[name]
	networkCertReconciler.mu.RUnlock()
	if ok {
		c.Status = CheckStatusOK
		return c
	}
	c.Status = CheckStatusFail
	c.Detail = fmt.Sprintf("provider %q not found in cert reconciler", name)
	c.Hint = "The cert provider name is not registered. Check spelling (e.g. 'letsencrypt_staging', 'letsencrypt', 'selfsigned')."
	return c
}

func checkChallengeSupported(providerName, challenge string) DiagnoseCheck {
	c := DiagnoseCheck{
		ID:    "challenge_supported",
		Label: fmt.Sprintf("Challenge type %q is supported by provider", challenge),
	}
	networkCertReconciler.mu.RLock()
	provider, ok := networkCertReconciler.providers[providerName]
	networkCertReconciler.mu.RUnlock()
	if !ok {
		// El check anterior ya falla; aquí marcamos warn por dependencia.
		c.Status = CheckStatusWarn
		c.Detail = "provider not registered, cannot test challenge support"
		return c
	}
	if provider.SupportsChallenge(challenge) {
		c.Status = CheckStatusOK
		return c
	}
	c.Status = CheckStatusFail
	c.Detail = fmt.Sprintf("provider %s does not support %s", providerName, challenge)
	switch providerName {
	case "selfsigned":
		c.Hint = "Self-signed provider does not use challenges. Omit challenge_type."
	default:
		c.Hint = "Currently only 'dns-01' is supported for ACME providers."
	}
	return c
}

func checkDNSProviderSpecified(dnsProvider string) DiagnoseCheck {
	c := DiagnoseCheck{
		ID:    "dns_provider_set",
		Label: "dns_provider is specified for dns-01 challenge",
	}
	if dnsProvider != "" {
		c.Status = CheckStatusOK
		return c
	}
	c.Status = CheckStatusFail
	c.Detail = "dns-01 challenge requires a dns_provider"
	c.Hint = "Set dns_provider (e.g. 'duckdns')."
	return c
}

func checkDDNSTokenExists(dnsProvider, domain string) DiagnoseCheck {
	c := DiagnoseCheck{
		ID:    "ddns_token_exists",
		Label: fmt.Sprintf("DDNS token exists for %s:%s", dnsProvider, domain),
	}
	label := dnsProvider + ":" + domain
	if _, err := networkSecretsStore.GetSecretByLabel("ddns_token", label); err != nil {
		c.Status = CheckStatusFail
		c.Detail = fmt.Sprintf("no DDNS token found with label %q", label)
		c.Hint = fmt.Sprintf("Create a DDNS entry for %s with provider %s before issuing the cert (POST /api/v4/network/ddns).",
			domain, dnsProvider)
		return c
	}
	c.Status = CheckStatusOK
	return c
}

// isACMEProvider devuelve true si el nombre corresponde a un provider
// que usa ACME y por tanto necesita account key.
func isACMEProvider(name string) bool {
	switch name {
	case "letsencrypt", "letsencrypt_staging":
		return true
	default:
		return false
	}
}

func checkACMEAccountKey() DiagnoseCheck {
	c := DiagnoseCheck{
		ID:    "acme_account_key_loadable",
		Label: "ACME account key is loadable",
	}
	if _, err := os.Stat(DefaultACMEAccountKeyPath); err != nil {
		if os.IsNotExist(err) {
			c.Status = CheckStatusWarn
			c.Detail = fmt.Sprintf("ACME account key file %s does not exist yet", DefaultACMEAccountKeyPath)
			c.Hint = "The key will be created automatically on first ACME use. No action required."
			return c
		}
		c.Status = CheckStatusFail
		c.Detail = fmt.Sprintf("could not stat %s: %v", DefaultACMEAccountKeyPath, err)
		c.Hint = "Check that the daemon has read access to the ACME key directory."
		return c
	}
	// Intentar parsear; si falla, fail crítico (la cuenta está corrupta).
	if _, err := LoadOrCreateACMEAccountKey(DefaultACMEAccountKeyPath); err != nil {
		c.Status = CheckStatusFail
		c.Detail = fmt.Sprintf("could not load existing key: %v", err)
		c.Hint = "The ACME account key file exists but cannot be parsed. Inspect it manually or delete to regenerate (you lose registration history)."
		return c
	}
	c.Status = CheckStatusOK
	return c
}

func checkCapabilityOpenSSL(caps SystemCapabilities) DiagnoseCheck {
	c := DiagnoseCheck{
		ID:    "capability_openssl",
		Label: "openssl available for cert inspection",
	}
	if caps.OpenSSLInstalled {
		c.Status = CheckStatusOK
		return c
	}
	c.Status = CheckStatusWarn
	c.Detail = "openssl binary not found"
	c.Hint = "Without openssl, manual cert troubleshooting is harder. Install: `apt install openssl`."
	return c
}

func checkCapabilityDig(caps SystemCapabilities) DiagnoseCheck {
	c := DiagnoseCheck{
		ID:    "capability_dig",
		Label: "dig available for DNS propagation checks",
	}
	if caps.DigInstalled {
		c.Status = CheckStatusOK
		return c
	}
	c.Status = CheckStatusWarn
	c.Detail = "dig binary not found"
	c.Hint = "Without dig, you cannot manually verify DNS-01 challenge propagation. Install: `apt install dnsutils`."
	return c
}

// ─────────────────────────────────────────────────────────────────────────────
// Aggregation
// ─────────────────────────────────────────────────────────────────────────────

// aggregateStatus resume el conjunto:
//   - cualquier fail → fail.
//   - sin fails pero alguno warn → warn.
//   - todos ok → ok.
//   - vacío → ok (caso degenerado).
func aggregateStatus(checks []DiagnoseCheck) DiagnoseCheckStatus {
	hasWarn := false
	for _, c := range checks {
		switch c.Status {
		case CheckStatusFail:
			return CheckStatusFail
		case CheckStatusWarn:
			hasWarn = true
		}
	}
	if hasWarn {
		return CheckStatusWarn
	}
	return CheckStatusOK
}
