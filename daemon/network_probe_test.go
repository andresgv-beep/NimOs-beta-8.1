// network_probe_test.go — Tests del NetworkProbe.
//
// Cubre:
//   - Ports: sin listeners inyectados → Listening=false.
//   - Ports: con listener fn que devuelve ok=true → todo OK.
//   - Ports: con listener fn que devuelve ok=false → Listening=false.
//   - Ports: ID desconocido → Listening=false.
//   - Cert: archivo no existe → Exists=false, sin parse.
//   - Cert: archivo existe pero contenido inválido → ParseError != nil.
//   - Cert: archivo válido → NotBefore/NotAfter parseados.
//
// Para los tests de cert generamos un cert autofirmado al vuelo usando
// crypto/x509 + crypto/rsa (stdlib pura, sin OpenSSL).

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// generateSelfSignedCert genera un cert PEM con NotBefore/NotAfter
// dados y lo escribe a fullchainPath. Devuelve error si algo falla.
func generateSelfSignedCert(fullchainPath string, notBefore, notAfter time.Time) error {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test.local"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return err
	}
	f, err := os.Create(fullchainPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
}

// ═════════════════════════════════════════════════════════════════════════════
// Ports
// ═════════════════════════════════════════════════════════════════════════════

func TestProbe_PortsNoListenersConfigured(t *testing.T) {
	p := NewRealNetworkProbe(NewFakeClock(time.Now()))
	res := p.Probe(nil, []PortProbeInput{{ID: "http"}, {ID: "https"}})

	if len(res.Ports) != 2 {
		t.Fatalf("got %d ports, want 2", len(res.Ports))
	}
	for _, port := range res.Ports {
		if port.Listening {
			t.Errorf("port %s: Listening=true, want false (no fn injected)", port.ID)
		}
	}
}

func TestProbe_PortsWithActiveListeners(t *testing.T) {
	p := NewRealNetworkProbe(NewFakeClock(time.Now()))
	p.HTTPListener = func() (int, string, bool) { return 8080, "0.0.0.0", true }
	p.HTTPSListener = func() (int, string, bool) { return 8443, "127.0.0.1", true }

	res := p.Probe(nil, []PortProbeInput{{ID: "http"}, {ID: "https"}})

	if len(res.Ports) != 2 {
		t.Fatalf("got %d ports", len(res.Ports))
	}
	for _, port := range res.Ports {
		if !port.Listening {
			t.Errorf("port %s: Listening=false", port.ID)
		}
		switch port.ID {
		case "http":
			if port.Port != 8080 || port.BindAddress != "0.0.0.0" {
				t.Errorf("http: got port=%d bind=%s, want 8080/0.0.0.0", port.Port, port.BindAddress)
			}
		case "https":
			if port.Port != 8443 || port.BindAddress != "127.0.0.1" {
				t.Errorf("https: got port=%d bind=%s, want 8443/127.0.0.1", port.Port, port.BindAddress)
			}
		}
	}
}

func TestProbe_PortsListenerReturnsNotOK(t *testing.T) {
	p := NewRealNetworkProbe(NewFakeClock(time.Now()))
	p.HTTPListener = func() (int, string, bool) { return 0, "", false }

	res := p.Probe(nil, []PortProbeInput{{ID: "http"}})

	if res.Ports[0].Listening {
		t.Error("ok=false should yield Listening=false")
	}
}

