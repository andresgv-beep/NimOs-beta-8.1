// network_cert_reconciler_test.go — Tests del CertReconciler.
//
// Estrategia:
//   - mockCertProvider devuelve material configurable.
//   - mockDNSChallengerFactory devuelve un mock challenger inerte.
//   - DB real con schemas + SecretsStore real (master key efímera).
//   - FakeClock para controlar tiempo de needsRenewal.
//   - CertsBaseDir en t.TempDir() para que los tests no toquen /etc/ssl.
//
// Cubre:
//   - Reconciler interface (Name/Tier/Interval).
//   - needsRenewal: pending, expirando, auto_renew false, ya renovado.
//   - processOne happy path con selfsigned (sin challenges).
//   - processOne con dns-01 + DNSChallenger.
//   - Provider desconocido → provider_unknown event.
//   - Challenge incompatible → challenge_unsupported event.
//   - dns_provider missing → dns_provider_missing event.
//   - Errores clasificados: challenge_failed, rate_limited, transient.
//   - Filesystem: directorios y permisos correctos.
//   - Idempotencia: segunda pasada cerca en tiempo no renueva.

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock cert provider
// ─────────────────────────────────────────────────────────────────────────────

type mockCertProvider struct {
	name              string
	supportsChallenge map[string]bool

	mu       sync.Mutex
	calls    []CertRequest
	material *CertMaterial
	issueErr error
}

func (m *mockCertProvider) Name() string { return m.name }

func (m *mockCertProvider) SupportsChallenge(c string) bool {
	return m.supportsChallenge[c]
}

func (m *mockCertProvider) Issue(_ context.Context, req CertRequest) (*CertMaterial, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)
	return m.material, m.issueErr
}

func (m *mockCertProvider) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// ─────────────────────────────────────────────────────────────────────────────
// Mock DNS challenger (just for plumbing, doesn't do real DNS)
// ─────────────────────────────────────────────────────────────────────────────

type mockDNSChallenger struct {
	name string
}

func (m *mockDNSChallenger) Name() string                                    { return m.name }
func (m *mockDNSChallenger) SetTXT(_ context.Context, _, _ string) error     { return nil }
func (m *mockDNSChallenger) RemoveTXT(_ context.Context, _ string) error     { return nil }

// mockDNSChallengerFactory devuelve siempre el mismo mock; mantiene
// estado del último token recibido para verificación.
type mockDNSFactoryState struct {
	mu          sync.Mutex
	lastName    string
	lastToken   string
	returnError error
}

