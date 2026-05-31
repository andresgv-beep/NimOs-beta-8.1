// network_exposure_config_test.go — Tests del config singleton de exposición.
//
// Cubre:
//   - Get sin fila → defaults (enabled=false, sin dominio).
//   - Save + Get roundtrip.
//   - Upsert: segundo Save actualiza, no duplica.
//   - Enabled=true sin base_domain → error.
//   - CaddyAdminURL vacío → se rellena con default.
//   - CHECK id='singleton' garantiza fila única.

package main

import (
	"context"
	"database/sql"
	"testing"
)

func TestExposureConfig_GetDefaultsWhenEmpty(t *testing.T) {
	repo, _, _, cleanup := newTestRepo(t)
	defer cleanup()

	cfg, err := repo.GetExposureConfig(context.Background())
	if err != nil {
		t.Fatalf("GetExposureConfig: %v", err)
	}
	if cfg.Enabled {
		t.Error("default should be disabled")
	}
	if cfg.BaseDomain != "" {
		t.Errorf("default base_domain = %q, want empty", cfg.BaseDomain)
	}
	if cfg.CaddyAdminURL != "http://127.0.0.1:2019" {
		t.Errorf("default caddy_admin_url = %q", cfg.CaddyAdminURL)
	}
}

func TestExposureConfig_SaveAndGet(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	cfg := NetworkExposureConfig{
		BaseDomain:    "nimosbarraca1.duckdns.org",
		CaddyAdminURL: "http://127.0.0.1:2019",
		Enabled:       true,
	}
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, cfg)
	})

	got, err := repo.GetExposureConfig(context.Background())
	if err != nil {
		t.Fatalf("GetExposureConfig: %v", err)
	}
	if got.BaseDomain != "nimosbarraca1.duckdns.org" || !got.Enabled {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestExposureConfig_UpsertUpdatesNotDuplicates(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
			BaseDomain: "old.duckdns.org", Enabled: true,
		})
	})
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
			BaseDomain: "new.duckdns.org", Enabled: false,
		})
	})

	got, _ := repo.GetExposureConfig(context.Background())
	if got.BaseDomain != "new.duckdns.org" || got.Enabled {
		t.Errorf("upsert did not update: %+v", got)
	}

	// Verificar fila única.
	var count int
	c.db.QueryRow("SELECT COUNT(*) FROM network_exposure_config").Scan(&count)
	if count != 1 {
		t.Errorf("row count = %d, want 1 (singleton)", count)
	}
}

func TestExposureConfig_EnableWithoutDomainRejected(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	tx, _ := c.db.Begin()
	err := repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
		BaseDomain: "", Enabled: true,
	})
	_ = tx.Rollback()
	if err == nil {
		t.Error("enabling exposure without base_domain should fail")
	}
}

func TestExposureConfig_EmptyCaddyURLGetsDefault(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SaveExposureConfig(context.Background(), tx, NetworkExposureConfig{
			BaseDomain: "x.duckdns.org", CaddyAdminURL: "", Enabled: false,
		})
	})
	got, _ := repo.GetExposureConfig(context.Background())
	if got.CaddyAdminURL != "http://127.0.0.1:2019" {
		t.Errorf("empty caddy url not defaulted: %q", got.CaddyAdminURL)
	}
}

func TestExposureConfig_SingletonCheckEnforced(t *testing.T) {
	_, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	// Intentar insertar una fila con id != 'singleton' debe fallar por CHECK.
	_, err := c.db.Exec(`
		INSERT INTO network_exposure_config (id, base_domain, caddy_admin_url, enabled, updated_at)
		VALUES ('otra', 'x.org', 'http://127.0.0.1:2019', 0, '2026-05-21T12:00:00Z')
	`)
	if err == nil {
		t.Error("CHECK(id='singleton') should reject other ids")
	}
}
