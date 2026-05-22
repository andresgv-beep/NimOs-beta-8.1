// network_diagnose_http_test.go — Tests del endpoint /diagnose/cert.

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Setup
// ─────────────────────────────────────────────────────────────────────────────

var diagnoseHTTPTestMu sync.Mutex

func setupDiagnoseHTTPTest(t *testing.T) (token string, c *sqlConn, cleanup func()) {
	t.Helper()
	diagnoseHTTPTestMu.Lock()

	prevDB := db
	prevRepo := networkRepo
	prevEmitter := networkEventEmitter
	prevSecrets := networkSecretsStore
	prevCaps := networkCapabilities
	prevCertRec := networkCertReconciler

	c, dbCleanup := setupNetworkDB(t)
	db = c.db

	clock := NewFakeClock(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	networkRepo = NewNetworkRepo(c.db, clock)
	networkEventEmitter = NewEventEmitter(c.db, clock, DefaultEventEmitterConfig())

	key := make([]byte, masterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatal(err)
	}
	store, err := NewSecretsStoreWithKey(c.db, key, clock)
	if err != nil {
		t.Fatal(err)
	}
	networkSecretsStore = store

	capsStore, err := NewCapabilitiesStore(c.db, clock, func() SystemCapabilities {
		// Detect mock: openssl y dig instalados.
		return SystemCapabilities{
			OpenSSLInstalled: true,
			DigInstalled:     true,
			DetectedAt:       clock.Now(),
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	networkCapabilities = capsStore

	// Cert reconciler con selfsigned registrado por defecto.
	certRec, err := NewCertReconciler(networkRepo, store, networkEventEmitter,
		clock,
		func(name, token string) (DNSChallengeProvider, error) {
			return &mockDNSChallenger{name: name}, nil
		},
		CertReconcilerConfig{CertsBaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	certRec.RegisterProvider(NewSelfSignedProvider(SelfSignedProviderConfig{Clock: clock}))
	// Mock provider que SOLO soporta dns-01 (para tests).
	certRec.RegisterProvider(&mockCertProvider{
		name:              "letsencrypt_staging",
		supportsChallenge: map[string]bool{"dns-01": true},
	})
	networkCertReconciler = certRec

	// Sesiones.
	if _, err := c.db.Exec(`
		CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY,
			username TEXT NOT NULL,
			role TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			ip TEXT
		)
	`); err != nil {
		t.Fatal(err)
	}
	tokenBytes := make([]byte, 16)
	rand.Read(tokenBytes)
	token = hex.EncodeToString(tokenBytes)
	hashed := sha256Hex(token)
	c.db.Exec(`INSERT INTO sessions (token, username, role, created_at, expires_at, ip)
		VALUES (?, 'test-admin', 'admin', ?, ?, '127.0.0.1')`,
		hashed, time.Now().UnixMilli(), time.Now().Add(time.Hour).UnixMilli())

	cleanup = func() {
		networkCertReconciler = prevCertRec
		networkCapabilities = prevCaps
		networkSecretsStore = prevSecrets
		networkEventEmitter = prevEmitter
		networkRepo = prevRepo
		db = prevDB
		dbCleanup()
		diagnoseHTTPTestMu.Unlock()
	}
	return token, c, cleanup
}

func doDiagnoseReq(t *testing.T, token, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v4/network/diagnose/cert?"+query, strings.NewReader(""))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	handleNetworkDiagnoseRoutes(rr, req)
	return rr
}

func decodeDiagnoseBody(t *testing.T, rr *httptest.ResponseRecorder) DiagnoseCertResponse {
	t.Helper()
	var out DiagnoseCertResponse
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v raw=%q", err, rr.Body.String())
	}
	return out
}

// findCheck busca un check por ID. Devuelve nil si no aparece.
func findCheck(checks []DiagnoseCheck, id string) *DiagnoseCheck {
	for i := range checks {
		if checks[i].ID == id {
			return &checks[i]
		}
	}
	return nil
}

// ═════════════════════════════════════════════════════════════════════════════
// Aggregation function
// ═════════════════════════════════════════════════════════════════════════════

func TestAggregateStatus_Cases(t *testing.T) {
	cases := []struct {
		name   string
		checks []DiagnoseCheck
		want   DiagnoseCheckStatus
	}{
		{"empty", nil, CheckStatusOK},
		{"all ok", []DiagnoseCheck{{Status: CheckStatusOK}, {Status: CheckStatusOK}}, CheckStatusOK},
		{"one warn", []DiagnoseCheck{{Status: CheckStatusOK}, {Status: CheckStatusWarn}}, CheckStatusWarn},
		{"one fail", []DiagnoseCheck{{Status: CheckStatusOK}, {Status: CheckStatusFail}}, CheckStatusFail},
		{"warn and fail", []DiagnoseCheck{{Status: CheckStatusWarn}, {Status: CheckStatusFail}}, CheckStatusFail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := aggregateStatus(c.checks); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Routing & validation
// ═════════════════════════════════════════════════════════════════════════════

func TestDiagnoseHTTP_RequiresAuth(t *testing.T) {
	_, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	rr := doDiagnoseReq(t, "", "domain=test.example.com&cert_provider=selfsigned")
	if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusForbidden {
		t.Errorf("status=%d, want 401/403", rr.Code)
	}
}

func TestDiagnoseHTTP_MethodNotAllowed(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/api/v4/network/diagnose/cert", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handleNetworkDiagnoseRoutes(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d, want 405", rr.Code)
	}
}

func TestDiagnoseHTTP_UnknownPath(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/v4/network/diagnose/ports", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handleNetworkDiagnoseRoutes(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", rr.Code)
	}
}

func TestDiagnoseHTTP_RequiresDomain(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	rr := doDiagnoseReq(t, token, "cert_provider=selfsigned")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rr.Code)
	}
}

func TestDiagnoseHTTP_RequiresCertProvider(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	rr := doDiagnoseReq(t, token, "domain=test.example.com")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rr.Code)
	}
}

