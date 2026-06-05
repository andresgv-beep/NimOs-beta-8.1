// network_exposure_http.go — HTTP handlers para /api/v4/network/exposure.
//
// Endpoint contract:
//
//   Config global (singleton):
//   GET  /api/v4/network/exposure/config   — lee config (dominio, enabled)
//   PUT  /api/v4/network/exposure/config   — actualiza config
//
//   Apps expuestas:
//   GET    /api/v4/network/exposure        — lista apps + estado de certs
//   POST   /api/v4/network/exposure        — registra/expone una app
//   GET    /api/v4/network/exposure/:id    — detalle de una app
//   PUT    /api/v4/network/exposure/:id    — actualiza config de la app
//   DELETE /api/v4/network/exposure/:id    — deja de exponer (borra)
//
// El GET de colección incluye el snapshot de certs del observer para que
// la UI pinte el estado HTTPS de cada app junto a su fila, sin una segunda
// llamada.

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

// networkExposureObserver es el observer global de certs, enchufado en boot.
// Puede ser nil si el módulo no se ha inicializado.
var networkExposureObserver *NetworkExposureObserver

var exposureItemRegex = regexp.MustCompile(`^/api/v4/network/exposure/([^/]+)$`)

// handleNetworkExposureRoutes es el dispatcher del subsistema de exposición.
func handleNetworkExposureRoutes(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	method := r.Method

	// Config singleton.
	if path == "/api/v4/network/exposure/config" {
		switch method {
		case http.MethodGet:
			exposureConfigGetHTTP(w, r)
		case http.MethodPut:
			exposureConfigPutHTTP(w, r)
		default:
			jsonError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	// Colección.
	if path == "/api/v4/network/exposure" {
		switch method {
		case http.MethodGet:
			exposureListHTTP(w, r)
		case http.MethodPost:
			exposureCreateHTTP(w, r)
		default:
			jsonError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	// Recurso individual.
	if m := exposureItemRegex.FindStringSubmatch(path); m != nil {
		id := m[1]
		// "config" ya se manejó arriba; aquí id es un UUID de app.
		switch method {
		case http.MethodGet:
			exposureGetHTTP(w, r, id)
		case http.MethodPut:
			exposureUpdateHTTP(w, r, id)
		case http.MethodDelete:
			exposureDeleteHTTP(w, r, id)
		default:
			jsonError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	jsonError(w, http.StatusNotFound, "Not found")
}

// ─────────────────────────────────────────────────────────────────────────────
// Config singleton
// ─────────────────────────────────────────────────────────────────────────────

func exposureConfigGetHTTP(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	if networkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "Network module not initialized")
		return
	}
	cfg, err := networkRepo.GetExposureConfig(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOk(w, map[string]interface{}{"config": cfg})
}

type exposureConfigRequest struct {
	BaseDomain    *string `json:"base_domain"`
	CaddyAdminURL *string `json:"caddy_admin_url"`
	Enabled       *bool   `json:"enabled"`
	HTTPPort      *int    `json:"http_port"`
	HTTPSPort     *int    `json:"https_port"`
}

func exposureConfigPutHTTP(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	if networkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "Network module not initialized")
		return
	}
	var req exposureConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	// Partimos de la config actual y aplicamos los campos presentes.
	cfg, err := networkRepo.GetExposureConfig(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.BaseDomain != nil {
		cfg.BaseDomain = strings.TrimSpace(*req.BaseDomain)
	}
	if req.CaddyAdminURL != nil {
		cfg.CaddyAdminURL = strings.TrimSpace(*req.CaddyAdminURL)
	}
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if req.HTTPPort != nil {
		cfg.HTTPPort = *req.HTTPPort
	}
	if req.HTTPSPort != nil {
		cfg.HTTPSPort = *req.HTTPSPort
	}

	err = exposureWithTx(r.Context(), func(tx *sql.Tx) error {
		return networkRepo.SaveExposureConfig(r.Context(), tx, cfg)
	})
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOk(w, map[string]interface{}{"config": cfg})
}

// ─────────────────────────────────────────────────────────────────────────────
// Apps
// ─────────────────────────────────────────────────────────────────────────────

func exposureListHTTP(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	if networkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "Network module not initialized")
		return
	}
	apps, err := networkRepo.ListExposedApps(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := map[string]interface{}{"apps": apps}
	// Adjuntar snapshot de certs si el observer está disponible.
	if networkExposureObserver != nil {
		if snap := networkExposureObserver.Snapshot(); snap != nil {
			resp["certs"] = snap
		}
	}
	jsonOk(w, resp)
}

type exposureCreateRequest struct {
	AppID        string `json:"app_id"`
	DisplayName  string `json:"display_name"`
	Subdomain    string `json:"subdomain"`
	Path         string `json:"path"`
	UpstreamHost string `json:"upstream_host"`
	UpstreamPort int    `json:"upstream_port"`
	Enabled      *bool  `json:"enabled"`
}

func exposureCreateHTTP(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	if networkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "Network module not initialized")
		return
	}
	var req exposureCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.AppID) == "" {
		jsonError(w, http.StatusBadRequest, "app_id is required")
		return
	}
	if req.Subdomain == "" && req.Path == "" {
		jsonError(w, http.StatusBadRequest, "subdomain or path is required")
		return
	}
	if req.UpstreamHost == "" {
		jsonError(w, http.StatusBadRequest, "upstream_host is required")
		return
	}
	if req.UpstreamPort <= 0 || req.UpstreamPort > 65535 {
		jsonError(w, http.StatusBadRequest, "upstream_port must be 1-65535")
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	app := &NetworkExposedApp{
		AppID:        req.AppID,
		DisplayName:  req.DisplayName,
		Subdomain:    strings.TrimSpace(req.Subdomain),
		Path:         strings.TrimSpace(req.Path),
		UpstreamHost: req.UpstreamHost,
		UpstreamPort: req.UpstreamPort,
		Enabled:      enabled,
	}
	err := exposureWithTx(r.Context(), func(tx *sql.Tx) error {
		return networkRepo.CreateExposedApp(r.Context(), tx, app)
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			jsonError(w, http.StatusConflict, "App already exposed (app_id or route in use)")
			return
		}
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOk(w, map[string]interface{}{"app": app})
}

func exposureGetHTTP(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	if networkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "Network module not initialized")
		return
	}
	app, err := networkRepo.GetExposedApp(r.Context(), id)
	if errors.Is(err, ErrExposedAppNotFound) {
		jsonError(w, http.StatusNotFound, "Exposed app not found")
		return
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOk(w, map[string]interface{}{"app": app})
}

func exposureUpdateHTTP(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	if networkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "Network module not initialized")
		return
	}
	// Cargar existente para componer los campos a actualizar.
	app, err := networkRepo.GetExposedApp(r.Context(), id)
	if errors.Is(err, ErrExposedAppNotFound) {
		jsonError(w, http.StatusNotFound, "Exposed app not found")
		return
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req exposureCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if req.DisplayName != "" {
		app.DisplayName = req.DisplayName
	}
	if req.Subdomain != "" || req.Path != "" {
		app.Subdomain = strings.TrimSpace(req.Subdomain)
		app.Path = strings.TrimSpace(req.Path)
	}
	if req.UpstreamHost != "" {
		app.UpstreamHost = req.UpstreamHost
	}
	if req.UpstreamPort != 0 {
		app.UpstreamPort = req.UpstreamPort
	}
	if req.Enabled != nil {
		app.Enabled = *req.Enabled
	}

	err = exposureWithTx(r.Context(), func(tx *sql.Tx) error {
		return networkRepo.UpdateExposedAppConfig(r.Context(), tx, app)
	})
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOk(w, map[string]interface{}{"app": app})
}

func exposureDeleteHTTP(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	if networkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "Network module not initialized")
		return
	}
	err := exposureWithTx(r.Context(), func(tx *sql.Tx) error {
		return networkRepo.DeleteExposedApp(r.Context(), tx, id)
	})
	if errors.Is(err, ErrExposedAppNotFound) {
		jsonError(w, http.StatusNotFound, "Exposed app not found")
		return
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOk(w, map[string]interface{}{"deleted": id})
}

// exposureWithTx ejecuta fn dentro de una tx sobre la conexión global `db`.
func exposureWithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
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
