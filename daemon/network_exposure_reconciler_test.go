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
	"database/sql"
	"fmt"
	"testing"
)

// mockCaddyLoader captura la última config recibida y puede simular fallo.
type mockCaddyLoader struct {
	lastCfg  caddyConfig
	calls    int
	failWith error
}

func (m *mockCaddyLoader) Load(ctx context.Context, cfg caddyConfig) error {
	m.calls++
	m.lastCfg = cfg
	return m.failWith
}

func newTestExposureReconciler(t *testing.T) (*NetworkExposureReconciler, *mockCaddyLoader, *NetworkRepo, *sqlConn, func()) {
	t.Helper()
	repo, clock, c, cleanup := newTestRepo(t)
	mock := &mockCaddyLoader{}
	rec := NewNetworkExposureReconciler(repo, nil, clock, DefaultNetworkExposureReconcilerConfig())
	rec.caddyClientFor = func(adminURL string) caddyLoader { return mock }
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
	routes := mock.lastCfg.Apps.HTTP.Servers["nimos"].Routes
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

	routes := mock.lastCfg.Apps.HTTP.Servers["nimos"].Routes
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
	routes := mock.lastCfg.Apps.HTTP.Servers["nimos"].Routes
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
