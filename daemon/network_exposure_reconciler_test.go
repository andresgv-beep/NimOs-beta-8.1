// network_exposure_reconciler_test.go — Tests del reconciler de exposición.
//
// Usa un caddyLoader mock (no toca Caddy real). Cubre:
//   - Exposure OFF global → Load recibe config vacía, apps marcadas applied.
//   - Exposure ON → apps habilitadas van a Caddy, se marcan applied.
//   - App disabled no aparece en la config aunque exposure esté ON.
//   - Caddy Load falla → apps NO se marcan applied (siguen pending).
//   - Idempotencia: segunda pasada sin cambios no rompe nada.

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"testing"
)

// mockCaddySyncer captura las últimas rutas/TLS recibidas y puede simular fallo.
type mockCaddySyncer struct {
	lastRoutes  []caddyRoute
	calls       int
	failWith    error
	lastDomains []string
	lastPolicy  caddyTLSPolicy
	tlsCalls    int
	tlsFailWith error
	listenCalls    int
	lastHTTPPort   int
	lastHTTPSPort  int
	listenFailWith error
}

func (m *mockCaddySyncer) SyncAppRoutes(ctx context.Context, routes []caddyRoute) error {
	m.calls++
	m.lastRoutes = routes
	return m.failWith
}

func (m *mockCaddySyncer) SyncListen(ctx context.Context, httpPort, httpsPort int) error {
	m.listenCalls++
	m.lastHTTPPort = httpPort
	m.lastHTTPSPort = httpsPort
	return m.listenFailWith
}

func (m *mockCaddySyncer) SyncTLS(ctx context.Context, domains []string, policy caddyTLSPolicy) error {
	m.tlsCalls++
	m.lastDomains = domains
	m.lastPolicy = policy
	return m.tlsFailWith
}

func newTestExposureReconciler(t *testing.T) (*NetworkExposureReconciler, *mockCaddySyncer, *NetworkRepo, *sqlConn, func()) {
	t.Helper()
	repo, clock, c, cleanup := newTestRepo(t)
	mock := &mockCaddySyncer{}
	rec := NewNetworkExposureReconciler(repo, nil, nil, clock, DefaultNetworkExposureReconcilerConfig())
	rec.caddyClientFor = func(adminURL string) caddySyncer { return mock }
	return rec, mock, repo, c, cleanup
}

func TestExposureReconcile_GlobalOffLoadsEmpty(t *testing.T) {
	rec, mock, repo, c, cleanup := newTestExposureReconciler(t)
	defer cleanup()

	// Config OFF + una app habilitada.
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
			BaseDomain: "x.duckdns.org", Enabled: false,
		})
	})
	app := makeExposedApp("immich", "immich")
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateExposedApp(context.Background(), tx, app)
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if mock.calls != 1 {
		t.Errorf("Load calls = %d, want 1", mock.calls)
	}
	// Config vacía: server existe pero sin rutas.
	routes := mock.lastRoutes
	if len(routes) != 0 {
		t.Errorf("routes = %d, want 0 (global off)", len(routes))
	}
	// App marcada applied igualmente (no queda pending eterno).
	got, _ := repo.GetExposedApp(context.Background(), app.ID)
	if got.Convergence.IsPending() {
		t.Error("app should be applied even when global off")
	}
}

func TestExposureReconcile_GlobalOnExposesEnabled(t *testing.T) {
	rec, mock, repo, c, cleanup := newTestExposureReconciler(t)
	defer cleanup()

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
			BaseDomain: "nimosbarraca1.duckdns.org", Enabled: true,
		})
	})
	app := makeExposedApp("immich", "immich")
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateExposedApp(context.Background(), tx, app)
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	routes := mock.lastRoutes
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want 1", len(routes))
	}
	if routes[0].Match[0].Host[0] != "immich.nimosbarraca1.duckdns.org" {
		t.Errorf("wrong host: %v", routes[0].Match[0].Host)
	}
	got, _ := repo.GetExposedApp(context.Background(), app.ID)
	if got.Convergence.IsPending() {
		t.Error("app should be applied after successful load")
	}
}

func TestExposureReconcile_DisabledAppNotExposed(t *testing.T) {
	rec, mock, repo, c, cleanup := newTestExposureReconciler(t)
	defer cleanup()

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
			BaseDomain: "x.duckdns.org", Enabled: true,
		})
	})
	disabled := makeExposedApp("gitea", "gitea")
	disabled.Enabled = false
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateExposedApp(context.Background(), tx, disabled)
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	routes := mock.lastRoutes
	if len(routes) != 0 {
		t.Errorf("routes = %d, want 0 (app disabled)", len(routes))
	}
}

func TestExposureReconcile_CaddyFailKeepsPending(t *testing.T) {
	rec, mock, repo, c, cleanup := newTestExposureReconciler(t)
	defer cleanup()
	mock.failWith = fmt.Errorf("connection refused")

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
			BaseDomain: "x.duckdns.org", Enabled: true,
		})
	})
	app := makeExposedApp("immich", "immich")
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateExposedApp(context.Background(), tx, app)
	})

	err := rec.Reconcile(context.Background())
	if err == nil {
		t.Error("Reconcile should return error when Caddy load fails")
	}
	// App sigue pending (no se aplicó porque Caddy falló).
	got, _ := repo.GetExposedApp(context.Background(), app.ID)
	if !got.Convergence.IsPending() {
		t.Error("app should stay pending when caddy load fails")
	}
}

