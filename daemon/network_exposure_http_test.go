// network_exposure_http_test.go — Tests de /api/v4/network/exposure.

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var exposureHTTPTestMu sync.Mutex

func setupExposureHTTPTest(t *testing.T) (token string, cleanup func()) {
	t.Helper()
	exposureHTTPTestMu.Lock()

	prevDB := db
	prevRepo := networkRepo
	prevObs := networkExposureObserver

	c, dbCleanup := setupNetworkDB(t)
	db = c.db
	networkRepo = NewNetworkRepo(c.db, NewFakeClock(time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)))
	networkExposureObserver = nil

	if _, err := c.db.Exec(`
		CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY,
			username TEXT NOT NULL,
			role TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			ip TEXT
		)
	`); err != nil {
		t.Fatal(err)
	}
	tokenBytes := make([]byte, 16)
	rand.Read(tokenBytes)
	token = hex.EncodeToString(tokenBytes)
	hashed := sha256Hex(token)
	c.db.Exec(`INSERT INTO sessions (token, username, role, created_at, expires_at, ip)
		VALUES (?, 'test-admin', 'admin', ?, ?, '127.0.0.1')`,
		hashed, time.Now().UnixMilli(), time.Now().Add(time.Hour).UnixMilli())

	cleanup = func() {
		networkExposureObserver = prevObs
		networkRepo = prevRepo
		db = prevDB
		dbCleanup()
		exposureHTTPTestMu.Unlock()
	}
	return token, cleanup
}

func doExposureReq(t *testing.T, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handleNetworkExposureRoutes(rr, req)
	return rr
}

func TestExposureHTTP_RequiresAuth(t *testing.T) {
	_, cleanup := setupExposureHTTPTest(t)
	defer cleanup()

	rr := doExposureReq(t, "", "GET", "/api/v4/network/exposure", "")
	if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusForbidden {
		t.Errorf("status=%d, want 401/403", rr.Code)
	}
}

func TestExposureHTTP_ConfigGetDefault(t *testing.T) {
	token, cleanup := setupExposureHTTPTest(t)
	defer cleanup()

	rr := doExposureReq(t, token, "GET", "/api/v4/network/exposure/config", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Config NetworkExposureConfig `json:"config"`
	}
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Config.Enabled {
		t.Error("default config should be disabled")
	}
}

func TestExposureHTTP_ConfigPutAndGet(t *testing.T) {
	token, cleanup := setupExposureHTTPTest(t)
	defer cleanup()

	body := `{"base_domain":"nimosbarraca1.duckdns.org","enabled":true}`
	rr := doExposureReq(t, token, "PUT", "/api/v4/network/exposure/config", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = doExposureReq(t, token, "GET", "/api/v4/network/exposure/config", "")
	var resp struct {
		Config NetworkExposureConfig `json:"config"`
	}
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Config.BaseDomain != "nimosbarraca1.duckdns.org" || !resp.Config.Enabled {
		t.Errorf("config not persisted: %+v", resp.Config)
	}
}

func TestExposureHTTP_ConfigEnableWithoutDomainRejected(t *testing.T) {
	token, cleanup := setupExposureHTTPTest(t)
	defer cleanup()

	body := `{"enabled":true}`
	rr := doExposureReq(t, token, "PUT", "/api/v4/network/exposure/config", body)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400 (enable without domain)", rr.Code)
	}
}

func TestExposureHTTP_CreateAndList(t *testing.T) {
	token, cleanup := setupExposureHTTPTest(t)
	defer cleanup()

	body := `{"app_id":"immich","display_name":"Immich","subdomain":"immich","upstream_host":"127.0.0.1","upstream_port":2283}`
	rr := doExposureReq(t, token, "POST", "/api/v4/network/exposure", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = doExposureReq(t, token, "GET", "/api/v4/network/exposure", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status=%d", rr.Code)
	}
	var resp struct {
		Apps []*NetworkExposedApp `json:"apps"`
	}
	json.NewDecoder(rr.Body).Decode(&resp)
	if len(resp.Apps) != 1 || resp.Apps[0].AppID != "immich" {
		t.Errorf("list mismatch: %+v", resp.Apps)
	}
}

func TestExposureHTTP_CreateValidations(t *testing.T) {
	token, cleanup := setupExposureHTTPTest(t)
	defer cleanup()

	cases := []struct {
		name string
		body string
	}{
		{"no app_id", `{"subdomain":"x","upstream_host":"127.0.0.1","upstream_port":80}`},
		{"no route", `{"app_id":"x","upstream_host":"127.0.0.1","upstream_port":80}`},
		{"no host", `{"app_id":"x","subdomain":"x","upstream_port":80}`},
		{"bad port", `{"app_id":"x","subdomain":"x","upstream_host":"127.0.0.1","upstream_port":0}`},
	}
	for _, tc := range cases {
		rr := doExposureReq(t, token, "POST", "/api/v4/network/exposure", tc.body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: status=%d, want 400", tc.name, rr.Code)
		}
	}
}

func TestExposureHTTP_DuplicateConflict(t *testing.T) {
	token, cleanup := setupExposureHTTPTest(t)
	defer cleanup()

	body := `{"app_id":"immich","subdomain":"immich","upstream_host":"127.0.0.1","upstream_port":2283}`
	doExposureReq(t, token, "POST", "/api/v4/network/exposure", body)
	rr := doExposureReq(t, token, "POST", "/api/v4/network/exposure", body)
	if rr.Code != http.StatusConflict {
		t.Errorf("status=%d, want 409 (duplicate)", rr.Code)
	}
}

func TestExposureHTTP_GetUpdateDelete(t *testing.T) {
	token, cleanup := setupExposureHTTPTest(t)
	defer cleanup()

	// Create.
	body := `{"app_id":"immich","subdomain":"immich","upstream_host":"127.0.0.1","upstream_port":2283}`
	rr := doExposureReq(t, token, "POST", "/api/v4/network/exposure", body)
	var created struct {
		App *NetworkExposedApp `json:"app"`
	}
	json.NewDecoder(rr.Body).Decode(&created)
	id := created.App.ID

	// Get.
	rr = doExposureReq(t, token, "GET", "/api/v4/network/exposure/"+id, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status=%d", rr.Code)
	}

	// Update (cambiar puerto + disable).
	upd := `{"upstream_port":2284,"enabled":false}`
	rr = doExposureReq(t, token, "PUT", "/api/v4/network/exposure/"+id, upd)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = doExposureReq(t, token, "GET", "/api/v4/network/exposure/"+id, "")
	var got struct {
		App *NetworkExposedApp `json:"app"`
	}
	json.NewDecoder(rr.Body).Decode(&got)
	if got.App.UpstreamPort != 2284 || got.App.Enabled {
		t.Errorf("update not applied: %+v", got.App)
	}

	// Delete.
	rr = doExposureReq(t, token, "DELETE", "/api/v4/network/exposure/"+id, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE status=%d", rr.Code)
	}
	rr = doExposureReq(t, token, "GET", "/api/v4/network/exposure/"+id, "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("after delete GET status=%d, want 404", rr.Code)
	}
}

func TestExposureHTTP_GetNotFound(t *testing.T) {
	token, cleanup := setupExposureHTTPTest(t)
	defer cleanup()

	rr := doExposureReq(t, token, "GET", "/api/v4/network/exposure/no-existe", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", rr.Code)
	}
}
