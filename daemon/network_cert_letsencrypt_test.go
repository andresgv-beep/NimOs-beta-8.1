// network_cert_letsencrypt_test.go — Tests del LetsEncryptProvider.
//
// Estrategia:
//
//   Tests E2E completos contra un mock ACME serían 1500+ líneas y muy
//   frágiles (cualquier cambio en la librería x/crypto/acme rompería
//   los mocks). En la industria estos providers se testean contra
//   Pebble (servidor ACME de prueba de LE) o contra staging real.
//
//   Aquí cubrimos:
//   - Construcción + validación de config.
//   - Name() depende del config.
//   - SupportsChallenge devuelve solo dns-01.
//   - Validación de CertRequest (domain vacío, challenge incorrecto,
//     dnsChallenger nil).
//   - classifyACMEErr: tabla de casos input → sentinel.
//   - isTransientACME: tabla de casos.
//
// Los tests de flow E2E quedan pendientes para ejecutar contra
// LE staging cuando deployemos.

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"

	"golang.org/x/crypto/acme"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func mustGenAccountKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func buildLEProvider(t *testing.T, name, dirURL string) *LetsEncryptProvider {
	t.Helper()
	p, err := NewLetsEncryptProvider(LetsEncryptProviderConfig{
		Name:         name,
		DirectoryURL: dirURL,
		AccountKey:   mustGenAccountKey(t),
		Breaker:      NewCircuitBreaker(DefaultBreakerConfig("le-test")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// ═════════════════════════════════════════════════════════════════════════════
// Construction
// ═════════════════════════════════════════════════════════════════════════════

func TestLetsEncrypt_RequiresName(t *testing.T) {
	for _, badName := range []string{"", "wrong", "letsencryptv3"} {
		_, err := NewLetsEncryptProvider(LetsEncryptProviderConfig{
			Name:         badName,
			DirectoryURL: LetsEncryptStagingURL,
			AccountKey:   mustGenAccountKey(t),
			Breaker:      NewCircuitBreaker(DefaultBreakerConfig("x")),
		})
		if err == nil {
			t.Errorf("name %q: expected error", badName)
		}
	}
}

func TestLetsEncrypt_RequiresAccountKey(t *testing.T) {
	_, err := NewLetsEncryptProvider(LetsEncryptProviderConfig{
		Name:         "letsencrypt_staging",
		DirectoryURL: LetsEncryptStagingURL,
		AccountKey:   nil,
		Breaker:      NewCircuitBreaker(DefaultBreakerConfig("x")),
	})
	if err == nil {
		t.Error("expected error without AccountKey")
	}
}

func TestLetsEncrypt_RequiresBreaker(t *testing.T) {
	_, err := NewLetsEncryptProvider(LetsEncryptProviderConfig{
		Name:         "letsencrypt_staging",
		DirectoryURL: LetsEncryptStagingURL,
		AccountKey:   mustGenAccountKey(t),
		Breaker:      nil,
	})
	if err == nil {
		t.Error("expected error without Breaker")
	}
}

func TestLetsEncrypt_RequiresDirectoryURL(t *testing.T) {
	_, err := NewLetsEncryptProvider(LetsEncryptProviderConfig{
		Name:         "letsencrypt_staging",
		DirectoryURL: "",
		AccountKey:   mustGenAccountKey(t),
		Breaker:      NewCircuitBreaker(DefaultBreakerConfig("x")),
	})
	if err == nil {
		t.Error("expected error without DirectoryURL")
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Metadata
// ═════════════════════════════════════════════════════════════════════════════

func TestLetsEncrypt_NameMatchesConfig(t *testing.T) {
	cases := []struct {
		name string
	}{
		{"letsencrypt"},
		{"letsencrypt_staging"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := buildLEProvider(t, c.name, LetsEncryptStagingURL)
			if p.Name() != c.name {
				t.Errorf("Name = %q, want %q", p.Name(), c.name)
			}
		})
	}
}

func TestLetsEncrypt_OnlySupportsDNS01(t *testing.T) {
	p := buildLEProvider(t, "letsencrypt_staging", LetsEncryptStagingURL)
	if !p.SupportsChallenge("dns-01") {
		t.Error("dns-01 should be supported")
	}
	for _, c := range []string{"http-01", "tls-alpn-01", "", "unknown"} {
		if p.SupportsChallenge(c) {
			t.Errorf("%q should NOT be supported", c)
		}
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Issue — request validation (sin tocar la red)
// ═════════════════════════════════════════════════════════════════════════════

func TestLetsEncrypt_IssueRejectsEmptyDomain(t *testing.T) {
	p := buildLEProvider(t, "letsencrypt_staging", LetsEncryptStagingURL)
	_, err := p.Issue(context.Background(), CertRequest{
		Domain:        "",
		ChallengeType: "dns-01",
	})
	if err == nil {
		t.Error("expected error for empty domain")
	}
}

func TestLetsEncrypt_IssueRejectsWrongChallenge(t *testing.T) {
	p := buildLEProvider(t, "letsencrypt_staging", LetsEncryptStagingURL)
	_, err := p.Issue(context.Background(), CertRequest{
		Domain:        "test.example.com",
		ChallengeType: "http-01",
	})
	if err == nil {
		t.Error("expected error for http-01 (only dns-01 supported)")
	}
}

func TestLetsEncrypt_IssueRequiresDNSChallenger(t *testing.T) {
	p := buildLEProvider(t, "letsencrypt_staging", LetsEncryptStagingURL)
	_, err := p.Issue(context.Background(), CertRequest{
		Domain:        "test.example.com",
		ChallengeType: "dns-01",
		DNSChallenger: nil,
	})
	if err == nil {
		t.Error("expected error when DNSChallenger is nil")
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// classifyACMEErr
// ═════════════════════════════════════════════════════════════════════════════

func TestLetsEncrypt_ClassifyNil(t *testing.T) {
	p := buildLEProvider(t, "letsencrypt_staging", LetsEncryptStagingURL)
	if got := p.classifyACMEErr(nil); got != nil {
		t.Errorf("nil → %v, want nil", got)
	}
}

func TestLetsEncrypt_ClassifyPreservesSentinels(t *testing.T) {
	p := buildLEProvider(t, "letsencrypt_staging", LetsEncryptStagingURL)
	for _, sentinel := range []error{
		ErrCertChallengeFailed,
		ErrCertProviderTransient,
		ErrCertProviderRateLimited,
	} {
		if got := p.classifyACMEErr(sentinel); !errors.Is(got, sentinel) {
			t.Errorf("%v not preserved, got %v", sentinel, got)
		}
	}
}

func TestLetsEncrypt_ClassifyRateLimited(t *testing.T) {
	p := buildLEProvider(t, "letsencrypt_staging", LetsEncryptStagingURL)
	err := &acme.Error{
		StatusCode:  429,
		ProblemType: "urn:ietf:params:acme:error:rateLimited",
		Detail:      "too many requests",
	}
	if got := p.classifyACMEErr(err); !errors.Is(got, ErrCertProviderRateLimited) {
		t.Errorf("rateLimited not classified; got %v", got)
	}
}

func TestLetsEncrypt_ClassifyUnauthorized(t *testing.T) {
	p := buildLEProvider(t, "letsencrypt_staging", LetsEncryptStagingURL)
	err := &acme.Error{
		StatusCode:  403,
		ProblemType: "urn:ietf:params:acme:error:unauthorized",
		Detail:      "no auth",
	}
	if got := p.classifyACMEErr(err); !errors.Is(got, ErrCertChallengeFailed) {
		t.Errorf("unauthorized → %v, want ErrCertChallengeFailed", got)
	}
}

func TestLetsEncrypt_ClassifyDNSProblem(t *testing.T) {
	p := buildLEProvider(t, "letsencrypt_staging", LetsEncryptStagingURL)
	err := &acme.Error{
		StatusCode:  400,
		ProblemType: "urn:ietf:params:acme:error:dns",
		Detail:      "could not resolve TXT",
	}
	if got := p.classifyACMEErr(err); !errors.Is(got, ErrCertChallengeFailed) {
		t.Errorf("dns problem → %v, want ErrCertChallengeFailed", got)
	}
}

func TestLetsEncrypt_Classify5xxIsTransient(t *testing.T) {
	p := buildLEProvider(t, "letsencrypt_staging", LetsEncryptStagingURL)
	err := &acme.Error{
		StatusCode:  503,
		ProblemType: "urn:ietf:params:acme:error:serverInternal",
		Detail:      "down",
	}
	if got := p.classifyACMEErr(err); !errors.Is(got, ErrCertProviderTransient) {
		t.Errorf("503 → %v, want ErrCertProviderTransient", got)
	}
}

func TestLetsEncrypt_ClassifyContextErrorsPreserved(t *testing.T) {
	p := buildLEProvider(t, "letsencrypt_staging", LetsEncryptStagingURL)
	if got := p.classifyACMEErr(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Errorf("context.Canceled not preserved; got %v", got)
	}
	if got := p.classifyACMEErr(context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Errorf("DeadlineExceeded not preserved; got %v", got)
	}
}

func TestLetsEncrypt_ClassifyUnknownIsTransient(t *testing.T) {
	// Errores raros (e.g. de red) que no son *acme.Error ni context:
	// los tratamos como transient para que el breaker eventualmente abra.
	p := buildLEProvider(t, "letsencrypt_staging", LetsEncryptStagingURL)
	err := errors.New("connection reset by peer")
	if got := p.classifyACMEErr(err); !errors.Is(got, ErrCertProviderTransient) {
		t.Errorf("unknown net error → %v, want transient", got)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// isTransientACME
// ═════════════════════════════════════════════════════════════════════════════

func TestLetsEncrypt_IsTransient_Cases(t *testing.T) {
	p := buildLEProvider(t, "letsencrypt_staging", LetsEncryptStagingURL)
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"4xx auth", &acme.Error{StatusCode: 403}, false},
		{"4xx bad request", &acme.Error{StatusCode: 400}, false},
		{"5xx", &acme.Error{StatusCode: 503}, true},
		{"network error", errors.New("dial tcp: lookup failed"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.isTransientACME(c.err)
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
