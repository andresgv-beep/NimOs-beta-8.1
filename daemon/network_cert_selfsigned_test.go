// network_cert_selfsigned_test.go — Tests del SelfSignedProvider.
//
// Cubre:
//   - Construcción con defaults vs config personalizada.
//   - Name() y SupportsChallenge() devuelven valores estables.
//   - Issue genera cert PEM válido que se puede re-parsear.
//   - Domain se respeta en CN y SAN.
//   - NotBefore/NotAfter coinciden con clock + validity.
//   - Cert es self-signed: emisor == sujeto, IsCA == true.
//   - Context cancelado → error.
//   - Cada Issue genera material distinto (no reutiliza key).

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"
)

// tlsX509KeyPair es un alias para mantener la línea de test concisa.
var tlsX509KeyPair = tls.X509KeyPair

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// parseCertPEM parsea el cert PEM emitido y devuelve el *x509.Certificate.
// Lo usamos para verificar contenido sin reimplementar el parsing en cada test.
func parseCertPEM(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("could not decode PEM")
	}
	if block.Type != "CERTIFICATE" {
		t.Fatalf("PEM type = %q, want CERTIFICATE", block.Type)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return cert
}

// parseKeyPEM verifica que la clave es un PEM válido tipo RSA PRIVATE KEY.
func parseKeyPEM(t *testing.T, keyPEM []byte) {
	t.Helper()
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		t.Fatal("could not decode key PEM")
	}
	if block.Type != "RSA PRIVATE KEY" {
		t.Errorf("PEM type = %q, want RSA PRIVATE KEY", block.Type)
	}
	if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
		t.Errorf("parse RSA key: %v", err)
	}
}