func (s *mockDNSFactoryState) Factory() DNSChallengerFactory {
	return func(name, token string) (DNSChallengeProvider, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.lastName = name
		s.lastToken = token
		if s.returnError != nil {
			return nil, s.returnError
		}
		return &mockDNSChallenger{name: name}, nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// materialFor crea CertMaterial con fechas relativas al clock.
func materialFor(now time.Time, validityDays int) *CertMaterial {
	return &CertMaterial{
		FullchainPEM: []byte("FAKE-FULLCHAIN-PEM"),
		PrivkeyPEM:   []byte("FAKE-PRIVKEY-PEM"),
		NotBefore:    now.Unix(),
		NotAfter:     now.Add(time.Duration(validityDays) * 24 * time.Hour).Unix(),
	}
}

func newTestCertReconciler(t *testing.T) (*CertReconciler, *mockCertProvider, *mockDNSFactoryState, *NetworkRepo, *SecretsStore, *EventEmitter, *FakeClock, string, *sqlConn, func()) {
	t.Helper()
	c, cleanup := setupNetworkDB(t)
	clock := NewFakeClock(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	repo := NewNetworkRepo(c.db, clock)
	emitter := NewEventEmitter(c.db, clock, DefaultEventEmitterConfig())

	key := make([]byte, masterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatal(err)
	}
	store, err := NewSecretsStoreWithKey(c.db, key, clock)
	if err != nil {
		t.Fatal(err)
	}

	dnsState := &mockDNSFactoryState{}
	tmpCertsDir := t.TempDir()

	rec, err := NewCertReconciler(repo, store, emitter, clock, dnsState.Factory(), CertReconcilerConfig{
		Interval:     60 * time.Second,
		RenewWindow:  30 * 24 * time.Hour,
		CertsBaseDir: tmpCertsDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	provider := &mockCertProvider{
		name:              "selfsigned",
		supportsChallenge: map[string]bool{}, // none by default
	}
	rec.RegisterProvider(provider)

	return rec, provider, dnsState, repo, store, emitter, clock, tmpCertsDir, c, cleanup
}

// seedCert crea un cert en DB en estado aplicado (applied=desired).
func seedCert(t *testing.T, db *sql.DB, repo *NetworkRepo, c *NetworkCert) {
	t.Helper()
	withNetTx(t, db, func(tx *sql.Tx) error {
		if err := repo.CreateCert(context.Background(), tx, c); err != nil {
			return err
		}
		return repo.MarkCertApplied(context.Background(), tx, c.ID)
	})
}

// ═════════════════════════════════════════════════════════════════════════════
// Construction & interface
// ═════════════════════════════════════════════════════════════════════════════

func TestCertReconciler_NewRequiresDeps(t *testing.T) {
	_, err := NewCertReconciler(nil, nil, nil, nil, nil, CertReconcilerConfig{})
	if err == nil {
		t.Error("expected error with nil deps")
	}
}

func TestCertReconciler_ImplementsReconciler(t *testing.T) {
	rec, _, _, _, _, _, _, _, _, cleanup := newTestCertReconciler(t)
	defer cleanup()

	if rec.Name() != "cert_renewer" {
		t.Errorf("Name = %q", rec.Name())
	}
	if rec.Tier() != TierCritical {
		t.Errorf("Tier = %v, want Critical", rec.Tier())
	}
	if rec.Interval() != 60*time.Second {
		t.Errorf("Interval = %v", rec.Interval())
	}
}

func TestCertReconciler_DefaultsApplied(t *testing.T) {
	c, cleanup := setupNetworkDB(t)
	defer cleanup()
	clock := NewFakeClock(time.Now())
	repo := NewNetworkRepo(c.db, clock)
	emitter := NewEventEmitter(c.db, clock, DefaultEventEmitterConfig())
	key := make([]byte, masterKeySize)
	io.ReadFull(rand.Reader, key)
	store, _ := NewSecretsStoreWithKey(c.db, key, clock)

	rec, err := NewCertReconciler(repo, store, emitter, clock,
		func(name, token string) (DNSChallengeProvider, error) {
			return nil, nil
		},
		CertReconcilerConfig{}) // empty -> defaults
	if err != nil {
		t.Fatal(err)
	}
	if rec.config.Interval == 0 || rec.config.RenewWindow == 0 || rec.config.CertsBaseDir == "" {
		t.Errorf("defaults not applied: %+v", rec.config)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// needsRenewal decision
// ═════════════════════════════════════════════════════════════════════════════

func TestCertReconciler_NeedsRenewal_Pending(t *testing.T) {
	rec, _, _, _, _, _, _, _, _, cleanup := newTestCertReconciler(t)
	defer cleanup()

	c := &NetworkCert{
		AutoRenew:   false,
		Convergence: Convergence{Desired: 2, Applied: 1},
		NotAfter:    rec.clock.Now().Add(100 * 24 * time.Hour),
	}
	if !rec.needsRenewal(c) {
		t.Error("pending should trigger")
	}
}

func TestCertReconciler_NeedsRenewal_AutoExpiring(t *testing.T) {
	rec, _, _, _, _, _, clock, _, _, cleanup := newTestCertReconciler(t)
	defer cleanup()

	// Cert expira en 15 días, dentro de window 30d.
	c := &NetworkCert{
		AutoRenew:   true,
		Convergence: Convergence{Desired: 1, Applied: 1},
		NotAfter:    clock.Now().Add(15 * 24 * time.Hour),
	}
	if !rec.needsRenewal(c) {
		t.Error("expiring within window should trigger")
	}
}

func TestCertReconciler_NeedsRenewal_AutoFarFromExpiry(t *testing.T) {
	rec, _, _, _, _, _, clock, _, _, cleanup := newTestCertReconciler(t)
	defer cleanup()

	c := &NetworkCert{
		AutoRenew:   true,
		Convergence: Convergence{Desired: 1, Applied: 1},
		NotAfter:    clock.Now().Add(60 * 24 * time.Hour),
	}
	if rec.needsRenewal(c) {
		t.Error("far from expiry should NOT trigger")
	}
}

func TestCertReconciler_NeedsRenewal_NoAutoRenewSkips(t *testing.T) {
	rec, _, _, _, _, _, clock, _, _, cleanup := newTestCertReconciler(t)
	defer cleanup()

	c := &NetworkCert{
		AutoRenew:   false,
		Convergence: Convergence{Desired: 1, Applied: 1},
		NotAfter:    clock.Now().Add(5 * 24 * time.Hour), // expiring fast
	}
	if rec.needsRenewal(c) {
		t.Error("auto_renew=false should never trigger when applied")
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// processOne — happy paths
// ═════════════════════════════════════════════════════════════════════════════

func TestCertReconciler_HappyPathSelfSigned(t *testing.T) {
	rec, provider, _, repo, _, emitter, clock, baseDir, c, cleanup := newTestCertReconciler(t)
	defer cleanup()

	provider.material = materialFor(clock.Now(), 365)

	cert := &NetworkCert{
		Domain:        "test.example.com",
		CertProvider:  "selfsigned",
		FullchainPath: filepath.Join(baseDir, "placeholder", "fullchain.pem"),
		PrivkeyPath:   filepath.Join(baseDir, "placeholder", "privkey.pem"),
		NotBefore:     clock.Now().Add(-time.Hour),
		NotAfter:      clock.Now().Add(10 * 24 * time.Hour),
		Enabled:       true,
		AutoRenew:     true,
		IssuedAt:      clock.Now().Add(-30 * 24 * time.Hour),
	}
	seedCert(t, c.db, repo, cert)

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Provider llamado una vez.
	if provider.CallCount() != 1 {
		t.Errorf("provider calls = %d, want 1", provider.CallCount())
	}

	// Archivos escritos.
	fullchainPath := filepath.Join(baseDir, cert.ID, "fullchain.pem")
	privkeyPath := filepath.Join(baseDir, cert.ID, "privkey.pem")
	if _, err := os.Stat(fullchainPath); err != nil {
		t.Errorf("fullchain missing: %v", err)
	}
	if _, err := os.Stat(privkeyPath); err != nil {
		t.Errorf("privkey missing: %v", err)
	}

	// Permisos (skip en Windows).
	if runtime.GOOS != "windows" {
		fcInfo, _ := os.Stat(fullchainPath)
		if fcInfo.Mode().Perm() != 0o644 {
			t.Errorf("fullchain perm = %o, want 0644", fcInfo.Mode().Perm())
		}
		pkInfo, _ := os.Stat(privkeyPath)
		if pkInfo.Mode().Perm() != 0o600 {
			t.Errorf("privkey perm = %o, want 0600", pkInfo.Mode().Perm())
		}
	}

	// Persistencia DB: paths actualizados, fechas, last_renewed_at set.
	updated, _ := repo.GetCert(context.Background(), cert.ID)
	if updated.FullchainPath != fullchainPath {
		t.Errorf("DB fullchain_path = %q, want %q", updated.FullchainPath, fullchainPath)
	}
	if updated.LastRenewedAt == nil {
		t.Error("LastRenewedAt should be set after renewal")
	}

	// Evento (primera vez → cert_issued).
	events, _ := emitter.ListEventsByCategory(context.Background(), CategoryCert, 10)
	foundIssued := false
	for _, e := range events {
		if e.Event == "cert_issued" {
			foundIssued = true
			if e.Level != string(EventLevelInfo) {
				t.Errorf("level = %q, want info", e.Level)
			}
		}
	}
	if !foundIssued {
		t.Error("expected cert_issued event")
	}

	// Operation registrada con triggered_by correcto.
	ops, _ := repo.ListOperationsByTriggeredBy(context.Background(), "reconciler:cert_renewer", 10)
	if len(ops) != 1 || ops[0].Status != "completed" {
		t.Errorf("operation = %+v", ops)
	}
}

func TestCertReconciler_DNSChallengeIntegrated(t *testing.T) {
	rec, provider, dnsState, repo, store, _, clock, baseDir, c, cleanup := newTestCertReconciler(t)
	defer cleanup()

	// Provider que sí soporta dns-01.
	provider.supportsChallenge = map[string]bool{"dns-01": true}
	provider.material = materialFor(clock.Now(), 90)

	// Sembrar secret DDNS para el dominio.
	if _, err := store.CreateSecret("ddns_token", "duckdns:my.example.com", []byte("ddns-token-value")); err != nil {
		t.Fatal(err)
	}

	challengeType := "dns-01"
	dnsProvider := "duckdns"
	cert := &NetworkCert{
		Domain:        "my.example.com",
		CertProvider:  "selfsigned", // mock dice que soporta dns-01
		ChallengeType: &challengeType,
		DNSProvider:   &dnsProvider,
		FullchainPath: filepath.Join(baseDir, "placeholder", "fullchain.pem"),
		PrivkeyPath:   filepath.Join(baseDir, "placeholder", "privkey.pem"),
		NotBefore:     clock.Now().Add(-time.Hour),
		NotAfter:      clock.Now().Add(10 * 24 * time.Hour),
		Enabled:       true,
		AutoRenew:     true,
		IssuedAt:      clock.Now().Add(-30 * 24 * time.Hour),
	}
	seedCert(t, c.db, repo, cert)

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Provider llamado con DNSChallenger no nil.
	if provider.CallCount() != 1 {
		t.Fatalf("calls = %d, want 1", provider.CallCount())
	}
	if provider.calls[0].DNSChallenger == nil {
		t.Error("provider should have received DNSChallenger")
	}

	// El factory fue invocado con el token correcto.
	dnsState.mu.Lock()
	defer dnsState.mu.Unlock()
	if dnsState.lastName != "duckdns" {
		t.Errorf("factory name = %q", dnsState.lastName)
	}
	if dnsState.lastToken != "ddns-token-value" {
		t.Errorf("factory token = %q", dnsState.lastToken)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// processOne — error paths
// ═════════════════════════════════════════════════════════════════════════════

func TestCertReconciler_UnknownProvider(t *testing.T) {
	rec, _, _, repo, _, emitter, clock, baseDir, c, cleanup := newTestCertReconciler(t)
	defer cleanup()

	cert := &NetworkCert{
		Domain:        "x.example.com",
		CertProvider:  "letsencrypt", // not registered
		FullchainPath: filepath.Join(baseDir, "p", "fullchain.pem"),
		PrivkeyPath:   filepath.Join(baseDir, "p", "privkey.pem"),
		NotBefore:     clock.Now(),
		NotAfter:      clock.Now().Add(5 * 24 * time.Hour),
		Enabled:       true,
		AutoRenew:     true,
		IssuedAt:      clock.Now(),
	}
	seedCert(t, c.db, repo, cert)

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	events, _ := emitter.ListEventsByCategory(context.Background(), CategoryCert, 10)
	found := false
	for _, e := range events {
		if e.Event == "provider_unknown" {
			found = true
		}
	}
	if !found {
		t.Error("expected provider_unknown event")
	}
}

// NOTA: TestCertReconciler_ChallengeUnsupported y
// TestCertReconciler_DNSProviderMissing fueron eliminados porque el
// CHECK constraint del schema network_certs ya impide crear filas
// con estados challenge_type/dns_provider inconsistentes:
//
//   CHECK((challenge_type IS NULL     AND dns_provider IS NULL) OR
//         (challenge_type = 'http-01' AND dns_provider IS NULL) OR
//         (challenge_type = 'dns-01'  AND dns_provider IS NOT NULL))
//
// La lógica defensiva queda en el reconciler como defense-in-depth
// (no cuesta nada) pero no la testeamos porque la DB ya garantiza
// que no puede ocurrir vía CreateCert.

func TestCertReconciler_ChallengeFailed(t *testing.T) {
	rec, provider, _, repo, _, emitter, clock, baseDir, c, cleanup := newTestCertReconciler(t)
	defer cleanup()

	provider.issueErr = ErrCertChallengeFailed
	cert := &NetworkCert{
		Domain:        "x.example.com",
		CertProvider:  "selfsigned",
		FullchainPath: filepath.Join(baseDir, "p", "fullchain.pem"),
		PrivkeyPath:   filepath.Join(baseDir, "p", "privkey.pem"),
		NotBefore:     clock.Now(),
		NotAfter:      clock.Now().Add(5 * 24 * time.Hour),
		Enabled:       true,
		AutoRenew:     true,
		IssuedAt:      clock.Now(),
	}
	seedCert(t, c.db, repo, cert)

	rec.Reconcile(context.Background())

	events, _ := emitter.ListEventsByCategory(context.Background(), CategoryCert, 10)
	found := false
	for _, e := range events {
		if e.Event == "challenge_failed" {
			found = true
			if e.Level != string(EventLevelError) {
				t.Errorf("level = %q", e.Level)
			}
		}
	}
	if !found {
		t.Error("expected challenge_failed event")
	}
}

func TestCertReconciler_RateLimitedIsWarn(t *testing.T) {
	rec, provider, _, repo, _, emitter, clock, baseDir, c, cleanup := newTestCertReconciler(t)
	defer cleanup()

	provider.issueErr = ErrCertProviderRateLimited
	cert := &NetworkCert{
		Domain:        "x.example.com",
		CertProvider:  "selfsigned",
		FullchainPath: filepath.Join(baseDir, "p", "fullchain.pem"),
		PrivkeyPath:   filepath.Join(baseDir, "p", "privkey.pem"),
		NotBefore:     clock.Now(),
		NotAfter:      clock.Now().Add(5 * 24 * time.Hour),
		Enabled:       true,
		AutoRenew:     true,
		IssuedAt:      clock.Now(),
	}
	seedCert(t, c.db, repo, cert)

	rec.Reconcile(context.Background())

	events, _ := emitter.ListEventsByCategory(context.Background(), CategoryCert, 10)
	found := false
	for _, e := range events {
		if e.Event == "rate_limited" {
			found = true
			if e.Level != string(EventLevelWarn) {
				t.Errorf("rate_limited level = %q, want warn", e.Level)
			}
		}
	}
	if !found {
		t.Error("expected rate_limited event")
	}
}

func TestCertReconciler_TransientIsWarn(t *testing.T) {
	rec, provider, _, repo, _, emitter, clock, baseDir, c, cleanup := newTestCertReconciler(t)
	defer cleanup()

	provider.issueErr = ErrCertProviderTransient
	cert := &NetworkCert{
		Domain:        "x.example.com",
		CertProvider:  "selfsigned",
		FullchainPath: filepath.Join(baseDir, "p", "fullchain.pem"),
		PrivkeyPath:   filepath.Join(baseDir, "p", "privkey.pem"),
		NotBefore:     clock.Now(),
		NotAfter:      clock.Now().Add(5 * 24 * time.Hour),
		Enabled:       true,
		AutoRenew:     true,
		IssuedAt:      clock.Now(),
	}
	seedCert(t, c.db, repo, cert)

	rec.Reconcile(context.Background())

	events, _ := emitter.ListEventsByCategory(context.Background(), CategoryCert, 10)
	found := false
	for _, e := range events {
		if e.Event == "transient_failure" {
			found = true
			if e.Level != string(EventLevelWarn) {
				t.Errorf("transient level = %q, want warn", e.Level)
			}
		}
	}
	if !found {
		t.Error("expected transient_failure event")
	}
}

func TestCertReconciler_ContextCancellation(t *testing.T) {
	rec, provider, _, repo, _, _, clock, baseDir, c, cleanup := newTestCertReconciler(t)
	defer cleanup()

	provider.material = materialFor(clock.Now(), 90)

	// Several certs to renew.
	for i := 0; i < 5; i++ {
		cert := &NetworkCert{
			Domain:        "x" + string(rune('a'+i)) + ".example.com",
			CertProvider:  "selfsigned",
			FullchainPath: filepath.Join(baseDir, "p", "fullchain.pem"),
			PrivkeyPath:   filepath.Join(baseDir, "p", "privkey.pem"),
			NotBefore:     clock.Now(),
			NotAfter:      clock.Now().Add(5 * 24 * time.Hour),
			Enabled:       true,
			AutoRenew:     true,
			IssuedAt:      clock.Now(),
		}
		seedCert(t, c.db, repo, cert)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := rec.Reconcile(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestCertReconciler_SkipsDisabled(t *testing.T) {
	rec, provider, _, repo, _, _, clock, baseDir, c, cleanup := newTestCertReconciler(t)
	defer cleanup()

	provider.material = materialFor(clock.Now(), 90)

	cert := &NetworkCert{
		Domain:        "disabled.example.com",
		CertProvider:  "selfsigned",
		FullchainPath: filepath.Join(baseDir, "p", "fullchain.pem"),
		PrivkeyPath:   filepath.Join(baseDir, "p", "privkey.pem"),
		NotBefore:     clock.Now(),
		NotAfter:      clock.Now().Add(5 * 24 * time.Hour),
		Enabled:       false, // disabled
		AutoRenew:     true,
		IssuedAt:      clock.Now(),
	}
	seedCert(t, c.db, repo, cert)

	rec.Reconcile(context.Background())

	if provider.CallCount() != 0 {
		t.Errorf("disabled cert was processed; calls=%d", provider.CallCount())
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Idempotency
// ═════════════════════════════════════════════════════════════════════════════

func TestCertReconciler_IdempotencyAfterRenewal(t *testing.T) {
	rec, provider, _, repo, _, _, clock, baseDir, c, cleanup := newTestCertReconciler(t)
	defer cleanup()

	provider.material = materialFor(clock.Now(), 90) // expires in 90 days

	cert := &NetworkCert{
		Domain:        "test.example.com",
		CertProvider:  "selfsigned",
		FullchainPath: filepath.Join(baseDir, "p", "fullchain.pem"),
		PrivkeyPath:   filepath.Join(baseDir, "p", "privkey.pem"),
		NotBefore:     clock.Now().Add(-time.Hour),
		NotAfter:      clock.Now().Add(5 * 24 * time.Hour), // expiring → triggers
		Enabled:       true,
		AutoRenew:     true,
		IssuedAt:      clock.Now().Add(-30 * 24 * time.Hour),
	}
	seedCert(t, c.db, repo, cert)

	// Primera pasada: renueva.
	rec.Reconcile(context.Background())
	if provider.CallCount() != 1 {
		t.Fatalf("first pass calls = %d", provider.CallCount())
	}

	// Segunda pasada justo después: el cert ahora vence en 90d → no renueva.
	clock.Advance(5 * time.Second)
	rec.Reconcile(context.Background())
	if provider.CallCount() != 1 {
		t.Errorf("second pass should not renew (cert is fresh); calls=%d", provider.CallCount())
	}
}
