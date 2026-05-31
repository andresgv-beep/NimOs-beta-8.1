// network_exposure_caddy_test.go — Tests del generador de rutas + cliente admin.
//
// Cubre:
//   - buildAppRoutes: subdomain → match host.
//   - buildAppRoutes: path → match path (+ host base).
//   - buildAppRoutes: app disabled se omite.
//   - buildAppRoutes: sin base_domain → array vacío.
//   - normalizeCaddyPath: añade / y *.
//   - SyncAppRoutes: PUT correcto al path del grupo nimos_apps, 200 OK.
//   - SyncAppRoutes: status != 200 → error con cuerpo.
//   - SyncAppRoutes: "unknown object" → error pista de base mal instalado.
//   - SyncAppRoutes: servidor caído → error claro.
//   - FetchCertificates: devuelve el cuerpo crudo.

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildAppRoutes_SubdomainMatchHost(t *testing.T) {
	cfg := NetworkExposureConfig{BaseDomain: "nimosbarraca1.duckdns.org", Enabled: true}
	apps := []*NetworkExposedApp{
		{AppID: "immich", Subdomain: "immich", UpstreamHost: "127.0.0.1", UpstreamPort: 2283, Enabled: true},
	}
	routes := buildAppRoutes(cfg, apps)
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want 1", len(routes))
	}
	host := routes[0].Match[0].Host
	if len(host) != 1 || host[0] != "immich.nimosbarraca1.duckdns.org" {
		t.Errorf("host = %v, want [immich.nimosbarraca1.duckdns.org]", host)
	}
	dial := routes[0].Handle[0].Upstreams[0].Dial
	if dial != "127.0.0.1:2283" {
		t.Errorf("dial = %q, want 127.0.0.1:2283", dial)
	}
}

func TestBuildAppRoutes_PathMatch(t *testing.T) {
	cfg := NetworkExposureConfig{BaseDomain: "nimosbarraca1.duckdns.org", Enabled: true}
	apps := []*NetworkExposedApp{
		{AppID: "gitea", Path: "/gitea", UpstreamHost: "127.0.0.1", UpstreamPort: 3000, Enabled: true},
	}
	routes := buildAppRoutes(cfg, apps)
	route := routes[0]
	if len(route.Match[0].Path) != 1 || route.Match[0].Path[0] != "/gitea*" {
		t.Errorf("path = %v, want [/gitea*]", route.Match[0].Path)
	}
	if len(route.Match[0].Host) != 1 || route.Match[0].Host[0] != "nimosbarraca1.duckdns.org" {
		t.Errorf("path route host = %v, want base domain", route.Match[0].Host)
	}
}

func TestBuildAppRoutes_DisabledAppOmitted(t *testing.T) {
	cfg := NetworkExposureConfig{BaseDomain: "x.duckdns.org", Enabled: true}
	apps := []*NetworkExposedApp{
		{AppID: "on", Subdomain: "on", UpstreamHost: "127.0.0.1", UpstreamPort: 1, Enabled: true},
		{AppID: "off", Subdomain: "off", UpstreamHost: "127.0.0.1", UpstreamPort: 2, Enabled: false},
	}
	routes := buildAppRoutes(cfg, apps)
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want 1 (disabled omitted)", len(routes))
	}
	if routes[0].Match[0].Host[0] != "on.x.duckdns.org" {
		t.Errorf("wrong route survived: %v", routes[0].Match[0].Host)
	}
}

func TestBuildAppRoutes_NoBaseDomainEmpty(t *testing.T) {
	cfg := NetworkExposureConfig{BaseDomain: "", Enabled: false}
	apps := []*NetworkExposedApp{
		{AppID: "immich", Subdomain: "immich", UpstreamHost: "127.0.0.1", UpstreamPort: 2283, Enabled: true},
	}
	routes := buildAppRoutes(cfg, apps)
	if len(routes) != 0 {
		t.Errorf("routes = %d, want 0 (no base domain)", len(routes))
	}
}

func TestNormalizeCaddyPath(t *testing.T) {
	cases := map[string]string{
		"/gitea": "/gitea*",
		"gitea":  "/gitea*",
		"/x*":    "/x*",
		"/a/b":   "/a/b*",
	}
	for in, want := range cases {
		if got := normalizeCaddyPath(in); got != want {
			t.Errorf("normalizeCaddyPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCaddyClient_SyncSuccess(t *testing.T) {
	var receivedBody []byte
	var receivedPath, receivedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedMethod = r.Method
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewCaddyAdminClient(srv.URL, srv.Client())
	routes := buildAppRoutes(
		NetworkExposureConfig{BaseDomain: "x.org", Enabled: true},
		[]*NetworkExposedApp{{AppID: "a", Subdomain: "a", UpstreamHost: "127.0.0.1", UpstreamPort: 80, Enabled: true}},
	)
	if err := client.SyncAppRoutes(context.Background(), routes); err != nil {
		t.Fatalf("SyncAppRoutes: %v", err)
	}
	if receivedMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", receivedMethod)
	}
	if !strings.Contains(receivedPath, "nimos_apps") {
		t.Errorf("path = %q, want to contain nimos_apps", receivedPath)
	}
	// El body debe ser un array de rutas válido.
	var parsed []caddyRoute
	if err := json.Unmarshal(receivedBody, &parsed); err != nil {
		t.Errorf("body not valid routes array: %v", err)
	}
	if len(parsed) != 1 {
		t.Errorf("route count in body = %d, want 1", len(parsed))
	}
}

func TestCaddyClient_SyncEmptyRoutes(t *testing.T) {
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewCaddyAdminClient(srv.URL, srv.Client())
	// Sin apps → array vacío, pero debe enviar [] no null.
	if err := client.SyncAppRoutes(context.Background(), buildAppRoutes(NetworkExposureConfig{BaseDomain: "x.org"}, nil)); err != nil {
		t.Fatalf("SyncAppRoutes empty: %v", err)
	}
	if strings.TrimSpace(string(receivedBody)) != "[]" {
		t.Errorf("empty routes body = %q, want []", receivedBody)
	}
}

func TestCaddyClient_SyncErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer srv.Close()

	client := NewCaddyAdminClient(srv.URL, srv.Client())
	err := client.SyncAppRoutes(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected error with body, got: %v", err)
	}
}

func TestCaddyClient_SyncMissingBaseGroup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("unknown object path /config/apps/http/servers/nimos/routes/@id/nimos_apps"))
	}))
	defer srv.Close()

	client := NewCaddyAdminClient(srv.URL, srv.Client())
	err := client.SyncAppRoutes(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "base config missing") {
		t.Errorf("expected base-missing hint, got: %v", err)
	}
}

func TestCaddyClient_SyncServerDown(t *testing.T) {
	client := NewCaddyAdminClient("http://127.0.0.1:59998", &http.Client{})
	if err := client.SyncAppRoutes(context.Background(), nil); err == nil {
		t.Error("expected error when Caddy is unreachable")
	}
}

func TestCaddyClient_FetchCertificates(t *testing.T) {
	payload := `{"result":[{"subjects":["immich.x.org"]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pki/certificates" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(payload))
	}))
	defer srv.Close()

	client := NewCaddyAdminClient(srv.URL, srv.Client())
	body, err := client.FetchCertificates(context.Background())
	if err != nil {
		t.Fatalf("FetchCertificates: %v", err)
	}
	if string(body) != payload {
		t.Errorf("body = %q, want %q", body, payload)
	}
}