func TestExposureReconcile_Idempotent(t *testing.T) {
	rec, mock, repo, c, cleanup := newTestExposureReconciler(t)
	defer cleanup()

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
			BaseDomain: "x.duckdns.org", Enabled: true,
		})
	})
	app := makeExposedApp("immich", "immich")
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateExposedApp(context.Background(), tx, app)
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Segunda pasada: app ya aplicada, no debe romper.
	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if mock.calls != 2 {
		t.Errorf("Load calls = %d, want 2 (config sent each pass)", mock.calls)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TLS sync (DNS-01 DuckDNS)
// ─────────────────────────────────────────────────────────────────────────────

func TestExposureReconcile_TLSWithToken(t *testing.T) {
	rec, mock, repo, c, cleanup := newTestExposureReconciler(t)
	defer cleanup()

	// SecretsStore real sobre la DB de test (mismo patrón que tests de DDNS).
	key := make([]byte, masterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatal(err)
	}
	store, err := NewSecretsStoreWithKey(c.db, key, rec.clock)
	if err != nil {
		t.Fatal(err)
	}
	rec.secrets = store

	// Entrada DDNS del dominio base con token cifrado (reutiliza seedDDNS:
	// plaintext = "plaintext-token-<domain>").
	seedDDNS(t, c.db, repo, store, "base.duckdns.org", true, true, 300)

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
			BaseDomain: "base.duckdns.org", Enabled: true,
		})
	})
	app := makeExposedApp("immich", "immich")
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateExposedApp(context.Background(), tx, app)
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if mock.tlsCalls != 1 {
		t.Fatalf("tlsCalls = %d, want 1", mock.tlsCalls)
	}
	// Base primero (cert del panel), luego la app.
	if len(mock.lastDomains) != 2 || mock.lastDomains[0] != "base.duckdns.org" ||
		mock.lastDomains[1] != "immich.base.duckdns.org" {
		t.Errorf("domains = %v, want [base.duckdns.org immich.base.duckdns.org]", mock.lastDomains)
	}
	if len(mock.lastPolicy.Issuers) != 1 {
		t.Fatalf("policy issuers = %+v, want 1 (token disponible)", mock.lastPolicy.Issuers)
	}
	prov := mock.lastPolicy.Issuers[0].Challenges.DNS.Provider
	if prov.Name != "duckdns" || prov.APIToken != "plaintext-token-base.duckdns.org" {
		t.Errorf("provider = %+v (token mal descifrado)", prov)
	}
	if prov.OverrideDomain != "base.duckdns.org" {
		t.Errorf("override_domain = %q, want base domain", prov.OverrideDomain)
	}
}

func TestExposureReconcile_TLSNoTokenDegrades(t *testing.T) {
	// Sin SecretsStore / sin entrada DDNS → el reconciler NO pide certs a
	// Caddy (automate vacío, política inerte), pero las rutas se sincronizan
	// igual. Degradación limpia.
	rec, mock, repo, c, cleanup := newTestExposureReconciler(t)
	defer cleanup()

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
			BaseDomain: "base.duckdns.org", Enabled: true,
		})
	})
	app := makeExposedApp("immich", "immich")
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateExposedApp(context.Background(), tx, app)
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if mock.calls != 1 {
		t.Errorf("route sync calls = %d, want 1 (rutas siempre)", mock.calls)
	}
	if mock.tlsCalls != 1 {
		t.Fatalf("tlsCalls = %d, want 1", mock.tlsCalls)
	}
	if len(mock.lastDomains) != 0 {
		t.Errorf("domains = %v, want empty (sin token no hay DNS-01)", mock.lastDomains)
	}
	if len(mock.lastPolicy.Issuers) != 0 {
		t.Errorf("policy should be inert without token: %+v", mock.lastPolicy.Issuers)
	}
}

func TestExposureReconcile_TLSFailureNotFatal(t *testing.T) {
	// Si el sync TLS falla, las rutas ya sincronizadas se mantienen y las
	// apps se marcan applied: el HTTP no se cae por un problema de certs.
	rec, mock, repo, c, cleanup := newTestExposureReconciler(t)
	defer cleanup()
	mock.tlsFailWith = fmt.Errorf("tls boom")

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
			BaseDomain: "base.duckdns.org", Enabled: true,
		})
	})
	app := makeExposedApp("immich", "immich")
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateExposedApp(context.Background(), tx, app)
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile should not be fatal on TLS failure: %v", err)
	}
	got, _ := repo.GetExposedApp(context.Background(), app.ID)
	if got.Convergence.IsPending() {
		t.Error("app should be applied (routes synced) despite TLS failure")
	}
}

func TestExposureReconcile_SyncsListenPorts(t *testing.T) {
	rec, mock, repo, c, cleanup := newTestExposureReconciler(t)
	defer cleanup()

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
			BaseDomain: "base.duckdns.org", Enabled: true,
			HTTPPort: 8080, HTTPSPort: 8443,
		})
	})
	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if mock.listenCalls != 1 || mock.lastHTTPPort != 8080 || mock.lastHTTPSPort != 8443 {
		t.Errorf("listen sync = %d calls, ports %d/%d; want 1 call, 8080/8443",
			mock.listenCalls, mock.lastHTTPPort, mock.lastHTTPSPort)
	}
}

func TestExposureReconcile_ListenFailureNotFatal(t *testing.T) {
	// Puerto en uso → Caddy rechaza el listen. Las rutas deben sincronizarse
	// igual: el server sigue funcional en los puertos anteriores.
	rec, mock, repo, c, cleanup := newTestExposureReconciler(t)
	defer cleanup()
	mock.listenFailWith = fmt.Errorf("address already in use")

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
			BaseDomain: "base.duckdns.org", Enabled: true,
		})
	})
	app := makeExposedApp("immich", "immich")
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateExposedApp(context.Background(), tx, app)
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile should not be fatal on listen failure: %v", err)
	}
	if mock.calls != 1 {
		t.Errorf("routes should still sync after listen failure (calls=%d)", mock.calls)
	}
}