func TestProbe_PortsUnknownID(t *testing.T) {
	p := NewRealNetworkProbe(NewFakeClock(time.Now()))
	res := p.Probe(nil, []PortProbeInput{{ID: "ftp"}})

	if res.Ports[0].Listening {
		t.Error("unknown ID should yield Listening=false")
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Certs
// ═════════════════════════════════════════════════════════════════════════════

func TestProbe_CertMissingFiles(t *testing.T) {
	p := NewRealNetworkProbe(NewFakeClock(time.Now()))
	res := p.Probe([]CertProbeInput{{
		ID:            "c1",
		Domain:        "x.local",
		FullchainPath: "/nonexistent/fullchain.pem",
		PrivkeyPath:   "/nonexistent/privkey.pem",
	}}, nil)

	got := res.Certs[0]
	if got.FullchainExists || got.PrivkeyExists {
		t.Errorf("expected both missing, got fullchain=%v privkey=%v", got.FullchainExists, got.PrivkeyExists)
	}
	if !got.NotBefore.IsZero() || !got.NotAfter.IsZero() {
		t.Error("dates should be zero when fullchain missing")
	}
	if got.ParseError != nil {
		t.Errorf("ParseError should be nil for missing file, got %v", got.ParseError)
	}
}

func TestProbe_CertOnlyPrivkeyExists(t *testing.T) {
	tmp := t.TempDir()
	privkey := filepath.Join(tmp, "privkey.pem")
	os.WriteFile(privkey, []byte("dummy"), 0600)
	fullchain := filepath.Join(tmp, "fullchain.pem") // no creado

	p := NewRealNetworkProbe(NewFakeClock(time.Now()))
	res := p.Probe([]CertProbeInput{{
		ID:            "c1",
		FullchainPath: fullchain,
		PrivkeyPath:   privkey,
	}}, nil)

	got := res.Certs[0]
	if got.FullchainExists {
		t.Error("fullchain should not exist")
	}
	if !got.PrivkeyExists {
		t.Error("privkey should exist")
	}
}

func TestProbe_CertParsesValidPEM(t *testing.T) {
	tmp := t.TempDir()
	fullchain := filepath.Join(tmp, "fullchain.pem")
	privkey := filepath.Join(tmp, "privkey.pem")

	notBefore := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := generateSelfSignedCert(fullchain, notBefore, notAfter); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(privkey, []byte("dummy-key"), 0600)

	p := NewRealNetworkProbe(NewFakeClock(time.Now()))
	res := p.Probe([]CertProbeInput{{
		ID:            "c1",
		FullchainPath: fullchain,
		PrivkeyPath:   privkey,
	}}, nil)

	got := res.Certs[0]
	if !got.FullchainExists || !got.PrivkeyExists {
		t.Fatal("both files should exist")
	}
	if got.ParseError != nil {
		t.Fatalf("ParseError: %v", got.ParseError)
	}
	if !got.NotBefore.Equal(notBefore) {
		t.Errorf("NotBefore = %v, want %v", got.NotBefore, notBefore)
	}
	if !got.NotAfter.Equal(notAfter) {
		t.Errorf("NotAfter = %v, want %v", got.NotAfter, notAfter)
	}
}

func TestProbe_CertInvalidPEM(t *testing.T) {
	tmp := t.TempDir()
	fullchain := filepath.Join(tmp, "fullchain.pem")
	os.WriteFile(fullchain, []byte("not a valid PEM at all"), 0644)

	p := NewRealNetworkProbe(NewFakeClock(time.Now()))
	res := p.Probe([]CertProbeInput{{
		ID:            "c1",
		FullchainPath: fullchain,
		PrivkeyPath:   "/nonexistent",
	}}, nil)

	got := res.Certs[0]
	if !got.FullchainExists {
		t.Fatal("file exists")
	}
	if got.ParseError == nil {
		t.Error("expected ParseError for invalid content")
	}
	if !got.NotBefore.IsZero() {
		t.Error("NotBefore should be zero on parse fail")
	}
}

func TestProbe_CertCorruptDER(t *testing.T) {
	tmp := t.TempDir()
	fullchain := filepath.Join(tmp, "fullchain.pem")
	// PEM válido por fuera, DER inválido por dentro.
	corrupt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not-real-der")})
	os.WriteFile(fullchain, corrupt, 0644)

	p := NewRealNetworkProbe(NewFakeClock(time.Now()))
	res := p.Probe([]CertProbeInput{{
		ID:            "c1",
		FullchainPath: fullchain,
		PrivkeyPath:   "/nonexistent",
	}}, nil)

	got := res.Certs[0]
	if got.ParseError == nil {
		t.Error("expected ParseError for corrupt DER inside valid PEM")
	}
}

func TestProbe_ProbedAtUsesClock(t *testing.T) {
	frozen := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	clock := NewFakeClock(frozen)
	p := NewRealNetworkProbe(clock)
	res := p.Probe(nil, nil)
	if !res.ProbedAt.Equal(frozen) {
		t.Errorf("ProbedAt = %v, want %v", res.ProbedAt, frozen)
	}
}

func TestProbe_EmptyInputs(t *testing.T) {
	p := NewRealNetworkProbe(NewFakeClock(time.Now()))
	res := p.Probe(nil, nil)
	if len(res.Ports) != 0 || len(res.Certs) != 0 {
		t.Errorf("empty inputs: got %d ports, %d certs", len(res.Ports), len(res.Certs))
	}
}
