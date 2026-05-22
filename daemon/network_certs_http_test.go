// network_certs_http_test.go — Tests de los handlers HTTP de certs.

package main

import (
	"context"
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

var certsHTTPTestMu sync.Mutex

func setupCertsHTTPTest(t *testing.T) (token string, c *sqlConn, cleanup func()) {
	t.Helper()
	certsHTTPTestMu.Lock()

	prevDB := db
	prevRepo := networkRepo
	prevEmitter := networkEventEmitter
	prevSecrets := networkSecretsStore

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
		networkSecretsStore = prevSecrets
		networkEventEmitter = prevEmitter
		networkRepo = prevRepo
		db = prevDB
		dbCleanup()
		certsHTTPTestMu.Unlock()
	}
	return token, c, cleanup
}

func doCertsReq(t *testing.T, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handleNetworkCertsRoutes(rr, req)
	return rr
}

func decodeCertsBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode body: %v (raw=%q)", err, rr.Body.String())
	}
	return out
}

// ═════════════════════════════════════════════════════════════════════════════
// validateCertCreate
// ═════════════════════════════════════════════════════════════════════════════

func TestValidateCertCreate_Cases(t *testing.T) {
	cases := []struct {
		name    string
		req     certCreateRequest
		wantErr bool
	}{
		{"selfsigned ok", certCreateRequest{
			Domain: "test.example.com", CertProvider: "selfsigned",
		}, false},
		{"selfsigned with challenge invalid", certCreateRequest{
			Domain: "test.example.com", CertProvider: "selfsigned",
			ChallengeType: "http-01",
		}, true},
		{"selfsigned with dns_provider invalid", certCreateRequest{
			Domain: "test.example.com", CertProvider: "selfsigned",
			DNSProvider: "duckdns",
		}, true},
		{"letsencrypt http-01 ok", certCreateRequest{
			Domain: "test.example.com", CertProvider: "letsencrypt",
			ChallengeType: "http-01",
		}, false},
		{"letsencrypt dns-01 with duckdns ok", certCreateRequest{
			Domain: "test.example.com", CertProvider: "letsencrypt",
			ChallengeType: "dns-01", DNSProvider: "duckdns",
		}, false},
		{"letsencrypt staging ok", certCreateRequest{
			Domain: "test.example.com", CertProvider: "letsencrypt_staging",
			ChallengeType: "dns-01", DNSProvider: "duckdns",
		}, false},
		{"letsencrypt dns-01 without dns_provider", certCreateRequest{
			Domain: "test.example.com", CertProvider: "letsencrypt",
			ChallengeType: "dns-01",
		}, true},
		{"letsencrypt http-01 with dns_provider", certCreateRequest{
			Domain: "test.example.com", CertProvider: "letsencrypt",
			ChallengeType: "http-01", DNSProvider: "duckdns",
		}, true},
		{"letsencrypt without challenge", certCreateRequest{
			Domain: "test.example.com", CertProvider: "letsencrypt",
		}, true},
		{"unknown provider", certCreateRequest{
			Domain: "test.example.com", CertProvider: "wat",
		}, true},
		{"unknown challenge", certCreateRequest{
			Domain: "test.example.com", CertProvider: "letsencrypt",
			ChallengeType: "tls-alpn-01",
		}, true},
		{"empty domain", certCreateRequest{
			CertProvider: "selfsigned",
		}, true},
		{"empty provider", certCreateRequest{
			Domain: "test.example.com",
		}, true},
		{"bad domain", certCreateRequest{
			Domain: "has space", CertProvider: "selfsigned",
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateCertCreate(&c.req)
			if (err != nil) != c.wantErr {
				t.Errorf("err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// GET /api/v4/network/certs
// ═════════════════════════════════════════════════════════════════════════════

func TestCertsHTTP_ListEmpty(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	rr := doCertsReq(t, token, "GET", "/api/v4/network/certs", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := decodeCertsBody(t, rr)
	if list, ok := body["certs"].([]interface{}); !ok || len(list) != 0 {
		t.Errorf("expected empty list, got %v", body["certs"])
	}
}

func TestCertsHTTP_ListRequiresAuth(t *testing.T) {
	_, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	rr := doCertsReq(t, "", "GET", "/api/v4/network/certs", "")
	if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusForbidden {
		t.Errorf("status=%d, want 401/403", rr.Code)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// POST /api/v4/network/certs
// ═════════════════════════════════════════════════════════════════════════════

func TestCertsHTTP_CreateSelfSignedHappy(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	body := `{
		"domain": "test.example.com",
		"cert_provider": "selfsigned"
	}`
	rr := doCertsReq(t, token, "POST", "/api/v4/network/certs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	out := decodeCertsBody(t, rr)
	if out["domain"] != "test.example.com" {
		t.Errorf("domain = %v", out["domain"])
	}
	if out["cert_provider"] != "selfsigned" {
		t.Errorf("cert_provider = %v", out["cert_provider"])
	}
	// challenge_type / dns_provider deben ser nil/missing.
	if _, has := out["challenge_type"]; has {
		t.Errorf("challenge_type should be omitted, got %v", out["challenge_type"])
	}
	// Estado pending (applied=0 < desired>0).
	if out["status"] != "pending" {
		t.Errorf("status = %v, want pending", out["status"])
	}
}

func TestCertsHTTP_CreateLetsEncryptDNS01(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	body := `{
		"domain": "test.example.com",
		"cert_provider": "letsencrypt_staging",
		"challenge_type": "dns-01",
		"dns_provider": "duckdns"
	}`
	rr := doCertsReq(t, token, "POST", "/api/v4/network/certs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	out := decodeCertsBody(t, rr)
	if out["challenge_type"] != "dns-01" {
		t.Errorf("challenge_type = %v", out["challenge_type"])
	}
	if out["dns_provider"] != "duckdns" {
		t.Errorf("dns_provider = %v", out["dns_provider"])
	}
}

func TestCertsHTTP_CreateDefaults(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	body := `{"domain": "test.example.com", "cert_provider": "selfsigned"}`
	rr := doCertsReq(t, token, "POST", "/api/v4/network/certs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d", rr.Code)
	}
	out := decodeCertsBody(t, rr)
	if out["enabled"] != true {
		t.Errorf("default enabled = %v, want true", out["enabled"])
	}
	if out["auto_renew"] != true {
		t.Errorf("default auto_renew = %v, want true", out["auto_renew"])
	}
}

func TestCertsHTTP_CreateValidationErrors(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	cases := []struct {
		name string
		body string
	}{
		{"no provider", `{"domain": "test.example.com"}`},
		{"no domain", `{"cert_provider": "selfsigned"}`},
		{"selfsigned with challenge", `{"domain": "test.example.com", "cert_provider": "selfsigned", "challenge_type": "http-01"}`},
		{"letsencrypt no challenge", `{"domain": "test.example.com", "cert_provider": "letsencrypt"}`},
		{"dns-01 no dns_provider", `{"domain": "test.example.com", "cert_provider": "letsencrypt", "challenge_type": "dns-01"}`},
		{"bad domain", `{"domain": "has space", "cert_provider": "selfsigned"}`},
		{"invalid JSON", `not json`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := doCertsReq(t, token, "POST", "/api/v4/network/certs", c.body)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("status=%d, want 400; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestCertsHTTP_CreateDuplicateDomainConflict(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	body := `{"domain": "test.example.com", "cert_provider": "selfsigned"}`
	rr1 := doCertsReq(t, token, "POST", "/api/v4/network/certs", body)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first POST status=%d", rr1.Code)
	}
	rr2 := doCertsReq(t, token, "POST", "/api/v4/network/certs", body)
	if rr2.Code != http.StatusConflict {
		t.Errorf("second POST status=%d, want 409", rr2.Code)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// GET /api/v4/network/certs/:id
// ═════════════════════════════════════════════════════════════════════════════

func TestCertsHTTP_GetReturnsView(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	rrCreate := doCertsReq(t, token, "POST", "/api/v4/network/certs",
		`{"domain": "test.example.com", "cert_provider": "selfsigned"}`)
	id := decodeCertsBody(t, rrCreate)["id"].(string)

	rr := doCertsReq(t, token, "GET", "/api/v4/network/certs/"+id, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	out := decodeCertsBody(t, rr)
	if out["id"] != id {
		t.Errorf("id = %v", out["id"])
	}
}

func TestCertsHTTP_GetNotFound(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	rr := doCertsReq(t, token, "GET", "/api/v4/network/certs/nope", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", rr.Code)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// PUT /api/v4/network/certs/:id
// ═════════════════════════════════════════════════════════════════════════════

func TestCertsHTTP_UpdateEnabled(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	rrCreate := doCertsReq(t, token, "POST", "/api/v4/network/certs",
		`{"domain": "test.example.com", "cert_provider": "selfsigned"}`)
	id := decodeCertsBody(t, rrCreate)["id"].(string)

	rr := doCertsReq(t, token, "PUT", "/api/v4/network/certs/"+id,
		`{"enabled": false}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	out := decodeCertsBody(t, rr)
	if out["enabled"] != false {
		t.Errorf("enabled = %v, want false", out["enabled"])
	}
	if out["auto_renew"] != true {
		t.Errorf("auto_renew unexpectedly changed: %v", out["auto_renew"])
	}
}

func TestCertsHTTP_UpdateAutoRenew(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	rrCreate := doCertsReq(t, token, "POST", "/api/v4/network/certs",
		`{"domain": "test.example.com", "cert_provider": "selfsigned"}`)
	id := decodeCertsBody(t, rrCreate)["id"].(string)

	rr := doCertsReq(t, token, "PUT", "/api/v4/network/certs/"+id,
		`{"auto_renew": false}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	out := decodeCertsBody(t, rr)
	if out["auto_renew"] != false {
		t.Errorf("auto_renew = %v", out["auto_renew"])
	}
}

func TestCertsHTTP_UpdateBoth(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	rrCreate := doCertsReq(t, token, "POST", "/api/v4/network/certs",
		`{"domain": "test.example.com", "cert_provider": "selfsigned"}`)
	id := decodeCertsBody(t, rrCreate)["id"].(string)

	rr := doCertsReq(t, token, "PUT", "/api/v4/network/certs/"+id,
		`{"enabled": false, "auto_renew": false}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	out := decodeCertsBody(t, rr)
	if out["enabled"] != false || out["auto_renew"] != false {
		t.Errorf("body=%v", out)
	}
}

func TestCertsHTTP_UpdateRequiresAtLeastOneField(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	rrCreate := doCertsReq(t, token, "POST", "/api/v4/network/certs",
		`{"domain": "test.example.com", "cert_provider": "selfsigned"}`)
	id := decodeCertsBody(t, rrCreate)["id"].(string)

	rr := doCertsReq(t, token, "PUT", "/api/v4/network/certs/"+id, `{}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rr.Code)
	}
}

func TestCertsHTTP_UpdateNotFound(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	rr := doCertsReq(t, token, "PUT", "/api/v4/network/certs/nope",
		`{"enabled": false}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", rr.Code)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// DELETE /api/v4/network/certs/:id
// ═════════════════════════════════════════════════════════════════════════════

func TestCertsHTTP_Delete(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	rrCreate := doCertsReq(t, token, "POST", "/api/v4/network/certs",
		`{"domain": "test.example.com", "cert_provider": "selfsigned"}`)
	id := decodeCertsBody(t, rrCreate)["id"].(string)

	rr := doCertsReq(t, token, "DELETE", "/api/v4/network/certs/"+id, "")
	if rr.Code != http.StatusNoContent {
		t.Errorf("status=%d, want 204", rr.Code)
	}

	rrGet := doCertsReq(t, token, "GET", "/api/v4/network/certs/"+id, "")
	if rrGet.Code != http.StatusNotFound {
		t.Errorf("after delete: status=%d", rrGet.Code)
	}
}

func TestCertsHTTP_DeleteNotFound(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	rr := doCertsReq(t, token, "DELETE", "/api/v4/network/certs/nope", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", rr.Code)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Method routing
// ═════════════════════════════════════════════════════════════════════════════

func TestCertsHTTP_MethodNotAllowed(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	cases := []struct {
		method, path string
	}{
		{"PUT", "/api/v4/network/certs"},
		{"DELETE", "/api/v4/network/certs"},
		{"PATCH", "/api/v4/network/certs"},
		{"POST", "/api/v4/network/certs/some-id"},
	}
	for _, c := range cases {
		rr := doCertsReq(t, token, c.method, c.path, "")
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: status=%d, want 405", c.method, c.path, rr.Code)
		}
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Audit
// ═════════════════════════════════════════════════════════════════════════════

func TestCertsHTTP_CreateEmitsAuditEvent(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	rr := doCertsReq(t, token, "POST", "/api/v4/network/certs",
		`{"domain": "test.example.com", "cert_provider": "selfsigned"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d", rr.Code)
	}

	events, _ := networkEventEmitter.ListEventsByCategory(context.Background(), CategoryCert, 10)
	found := false
	for _, e := range events {
		if e.Event == "created" && e.Level == string(EventLevelInfo) {
			found = true
		}
	}
	if !found {
		t.Error("expected 'created' event of level info")
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Service unavailable
// ═════════════════════════════════════════════════════════════════════════════

func TestCertsHTTP_ServiceUnavailableWhenRepoNil(t *testing.T) {
	token, _, cleanup := setupCertsHTTPTest(t)
	defer cleanup()

	prev := networkRepo
	networkRepo = nil
	defer func() { networkRepo = prev }()

	rr := doCertsReq(t, token, "GET", "/api/v4/network/certs", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", rr.Code)
	}
}
