// network_exposure_caddy_test.go — Tests del generador Caddy + cliente admin.
//
// Cubre:
//   - buildCaddyConfig: subdomain → match host.
//   - buildCaddyConfig: path → match path (+ host base).
//   - buildCaddyConfig: app disabled se omite.
//   - buildCaddyConfig: sin base_domain → server vacío (sin rutas).
//   - normalizeCaddyPath: añade / y *.
//   - Load: POST correcto al mock, 200 OK.
//   - Load: status != 200 → error con cuerpo.
//   - Load: servidor caído → error claro.
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

func TestBuildCaddyConfig_SubdomainMatchHost(t *testing.T) {
	cfg := NetworkExposureConfig{BaseDomain: "nimosbarraca1.duckdns.org", Enabled: true}
	apps := []*NetworkExposedApp{
		{AppID: "immich", Subdomain: "immich", UpstreamHost: "127.0.0.1", UpstreamPort: 2283, Enabled: true},
	}
	cc := buildCaddyConfig(cfg, apps)

	routes := cc.Apps.HTTP.Servers["nimos"].Routes
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

func TestBuildCaddyConfig_PathMatch(t *testing.T) {
	cfg := NetworkExposureConfig{BaseDomain: "nimosbarraca1.duckdns.org", Enabled: true}
	apps := []*NetworkExposedApp{
		{AppID: "gitea", Path: "/gitea", UpstreamHost: "127.0.0.1", UpstreamPort: 3000, Enabled: true},
	}
	cc := buildCaddyConfig(cfg, apps)
	route := cc.Apps.HTTP.Servers["nimos"].Routes[0]

	if len(route.Match[0].Path) != 1 || route.Match[0].Path[0] != "/gitea*" {
		t.Errorf("path = %v, want [/gitea*]", route.Match[0].Path)
	}
	// path-only debe llevar el host base para no capturar otros dominios.
	if len(route.Match[0].Host) != 1 || route.Match[0].Host[0] != "nimosbarraca1.duckdns.org" {
		t.Errorf("path route host = %v, want base domain", route.Match[0].Host)
	}
}

func TestBuildCaddyConfig_DisabledAppOmitted(t *testing.T) {
	cfg := NetworkExposureConfig{BaseDomain: "x.duckdns.org", Enabled: true}
	apps := []*NetworkExposedApp{
		{AppID: "on", Subdomain: "on", UpstreamHost: "127.0.0.1", UpstreamPort: 1, Enabled: true},
		{AppID: "off", Subdomain: "off", UpstreamHost: "127.0.0.1", UpstreamPort: 2, Enabled: false},
	}
	cc := buildCaddyConfig(cfg, apps)
	routes := cc.Apps.HTTP.Servers["nimos"].Routes
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want 1 (disabled omitted)", len(routes))
	}
	if routes[0].Match[0].Host[0] != "on.x.duckdns.org" {
		t.Errorf("wrong route survived: %v", routes[0].Match[0].Host)
	}
}

func TestBuildCaddyConfig_NoBaseDomainEmptyServer(t *testing.T) {
	cfg := NetworkExposureConfig{BaseDomain: "", Enabled: false}
	apps := []*NetworkExposedApp{
		{AppID: "immich", Subdomain: "immich", UpstreamHost: "127.0.0.1", UpstreamPort: 2283, Enabled: true},
	}
	cc := buildCaddyConfig(cfg, apps)
	server, ok := cc.Apps.HTTP.Servers["nimos"]
	if !ok {
		t.Fatal("nimos server should exist even without domain")
	}
	if len(server.Routes) != 0 {
		t.Errorf("routes = %d, want 0 (no base domain)", len(server.Routes))
	}
	if len(server.Listen) != 1 || server.Listen[0] != ":443" {
		t.Errorf("listen = %v, want [:443]", server.Listen)
	}
}

func TestNormalizeCaddyPath(t *testing.T) {
	cases := map[string]string{
		"/gitea":  "/gitea*",
		"gitea":   "/gitea*",
		"/x*":     "/x*",
		"/a/b":    "/a/b*",
	}
	for in, want := range cases {
		if got := normalizeCaddyPath(in); got != want {
			t.Errorf("normalizeCaddyPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCaddyClient_LoadSuccess(t *testing.T) {
	var receivedBody []byte
	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewCaddyAdminClient(srv.URL, srv.Client())
	cfg := buildCaddyConfig(
		NetworkExposureConfig{BaseDomain: "x.org", Enabled: true},
		[]*NetworkExposedApp{{AppID: "a", Subdomain: "a", UpstreamHost: "127.0.0.1", UpstreamPort: 80, Enabled: true}},
	)
	if err := client.Load(context.Background(), cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if receivedPath != "/load" {
		t.Errorf("path = %q, want /load", receivedPath)
	}
	// Verificar que el body es JSON Caddy válido.
	var parsed caddyConfig
	if err := json.Unmarshal(receivedBody, &parsed); err != nil {
		t.Errorf("body not valid caddy config: %v", err)
	}
	if len(parsed.Apps.HTTP.Servers["nimos"].Routes) != 1 {
		t.Error("route lost in serialization")
	}
}

func TestCaddyClient_LoadErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid config"))
	}))
	defer srv.Close()

	client := NewCaddyAdminClient(srv.URL, srv.Client())
	err := client.Load(context.Background(), caddyConfig{})
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if !strings.Contains(err.Error(), "invalid config") {
		t.Errorf("error should include body: %v", err)
	}
}

func TestCaddyClient_LoadServerDown(t *testing.T) {
	// URL a un puerto que nadie escucha.
	client := NewCaddyAdminClient("http://127.0.0.1:59999", &http.Client{})
	err := client.Load(context.Background(), caddyConfig{})
	if err == nil {
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