func TestDiagnoseHTTP_RejectsBadDomain(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	rr := doDiagnoseReq(t, token, "domain=has%20space&cert_provider=selfsigned")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rr.Code)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Provider registered check
// ═════════════════════════════════════════════════════════════════════════════

func TestDiagnoseHTTP_ProviderRegistered_OK(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	rr := doDiagnoseReq(t, token, "domain=test.example.com&cert_provider=selfsigned")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	res := decodeDiagnoseBody(t, rr)
	chk := findCheck(res.Checks, "cert_provider_registered")
	if chk == nil || chk.Status != CheckStatusOK {
		t.Errorf("expected ok for selfsigned; got %+v", chk)
	}
}

func TestDiagnoseHTTP_ProviderRegistered_Fail(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	rr := doDiagnoseReq(t, token, "domain=test.example.com&cert_provider=notregistered")
	res := decodeDiagnoseBody(t, rr)
	chk := findCheck(res.Checks, "cert_provider_registered")
	if chk == nil || chk.Status != CheckStatusFail {
		t.Errorf("expected fail; got %+v", chk)
	}
	if chk.Hint == "" {
		t.Error("expected hint for unregistered provider")
	}
	if res.OverallStatus != CheckStatusFail {
		t.Errorf("OverallStatus = %v, want fail", res.OverallStatus)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Challenge support check
// ═════════════════════════════════════════════════════════════════════════════

func TestDiagnoseHTTP_ChallengeSupported_OK(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	rr := doDiagnoseReq(t, token,
		"domain=test.example.com&cert_provider=letsencrypt_staging&challenge_type=dns-01&dns_provider=duckdns")
	res := decodeDiagnoseBody(t, rr)
	chk := findCheck(res.Checks, "challenge_supported")
	if chk == nil || chk.Status != CheckStatusOK {
		t.Errorf("expected ok; got %+v", chk)
	}
}

func TestDiagnoseHTTP_ChallengeOnSelfSigned_Fail(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	rr := doDiagnoseReq(t, token,
		"domain=test.example.com&cert_provider=selfsigned&challenge_type=http-01")
	res := decodeDiagnoseBody(t, rr)
	chk := findCheck(res.Checks, "challenge_supported")
	if chk == nil || chk.Status != CheckStatusFail {
		t.Errorf("expected fail; got %+v", chk)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// DDNS token check
// ═════════════════════════════════════════════════════════════════════════════

func TestDiagnoseHTTP_DDNSToken_Missing(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	rr := doDiagnoseReq(t, token,
		"domain=test.example.com&cert_provider=letsencrypt_staging&challenge_type=dns-01&dns_provider=duckdns")
	res := decodeDiagnoseBody(t, rr)
	chk := findCheck(res.Checks, "ddns_token_exists")
	if chk == nil || chk.Status != CheckStatusFail {
		t.Errorf("expected fail; got %+v", chk)
	}
	if chk.Hint == "" {
		t.Error("expected hint about creating DDNS entry")
	}
}

func TestDiagnoseHTTP_DDNSToken_Present(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	// Sembrar token.
	if _, err := networkSecretsStore.CreateSecret("ddns_token",
		"duckdns:test.example.com", []byte("token-x")); err != nil {
		t.Fatal(err)
	}

	rr := doDiagnoseReq(t, token,
		"domain=test.example.com&cert_provider=letsencrypt_staging&challenge_type=dns-01&dns_provider=duckdns")
	res := decodeDiagnoseBody(t, rr)
	chk := findCheck(res.Checks, "ddns_token_exists")
	if chk == nil || chk.Status != CheckStatusOK {
		t.Errorf("expected ok; got %+v", chk)
	}
}

func TestDiagnoseHTTP_DNS01WithoutDNSProvider(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	rr := doDiagnoseReq(t, token,
		"domain=test.example.com&cert_provider=letsencrypt_staging&challenge_type=dns-01")
	res := decodeDiagnoseBody(t, rr)
	chk := findCheck(res.Checks, "dns_provider_set")
	if chk == nil || chk.Status != CheckStatusFail {
		t.Errorf("expected fail for missing dns_provider; got %+v", chk)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Capabilities checks
// ═════════════════════════════════════════════════════════════════════════════

func TestDiagnoseHTTP_CapabilitiesShownAsOK(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	rr := doDiagnoseReq(t, token,
		"domain=test.example.com&cert_provider=letsencrypt_staging&challenge_type=dns-01&dns_provider=duckdns")
	res := decodeDiagnoseBody(t, rr)

	openssl := findCheck(res.Checks, "capability_openssl")
	if openssl == nil || openssl.Status != CheckStatusOK {
		t.Errorf("openssl check = %+v", openssl)
	}
	dig := findCheck(res.Checks, "capability_dig")
	if dig == nil || dig.Status != CheckStatusOK {
		t.Errorf("dig check = %+v", dig)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Overall status
// ═════════════════════════════════════════════════════════════════════════════

func TestDiagnoseHTTP_HappyPathOverallOK(t *testing.T) {
	token, _, cleanup := setupDiagnoseHTTPTest(t)
	defer cleanup()

	// Sembrar todo: token DDNS válido + cert provider registrado +
	// challenge soportado + dns_provider especificado.
	if _, err := networkSecretsStore.CreateSecret("ddns_token",
		"duckdns:test.example.com", []byte("token-x")); err != nil {
		t.Fatal(err)
	}

	rr := doDiagnoseReq(t, token,
		"domain=test.example.com&cert_provider=letsencrypt_staging&challenge_type=dns-01&dns_provider=duckdns")
	res := decodeDiagnoseBody(t, rr)

	// Sin ACME account key persistente; será warn pero NO fail.
	// El test asume que el filesystem NO tiene /var/lib/nimos/acme/account.key
	// (estamos en sandbox). Por tanto OverallStatus puede ser warn o ok.
	if res.OverallStatus == CheckStatusFail {
		t.Errorf("OverallStatus = fail; checks=%+v", res.Checks)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Helper isACMEProvider
// ═════════════════════════════════════════════════════════════════════════════

func TestIsACMEProvider_Cases(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"letsencrypt", true},
		{"letsencrypt_staging", true},
		{"selfsigned", false},
		{"zerossl", false},
		{"", false},
		{"unknown", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isACMEProvider(c.name); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
