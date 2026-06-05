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
// TLS automation (DNS-01 DuckDNS)
// ─────────────────────────────────────────────────────────────────────────────
//
// Los hosts de las apps viven dentro del subroute "nimos_apps", invisibles
// para el automatic HTTPS de Caddy (que solo mira rutas de primer nivel).
// Por eso NimOS le dice a Caddy EXPLÍCITAMENTE qué certs gestionar:
//
//   · /id/nimos_tls (política @id del base) → CÓMO obtenerlos:
//     ACME con challenge DNS-01 vía DuckDNS, usando el token que el módulo
//     DDNS ya custodia en nimos_secrets. DNS-01 no necesita puertos abiertos.
//   · /config/apps/tls/certificates/automate → QUÉ dominios gestionar
//     proactivamente.
//
// override_domain apunta SIEMPRE al dominio base: la API de DuckDNS solo
// conoce el dominio registrado (no sub-subdominios como next.base), y como
// DuckDNS sirve el TXT para todos los subdominios, la validación funciona.

// caddyTLSPolicy es la política de automatización TLS gestionada por NimOS
// (el objeto @id "nimos_tls" definido en el config base).
type caddyTLSPolicy struct {
	ID       string           `json:"@id"`
	Subjects []string         `json:"subjects"`
	Issuers  []caddyTLSIssuer `json:"issuers,omitempty"`
}

type caddyTLSIssuer struct {
	Module     string              `json:"module"`
	Challenges *caddyTLSChallenges `json:"challenges,omitempty"`
}

type caddyTLSChallenges struct {
	DNS *caddyTLSDNSChallenge `json:"dns,omitempty"`
}

type caddyTLSDNSChallenge struct {
	Provider caddyTLSDNSProvider `json:"provider"`
}

type caddyTLSDNSProvider struct {
	Name           string `json:"name"`
	APIToken       string `json:"api_token"`
	OverrideDomain string `json:"override_domain,omitempty"`
}

// collectTLSDomains deriva los dominios cuyos certs debe gestionar Caddy a
// partir de las apps habilitadas. Subdominio → sub.base; ruta → el propio
// base. Deduplicado, orden estable (orden de las apps).
func collectTLSDomains(cfg NetworkExposureConfig, apps []*NetworkExposedApp) []string {
	domains := []string{}
	if cfg.BaseDomain == "" {
		return domains
	}
	seen := map[string]bool{}
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			domains = append(domains, d)
		}
	}
	for _, a := range apps {
		if !a.Enabled {
			continue
		}
		switch {
		case a.Subdomain != "":
			add(a.Subdomain + "." + cfg.BaseDomain)
		case a.Path != "":
			add(cfg.BaseDomain)
		}
	}
	return domains
}

// buildTLSPolicy construye la política @id "nimos_tls". Con dominios y token
// → ACME DNS-01 DuckDNS. Sin dominios o sin token → política inerte (subjects
// vacíos, sin issuers): Caddy no intenta certs que no puede validar.
func buildTLSPolicy(domains []string, duckdnsToken, baseDomain string) caddyTLSPolicy {
	p := caddyTLSPolicy{ID: "nimos_tls", Subjects: domains}
	if p.Subjects == nil {
		p.Subjects = []string{}
	}
	if len(domains) > 0 && duckdnsToken != "" {
		p.Issuers = []caddyTLSIssuer{{
			Module: "acme",
			Challenges: &caddyTLSChallenges{
				DNS: &caddyTLSDNSChallenge{
					Provider: caddyTLSDNSProvider{
						Name:           "duckdns",
						APIToken:       duckdnsToken,
						OverrideDomain: baseDomain,
					},
				},
			},
		}}
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

// caddyTLSPolicyPath apunta a la política TLS @id "nimos_tls" del base.
const caddyTLSPolicyPath = "/id/nimos_tls"

// caddyTLSAutomatePath apunta al array de dominios que Caddy gestiona
// proactivamente (obtiene y renueva sus certs). El base lo define vacío;
// NimOS lo rellena con los dominios de las apps expuestas.
const caddyTLSAutomatePath = "/config/apps/tls/certificates/automate"

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
	if routes == nil {
		routes = []caddyRoute{}
	}
	return c.patchJSON(ctx, caddyAppsRoutesPath, routes, "caddy sync")
}

// SyncTLS sincroniza la gestión de certs con Caddy en dos pasos quirúrgicos,
// en este orden (primero el CÓMO, luego el QUÉ, para que cuando Caddy
// empiece a gestionar un dominio ya tenga la política DNS-01 lista):
//   1. PATCH /id/nimos_tls → política (issuer ACME + DNS-01 DuckDNS + token).
//   2. PATCH .../certificates/automate → dominios a gestionar.
func (c *CaddyAdminClient) SyncTLS(ctx context.Context, domains []string, policy caddyTLSPolicy) error {
	if err := c.patchJSON(ctx, caddyTLSPolicyPath, policy, "caddy tls policy"); err != nil {
		return err
	}
	if domains == nil {
		domains = []string{}
	}
	return c.patchJSON(ctx, caddyTLSAutomatePath, domains, "caddy tls automate")
}

// patchJSON hace PATCH del payload (JSON) al path dado y valida la
// respuesta. label da contexto a los errores.
func (c *CaddyAdminClient) patchJSON(ctx context.Context, path string, payload interface{}, label string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%s: marshal: %w", label, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		c.adminURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: build request: %w", label, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: request failed (is Caddy running?): %w", label, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		trimmed := strings.TrimSpace(string(msg))
		if resp.StatusCode == http.StatusBadRequest && strings.Contains(trimmed, "unknown object") {
			return fmt.Errorf("%s: base config missing managed object (reinstall Caddy base config): %s", label, trimmed)
		}
		return fmt.Errorf("%s: status %d: %s", label, resp.StatusCode, trimmed)
	}
	return nil
}

// Ping comprueba que la API admin de Caddy está viva con un GET /config/
// (endpoint raíz de la config: existe siempre que el admin responda, haya o
// no certs). Es la fuente de verdad de "reachable" para el observer — NO se
// usa FetchCertificates para esto, porque su endpoint puede no existir aún
// y eso no significa que Caddy esté caído.
func (c *CaddyAdminClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.adminURL+"/config/", nil)
	if err != nil {
		return fmt.Errorf("caddy ping: build request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("caddy ping: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("caddy ping: status %d", resp.StatusCode)
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
