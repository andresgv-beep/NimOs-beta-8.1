// network_cert_selfsigned.go — Implementación de CertProvider para
// certificados auto-firmados.
//
// Uso típico:
//   - Bootstrap inicial cuando todavía no se puede usar ACME (DNS no
//     resuelve, puerto 80 no abierto, etc).
//   - Entornos de desarrollo / LAN privada.
//   - Fallback temporal mientras se renueva un cert con ACME que falla.
//
// El cert generado:
//   - RSA 2048 bits (default razonable, compatible con todo).
//     ECDSA P-256 también sería válido pero RSA tiene compatibilidad
//     más amplia con clientes antiguos. Si en F-005+ surge la necesidad
//     de configurarlo, se añade.
//   - Validez 365 días.
//   - CN = domain, SAN = [domain].
//   - Self-signed (CA = subject).
//   - SHA-256 signature (default actual de crypto/x509).
//
// Limitaciones explícitas:
//   - No genera un chain real — el "fullchain" es el solo cert raíz
//     auto-firmado. NGINX lo acepta sin problemas, pero los navegadores
//     mostrarán advertencia (esperado en self-signed).
//   - No reutiliza la private key entre renovaciones (cada Issue
//     genera key nueva). Si quisiéramos pinear keys habría que
//     persistir y rotar, lo cual añade complejidad sin ganancia
//     real para self-signed.

package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Provider
// ─────────────────────────────────────────────────────────────────────────────

// SelfSignedProvider implementa CertProvider con auto-firmado.
//
// No tiene breaker porque es offline (crypto/x509 puro). Sus errores
// son siempre permanentes — no hay nada que reintentar si rand/x509
// falla.
type SelfSignedProvider struct {
	clock Clock

	// Validity es la duración del cert. Default 365d. Configurable
	// para tests (e.g. cert que expira en 1s).
	validity time.Duration

	// KeyBits es el tamaño de la clave RSA. Default 2048.
	keyBits int
}

// SelfSignedProviderConfig configura el constructor.
type SelfSignedProviderConfig struct {
	Clock    Clock          // nil → RealClock
	Validity time.Duration  // 0 → 365 days
	KeyBits  int            // 0 → 2048
}

// NewSelfSignedProvider construye el provider con defaults razonables.
func NewSelfSignedProvider(cfg SelfSignedProviderConfig) *SelfSignedProvider {
	if cfg.Clock == nil {
		cfg.Clock = NewRealClock()
	}
	if cfg.Validity == 0 {
		cfg.Validity = 365 * 24 * time.Hour
	}
	if cfg.KeyBits == 0 {
		cfg.KeyBits = 2048
	}
	return &SelfSignedProvider{
		clock:    cfg.Clock,
		validity: cfg.Validity,
		keyBits:  cfg.KeyBits,
	}
}

// Name implementa CertProvider.
func (p *SelfSignedProvider) Name() string { return "selfsigned" }

// SupportsChallenge implementa CertProvider. SelfSigned no usa
// challenges — siempre devuelve false.
func (p *SelfSignedProvider) SupportsChallenge(_ string) bool { return false }

// Issue implementa CertProvider. Genera un cert auto-firmado para el
// dominio dado.
//
// req.ChallengeType y req.DNSChallenger se ignoran. El context se
// respeta (cancelar antes de la generación aborta).
func (p *SelfSignedProvider) Issue(ctx context.Context, req CertRequest) (*CertMaterial, error) {
	if req.Domain == "" {
		return nil, errors.New("selfsigned: domain is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Generar clave privada.
	priv, err := rsa.GenerateKey(rand.Reader, p.keyBits)
	if err != nil {
		return nil, fmt.Errorf("selfsigned: generate key: %w", err)
	}

	// Serial number aleatorio (CA/Browser Forum lo recomienda).
	serialMax := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialMax)
	if err != nil {
		return nil, fmt.Errorf("selfsigned: serial: %w", err)
	}

	now := p.clock.Now().UTC()
	notBefore := now.Add(-1 * time.Minute) // 1min de cushion por skew
	notAfter := now.Add(p.validity)

	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: req.Domain,
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},

		BasicConstraintsValid: true,
		IsCA:                  true, // auto-firmado: es su propia CA

		DNSNames: []string{req.Domain},
	}

	// Self-signed: emisor y sujeto son el mismo template.
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("selfsigned: create cert: %w", err)
	}

	// Codificar a PEM.
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})

	keyDER := x509.MarshalPKCS1PrivateKey(priv)
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: keyDER,
	})

	return &CertMaterial{
		FullchainPEM: certPEM,
		PrivkeyPEM:   keyPEM,
		NotBefore:    notBefore.Unix(),
		NotAfter:     notAfter.Unix(),
	}, nil
}
