// network_exposure_observer_test.go — Tests del observer de certs Caddy.

package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// mockCertFetcher simula el cliente admin de Caddy para el observer.
//   · pingErr ≠ nil → Caddy "caído" (Ping falla)
//   · err ≠ nil     → Caddy vivo pero el endpoint de certs falla
type mockCertFetcher struct {
	payload []byte
	err     error
	pingErr error
}

func (m *mockCertFetcher) Ping(ctx context.Context) error {
	return m.pingErr
}

func (m *mockCertFetcher) FetchCertificates(ctx context.Context) ([]byte, error) {
	return m.payload, m.err
}

func newTestExposureObserver(t *testing.T, fetcher caddyCertFetcher) (*NetworkExposureObserver, *NetworkRepo, *sqlConn, func()) {
	t.Helper()
	repo, clock, c, cleanup := newTestRepo(t)
	obs := NewNetworkExposureObserver(repo, clock, DefaultNetworkExposureObserverConfig())
	obs.fetcherFor = func(adminURL string) caddyCertFetcher { return fetcher }
	return obs, repo, c, cleanup
}

func TestExposureObserver_ParsesWrappedResult(t *testing.T) {
	// El FakeClock del repo está fijado en 2026-05-21 12:00 UTC.
	clockNow := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	notAfter := clockNow.Add(67 * 24 * time.Hour).Unix()
	payload := fmt.Sprintf(`{"result":[{"subjects":["immich.x.org"],"issuer":"Let's Encrypt","not_after":%d,"managed":true}]}`, notAfter)

	obs, _, _, cleanup := newTestExposureObserver(t, &mockCertFetcher{payload: []byte(payload)})
	defer cleanup()

	if err := obs.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	snap := obs.Snapshot()
	if snap == nil || !snap.Reachable {
		t.Fatal("snapshot should be reachable")
	}
	if len(snap.Certs) != 1 {
		t.Fatalf("certs = %d, want 1", len(snap.Certs))
	}
	cert := snap.Certs[0]
	if cert.Subject != "immich.x.org" || cert.Issuer != "Let's Encrypt" || !cert.Managed {
		t.Errorf("cert mismatch: %+v", cert)
	}
	if cert.DaysLeft < 66 || cert.DaysLeft > 67 {
		t.Errorf("DaysLeft = %d, want ~67", cert.DaysLeft)
	}
}

func TestExposureObserver_ParsesDirectList(t *testing.T) {
	payload := `[{"subjects":["gitea.x.org"],"issuer":"ZeroSSL","not_after":0,"managed":true}]`
	obs, _, _, cleanup := newTestExposureObserver(t, &mockCertFetcher{payload: []byte(payload)})
	defer cleanup()

	if err := obs.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := obs.Snapshot()
	if len(snap.Certs) != 1 || snap.Certs[0].Subject != "gitea.x.org" {
		t.Errorf("direct list parse failed: %+v", snap.Certs)
	}
}

func TestExposureObserver_CaddyUnreachable(t *testing.T) {
	// Ping falla → Caddy caído de verdad → Reachable: false.
	obs, _, _, cleanup := newTestExposureObserver(t, &mockCertFetcher{pingErr: fmt.Errorf("connection refused")})
	defer cleanup()

	if err := obs.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile should not error on unreachable Caddy: %v", err)
	}
	snap := obs.Snapshot()
	if snap == nil {
		t.Fatal("snapshot should exist")
	}
	if snap.Reachable {
		t.Error("Reachable should be false when Caddy is down")
	}
	if len(snap.Certs) != 0 {
		t.Error("no certs when unreachable")
	}
}

func TestExposureObserver_AliveButCertsUnavailable(t *testing.T) {
	// El caso del bug del salpicadero: Caddy VIVO (Ping OK) pero el endpoint
	// de certs falla (404 /pki/certificates, sin certs aún…). El observer
	// debe reportar Reachable=TRUE con certs vacíos — NUNCA "Caddy caído".
	obs, _, _, cleanup := newTestExposureObserver(t, &mockCertFetcher{err: fmt.Errorf("status 404")})
	defer cleanup()

	if err := obs.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	snap := obs.Snapshot()
	if snap == nil {
		t.Fatal("snapshot should exist")
	}
	if !snap.Reachable {
		t.Error("Reachable should be TRUE when Caddy is alive (only certs endpoint failed)")
	}
	if len(snap.Certs) != 0 {
		t.Error("certs should be empty when cert endpoint fails")
	}
}

func TestExposureObserver_OmitsCertWithoutSubject(t *testing.T) {
	payload := `{"result":[{"subjects":[],"issuer":"x","not_after":0,"managed":true},{"subjects":["ok.x.org"],"not_after":0,"managed":true}]}`
	obs, _, _, cleanup := newTestExposureObserver(t, &mockCertFetcher{payload: []byte(payload)})
	defer cleanup()

	obs.Reconcile(context.Background())
	snap := obs.Snapshot()
	if len(snap.Certs) != 1 || snap.Certs[0].Subject != "ok.x.org" {
		t.Errorf("should omit cert without subject: %+v", snap.Certs)
	}
}

func TestExposureObserver_MalformedJSON(t *testing.T) {
	obs, _, _, cleanup := newTestExposureObserver(t, &mockCertFetcher{payload: []byte("not json at all")})
	defer cleanup()

	if err := obs.Reconcile(context.Background()); err != nil {
		t.Fatalf("should not error on malformed json: %v", err)
	}
	snap := obs.Snapshot()
	// Reachable=true (Caddy respondió) pero sin certs parseables.
	if !snap.Reachable {
		t.Error("Caddy responded, should be reachable")
	}
	if len(snap.Certs) != 0 {
		t.Error("malformed json yields no certs")
	}
}

func TestExposureObserver_NilSnapshotBeforeRun(t *testing.T) {
	obs, _, _, cleanup := newTestExposureObserver(t, &mockCertFetcher{})
	defer cleanup()
	if obs.Snapshot() != nil {
		t.Error("snapshot should be nil before first Reconcile")
	}
}

func TestExposureObserver_InterfaceMethods(t *testing.T) {
	obs, _, _, cleanup := newTestExposureObserver(t, &mockCertFetcher{})
	defer cleanup()
	if obs.Name() != "exposure_certs" {
		t.Errorf("Name = %q", obs.Name())
	}
	if obs.Tier() != TierLow {
		t.Errorf("Tier should be TierLow")
	}
	if obs.Interval() != 5*time.Minute {
		t.Errorf("Interval = %v, want 5m", obs.Interval())
	}
}
