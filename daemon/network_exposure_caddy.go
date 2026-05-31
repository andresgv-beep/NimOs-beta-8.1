// network_exposure_caddy.go — Generador de config Caddy + cliente API admin.
//
// Traduce las apps expuestas (network_exposed_apps) + la config global
// (network_exposure_config) al formato JSON que entiende la API admin de
// Caddy, y la envía vía POST /load a http://127.0.0.1:2019.
//
// Reparto de responsabilidades (el modelo de todo el subsistema):
//   · NimOS DECLARA   → tablas en DB (source of truth)
//   · NimOS GENERA    → este archivo: DB → JSON Caddy (derivado, efímero)
//   · Caddy EJECUTA   → TLS (ACME automático) + reverse proxy
//   · NimOS OBSERVA   → network_exposure_observer lee /pki/certificates
//
// El JSON generado NO se persiste: si Caddy reinicia y pierde la config,
// el reconciler la regenera desde la DB. La DB siempre manda.
//
// Enrutado agnóstico (Opción C):
//   · subdomain != "" → match por host (<subdomain>.<base_domain>)
//   · path     != "" → match por path  (<base_domain> + path)
//
// Caddy solicita y renueva el cert ACME automáticamente para cada host que
// aparece en un match "host". Por eso NimOS no gestiona certs: basta con
// declarar el host y Caddy hace el resto.

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

type caddyConfig struct {
	Apps caddyApps `json:"apps"`
}

type caddyApps struct {
	HTTP caddyHTTP `json:"http"`
}

type caddyHTTP struct {
	Servers map[string]caddyServer `json:"servers"`
}

type caddyServer struct {
	Listen []string     `json:"listen"`
	Routes []caddyRoute `json:"routes"`
}

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
// Generador
// ─────────────────────────────────────────────────────────────────────────────

// buildCaddyConfig construye la config Caddy a partir de las apps
// habilitadas y la config global. Si no hay apps o falta base_domain,
// devuelve un server vacío (listen :443 sin rutas) — Caddy queda activo
// pero sin exponer nada.
//
// El nombre del server es fijo ("nimos") para que cada POST /load
// reemplace limpiamente la config anterior.
func buildCaddyConfig(cfg NetworkExposureConfig, apps []*NetworkExposedApp) caddyConfig {
	routes := make([]caddyRoute, 0, len(apps))

	if cfg.BaseDomain != "" {
		for _, a := range apps {
			if !a.Enabled {
				continue
			}
			match := caddyMatch{}
			if a.Subdomain != "" {
				match.Host = []string{a.Subdomain + "." + cfg.BaseDomain}
			}
			if a.Path != "" {
				// Caddy hace match por prefijo con el patrón "/p*".
				match.Path = []string{normalizeCaddyPath(a.Path)}
				if len(match.Host) == 0 {
					match.Host = []string{cfg.BaseDomain}
				}
			}
			route := caddyRoute{
				Match: []caddyMatch{match},
				Handle: []caddyHandle{{
					Handler: "reverse_proxy",
					Upstreams: []caddyUpstream{{
						Dial: fmt.Sprintf("%s:%d", a.UpstreamHost, a.UpstreamPort),
					}},
				}},
			}
			routes = append(routes, route)
		}
	}

	return caddyConfig{
		Apps: caddyApps{
			HTTP: caddyHTTP{
				Servers: map[string]caddyServer{
					"nimos": {
						Listen: []string{":443"},
						Routes: routes,
					},
				},
			},
		},
	}
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

// CaddyAdminClient habla con la API admin de Caddy para cargar config sin
// downtime. El httpClient es inyectable para tests (servidor mock).
type CaddyAdminClient struct {
	adminURL   string
	httpClient *http.Client
}

// NewCaddyAdminClient crea un cliente. adminURL sin barra final
// (ej. "http://127.0.0.1:2019"). Si httpClient es nil, usa uno con timeout.
func NewCaddyAdminClient(adminURL string, httpClient *http.Client) *CaddyAdminClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &CaddyAdminClient{
		adminURL:   strings.TrimRight(adminURL, "/"),
		httpClient: httpClient,
	}
}

// Load envía la config completa a Caddy vía POST /load. Caddy la aplica
// atómicamente: si es inválida, mantiene la anterior y devuelve error.
func (c *CaddyAdminClient) Load(ctx context.Context, cfg caddyConfig) error {
	body, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("caddy load: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.adminURL+"/load", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("caddy load: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("caddy load: request failed (is Caddy running?): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("caddy load: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
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