// newTestSelfSigned construye un provider con clock fake y keyBits
// reducidos para acelerar tests (1024 vs 2048 — más rápido y aún válido
// criptográficamente para tests, NO usar en producción).
func newTestSelfSigned(t *testing.T, validity time.Duration) (*SelfSignedProvider, *FakeClock) {
	t.Helper()
	clock := NewFakeClock(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	p := NewSelfSignedProvider(SelfSignedProviderConfig{
		Clock:    clock,
		Validity: validity,
		KeyBits:  1024, // sólo para tests, NO producción
	})
	return p, clock
}

// ═════════════════════════════════════════════════════════════════════════════
// Construction & metadata
// ═════════════════════════════════════════════════════════════════════════════

func TestSelfSigned_NameIsStable(t *testing.T) {
	p, _ := newTestSelfSigned(t, 365*24*time.Hour)
	if p.Name() != "selfsigned" {
		t.Errorf("Name = %q, want selfsigned", p.Name())
	}
}

func TestSelfSigned_SupportsNoChallenges(t *testing.T) {
	p, _ := newTestSelfSigned(t, 365*24*time.Hour)
	for _, c := range []string{"http-01", "dns-01", "", "anything"} {
		if p.SupportsChallenge(c) {
			t.Errorf("SupportsChallenge(%q) = true, want false", c)
		}
	}
}

func TestSelfSigned_DefaultsApplied(t *testing.T) {
	p := NewSelfSignedProvider(SelfSignedProviderConfig{})
	if p.validity != 365*24*time.Hour {
		t.Errorf("validity default = %v, want 365d", p.validity)
	}
	if p.keyBits != 2048 {
		t.Errorf("keyBits default = %d, want 2048", p.keyBits)
	}
	if p.clock == nil {
		t.Error("clock should not be nil after defaults")
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Issue — happy paths
// ═════════════════════════════════════════════════════════════════════════════

func TestSelfSigned_IssueProducesValidPEMs(t *testing.T) {
	p, _ := newTestSelfSigned(t, 365*24*time.Hour)
	material, err := p.Issue(context.Background(), CertRequest{Domain: "test.example.com"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if material == nil {
		t.Fatal("material is nil")
	}
	parseCertPEM(t, material.FullchainPEM)
	parseKeyPEM(t, material.PrivkeyPEM)
}

func TestSelfSigned_IssueRespectsDomainInCNandSAN(t *testing.T) {
	p, _ := newTestSelfSigned(t, 365*24*time.Hour)
	material, err := p.Issue(context.Background(), CertRequest{Domain: "host.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCertPEM(t, material.FullchainPEM)
	if cert.Subject.CommonName != "host.example.org" {
		t.Errorf("CN = %q, want host.example.org", cert.Subject.CommonName)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "host.example.org" {
		t.Errorf("SAN = %v, want [host.example.org]", cert.DNSNames)
	}
}

func TestSelfSigned_IssueIsSelfSigned(t *testing.T) {
	p, _ := newTestSelfSigned(t, 365*24*time.Hour)
	material, err := p.Issue(context.Background(), CertRequest{Domain: "test.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCertPEM(t, material.FullchainPEM)

	// Issuer == Subject indica self-signed.
	if cert.Issuer.String() != cert.Subject.String() {
		t.Errorf("issuer != subject; got issuer=%v subject=%v", cert.Issuer, cert.Subject)
	}

	// IsCA == true.
	if !cert.IsCA {
		t.Error("IsCA = false, want true (self-signed acts as its own CA)")
	}

	// Verificar firma con su propia clave pública.
	if err := cert.CheckSignatureFrom(cert); err != nil {
		t.Errorf("cert does not verify against its own key: %v", err)
	}
}

func TestSelfSigned_IssueDatesUseClock(t *testing.T) {
	validity := 30 * 24 * time.Hour
	p, clock := newTestSelfSigned(t, validity)
	material, err := p.Issue(context.Background(), CertRequest{Domain: "x.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCertPEM(t, material.FullchainPEM)
	clockNow := clock.Now().UTC()

	// NotBefore debe estar muy cerca de clockNow (cushion de 1min).
	diff := cert.NotBefore.Sub(clockNow)
	if diff < -2*time.Minute || diff > 0 {
		t.Errorf("NotBefore offset from clock = %v, want ~-1min", diff)
	}

	// NotAfter = clockNow + validity (margen de unos segundos).
	expectedExpiry := clockNow.Add(validity)
	diff = cert.NotAfter.Sub(expectedExpiry)
	if diff < -5*time.Second || diff > 5*time.Second {
		t.Errorf("NotAfter = %v, want ~%v (diff %v)", cert.NotAfter, expectedExpiry, diff)
	}

	// Material.NotBefore/NotAfter (unix) deben coincidir con el cert.
	if material.NotBefore != cert.NotBefore.Unix() {
		t.Errorf("material.NotBefore mismatch with cert: %d vs %d", material.NotBefore, cert.NotBefore.Unix())
	}
	if material.NotAfter != cert.NotAfter.Unix() {
		t.Errorf("material.NotAfter mismatch: %d vs %d", material.NotAfter, cert.NotAfter.Unix())
	}
}

func TestSelfSigned_IssueIgnoresChallengeFields(t *testing.T) {
	// Even when caller sets challenge fields, selfsigned ignores them.
	p, _ := newTestSelfSigned(t, 365*24*time.Hour)
	material, err := p.Issue(context.Background(), CertRequest{
		Domain:        "x.example.com",
		ChallengeType: "http-01",
		DNSChallenger: nil, // selfsigned never uses this
	})
	if err != nil {
		t.Fatalf("Issue ignored challenge fields but errored: %v", err)
	}
	if material == nil {
		t.Error("material should be produced even with challenge fields set")
	}
}

func TestSelfSigned_IssueGeneratesDistinctMaterialEachTime(t *testing.T) {
	// Cada Issue debería generar serial y key distintos.
	p, _ := newTestSelfSigned(t, 365*24*time.Hour)
	mat1, _ := p.Issue(context.Background(), CertRequest{Domain: "x.example.com"})
	mat2, _ := p.Issue(context.Background(), CertRequest{Domain: "x.example.com"})

	cert1 := parseCertPEM(t, mat1.FullchainPEM)
	cert2 := parseCertPEM(t, mat2.FullchainPEM)

	if cert1.SerialNumber.Cmp(cert2.SerialNumber) == 0 {
		t.Error("two Issues produced same serial number")
	}
	if string(mat1.PrivkeyPEM) == string(mat2.PrivkeyPEM) {
		t.Error("two Issues produced same private key")
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Issue — error paths
// ═════════════════════════════════════════════════════════════════════════════

func TestSelfSigned_IssueRejectsEmptyDomain(t *testing.T) {
	p, _ := newTestSelfSigned(t, 365*24*time.Hour)
	_, err := p.Issue(context.Background(), CertRequest{Domain: ""})
	if err == nil {
		t.Error("expected error for empty domain")
	}
}

func TestSelfSigned_IssueRespectsCancelledContext(t *testing.T) {
	p, _ := newTestSelfSigned(t, 365*24*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Issue(ctx, CertRequest{Domain: "x.example.com"})
	if err == nil {
		t.Error("expected error on cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Issued cert is loadable by tls.Certificate (smoke test)
// ═════════════════════════════════════════════════════════════════════════════

func TestSelfSigned_MaterialUsableByTLS(t *testing.T) {
	// crypto/tls.X509KeyPair valida cert + key parsean Y son par
	// criptográfico válido. Si nginx/Go pueden cargar el material,
	// el contrato está bien.
	p, _ := newTestSelfSigned(t, 365*24*time.Hour)
	material, err := p.Issue(context.Background(), CertRequest{Domain: "tls.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tlsX509KeyPair(material.FullchainPEM, material.PrivkeyPEM)
	if err != nil {
		t.Errorf("tls.X509KeyPair failed: %v", err)
	}
}
