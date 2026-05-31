// network_exposure_config.go — Config global de exposición (singleton).
//
// network_exposure_config tiene una sola fila (id='singleton'). Guarda los
// parámetros compartidos por todas las apps expuestas: el dominio base, la
// URL admin de Caddy y el interruptor global de exposición.
//
// No usa triple generation: es configuración, no una entidad reconciable.
// El reconciler la lee como parámetro de entrada en cada pasada.
//
// API:
//   GetExposureConfig  → devuelve la config; si no existe, devuelve defaults
//                        (no error) para que el sistema arranque "vacío".
//   SaveExposureConfig → upsert de la fila singleton.

package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// NetworkExposureConfig es la configuración global del subsistema de
// exposición.
type NetworkExposureConfig struct {
	BaseDomain    string    `json:"base_domain"`
	CaddyAdminURL string    `json:"caddy_admin_url"`
	Enabled       bool      `json:"enabled"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// defaultExposureConfig devuelve la config inicial cuando no hay fila aún.
// Exposición desactivada y sin dominio: el admin debe configurarlo desde UI.
func defaultExposureConfig() NetworkExposureConfig {
	return NetworkExposureConfig{
		BaseDomain:    "",
		CaddyAdminURL: "http://127.0.0.1:2019",
		Enabled:       false,
	}
}

// GetExposureConfig lee la fila singleton. Si no existe, devuelve los
// defaults (sin error) — el sistema arranca con exposición desactivada.
func (r *NetworkRepo) GetExposureConfig(ctx context.Context) (NetworkExposureConfig, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT base_domain, caddy_admin_url, enabled, updated_at
		FROM network_exposure_config WHERE id = 'singleton'
	`)
	var (
		cfg        NetworkExposureConfig
		enabledInt int
		updatedStr string
	)
	err := row.Scan(&cfg.BaseDomain, &cfg.CaddyAdminURL, &enabledInt, &updatedStr)
	if err == sql.ErrNoRows {
		return defaultExposureConfig(), nil
	}
	if err != nil {
		return defaultExposureConfig(), fmt.Errorf("GetExposureConfig: %w", err)
	}
	cfg.Enabled = enabledInt != 0
	if t, perr := time.Parse(time.RFC3339, updatedStr); perr == nil {
		cfg.UpdatedAt = t
	}
	return cfg, nil
}

// SaveExposureConfig hace upsert de la fila singleton. Si Enabled=true,
// exige base_domain no vacío (no tiene sentido exponer sin dominio).
func (r *NetworkRepo) SaveExposureConfig(ctx context.Context, tx *sql.Tx, cfg NetworkExposureConfig) error {
	if cfg.Enabled && cfg.BaseDomain == "" {
		return fmt.Errorf("SaveExposureConfig: cannot enable exposure without base_domain")
	}
	if cfg.CaddyAdminURL == "" {
		cfg.CaddyAdminURL = defaultExposureConfig().CaddyAdminURL
	}
	now := r.clock.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO network_exposure_config (id, base_domain, caddy_admin_url, enabled, updated_at)
		VALUES ('singleton', ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			base_domain     = excluded.base_domain,
			caddy_admin_url = excluded.caddy_admin_url,
			enabled         = excluded.enabled,
			updated_at      = excluded.updated_at
	`, cfg.BaseDomain, cfg.CaddyAdminURL, intFromBool(cfg.Enabled), now)
	if err != nil {
		return fmt.Errorf("SaveExposureConfig: %w", err)
	}
	return nil
}
