// network_exposure_caddy.go — Generador de rutas Caddy + cliente API admin.
//
// MODELO 1 (panel independiente): Caddy es servido por un config BASE que
// instala install.sh. Ese base define el server "nimos" con:
//   · La ruta del panel de NimOS (dominio raíz → :5000) + protecciones.
//   · La API admin en :2019.
//   · Un grupo de rutas reservado @id "nimos_apps" (subroute) cuyo array
//     interno de rutas es lo que ESTE módulo gestiona.
//
// El panel NO depende de este módulo: aunque el daemon falle, Caddy sigue
// sirviendo el panel desde su config persistida. Esto es deliberado — el
// panel es infraestructura crítica y no debe depender del subsistema de
// exposición para ser accesible (no metas la llave de casa dentro de casa).
//
// Este módulo gestiona ÚNICAMENTE las rutas de las apps expuestas, de forma
// quirúrgica sobre la API admin de Caddy con un PUT al path del array de
// rutas del grupo "nimos_apps". NUNCA usa POST /load (que reemplazaría toda
// la config y borraría el panel).

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Tipos del config Caddy (subset que usamos)
// ─────────────────────────────────────────────────────────────────────────────

// caddyRoute es una ruta del server: un match + handlers.
type caddyRoute struct {
	Match  []caddyMatch  `json:"match,omitempty"`
	Handle []caddyHandle `json:"handle"`
}

type caddyMatch struct {
	Host []string `json:"host,omitempty"`
	Path []string `json:"path,omitempty"`
}

type caddyHandle struct {
	Handler   string          `json:"handler"`
	Upstreams []caddyUpstream `json:"upstreams,omitempty"`
}

type caddyUpstream struct {
	Dial string `json:"dial"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Generador de rutas de apps
// ─────────────────────────────────────────────────────────────────────────────

// buildAppRoutes construye SOLO las rutas de las apps expuestas (no el panel,
// no la config global). Es lo que NimOS sincroniza con Caddy.
//
// Si no hay dominio base o no hay apps habilitadas, devuelve un array vacío
// — Caddy se queda solo con el panel (definido en el base), sin exponer apps.
func buildAppRoutes(cfg NetworkExposureConfig, apps []*NetworkExposedApp) []caddyRoute {
	routes := make([]caddyRoute, 0, len(apps))
	if cfg.BaseDomain == "" {
		return routes
	}
	for _, a := range apps {
		if !a.Enabled {
			continue
		}
		match := caddyMatch{}
		if a.Subdomain != "" {
			match.Host = []string{a.Subdomain + "." + cfg.BaseDomain}
		}
		if a.Path != "" {
			match.Path = []string{normalizeCaddyPath(a.Path)}
			if len(match.Host) == 0 {
				match.Host = []string{cfg.BaseDomain}
			}
		}
		routes = append(routes, caddyRoute{
			Match: []caddyMatch{match},
			Handle: []caddyHandle{{
				Handler: "reverse_proxy",
				Upstreams: []caddyUpstream{{
					Dial: fmt.Sprintf("%s:%d", a.UpstreamHost, a.UpstreamPort),
				}},
			}},
		})
	}
	return routes
}

// normalizeCaddyPath asegura que el path empiece por "/" y termine en "*"
// para que Caddy haga match por prefijo (ej. "/immich" → "/immich*").
func normalizeCaddyPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "*") {
		p = p + "*"
	}
	return p
}

// ─────────────────────────────────────────────────────────────────────────────
// Cliente API admin
// ─────────────────────────────────────────────────────────────────────────────

// caddyAppsRoutesPath apunta al array de rutas internas del subroute con
// @id "nimos_apps" del server base. Caddy expone los objetos con @id bajo
// el endpoint /id/<nombre>, que entra directo al scope de ese objeto sin
// el prefijo /config/.../@id. Desde el objeto subroute, navegamos a su
// handle[0].routes. Reemplazar este array NO toca el panel ni el resto.
// El base lo instala install.sh.
const caddyAppsRoutesPath = "/id/nimos_apps/handle/0/routes"

// CaddyAdminClient habla con la API admin de Caddy. httpClient inyectable
// para tests.
type CaddyAdminClient struct {
	adminURL   string
	httpClient *http.Client
}

// NewCaddyAdminClient crea un cliente. adminURL sin barra final.
func NewCaddyAdminClient(adminURL string, httpClient *http.Client) *CaddyAdminClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &CaddyAdminClient{
		adminURL:   strings.TrimRight(adminURL, "/"),
		httpClient: httpClient,
	}
}

// SyncAppRoutes reemplaza SOLO el array de rutas de apps (grupo @id
// "nimos_apps"), sin tocar el panel ni la config global. Usa PATCH sobre el
// path concreto del array.
//
// IMPORTANTE: el método es PATCH, no PUT. En la API de Caddy:
//   · PATCH → reemplaza un objeto/array existente (lo que queremos).
//   · PUT   → INSERTA en el array (daría 409 "key already exists" sobre un
//             array ya presente).
//   · POST  → set/replace, pero sobre arrays "appendea".
// Como el array "routes" ya existe en el base (vacío), PATCH lo reemplaza
// limpiamente con el conjunto regenerado.
func (c *CaddyAdminClient) SyncAppRoutes(ctx context.Context, routes []caddyRoute) error {
	body, err := json.Marshal(routes)
	if err != nil {
		return fmt.Errorf("caddy sync: marshal routes: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		c.adminURL+caddyAppsRoutesPath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("caddy sync: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("caddy sync: request failed (is Caddy running?): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		trimmed := strings.TrimSpace(string(msg))
		if resp.StatusCode == http.StatusBadRequest && strings.Contains(trimmed, "unknown object") {
			return fmt.Errorf("caddy sync: base config missing 'nimos_apps' route group (reinstall Caddy base config): %s", trimmed)
		}
		return fmt.Errorf("caddy sync: status %d: %s", resp.StatusCode, trimmed)
	}
	return nil
}

// FetchCertificates lee el estado de los certs gestionados por Caddy desde
// GET /pki/certificates. Devuelve el JSON crudo para que el observer lo
// parsee. Si Caddy no responde, devuelve error (el observer lo trata como
// "estado desconocido", no como fallo crítico).
func (c *CaddyAdminClient) FetchCertificates(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.adminURL+"/pki/certificates", nil)
	if err != nil {
		return nil, fmt.Errorf("caddy certs: build request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("caddy certs: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("caddy certs: status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB cap
}
