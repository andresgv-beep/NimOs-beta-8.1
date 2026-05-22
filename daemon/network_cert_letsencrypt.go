// network_cert_letsencrypt.go — Implementación de CertProvider con
// ACME real (Let's Encrypt y compatibles).
//
// Usa golang.org/x/crypto/acme (RFC 8555, ACME v2). El mismo paquete
// que Caddy. Cero dependencias externas más allá de Go stdlib.
//
// Soporte actual:
//   - DNS-01 challenge únicamente. HTTP-01 puede añadirse cuando aparezca
//     consumidor (requiere que el daemon escuche en puerto 80, decisión
//     aparte).
//   - Una clave del cert por emisión (no reutilizamos keys entre
//     renovaciones; ACME no lo requiere y simplifica el código).
//   - Account key compartida entre todos los certs del daemon, vive en
//     network_cert_acme_account.go.
//
// Endpoints estándar de Let's Encrypt:
//   - Staging: https://acme-staging-v02.api.letsencrypt.org/directory
//   - Prod:    https://acme-v02.api.letsencrypt.org/directory
//
// Errores que el reconciler debe entender:
//   - ErrCertChallengeFailed:  el dominio no se validó (DNS no resuelve,
//                              TXT no publicado, etc.). NO transient.
//   - ErrCertProviderTransient: red, 5xx, breaker abierto. Reintentable.
//   - ErrCertProviderRateLimited: el CA nos blockeó. Reintentar lento.
//   - otros errors: no se asume nada — el reconciler lo trata como
//     permanente para evitar spam.

package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constants
// ─────────────────────────────────────────────────────────────────────────────

// LetsEncryptStagingURL es el endpoint de Let's Encrypt staging,
// usado para tests. Rate limits ~100x más permisivos.
const LetsEncryptStagingURL = "https://acme-staging-v02.api.letsencrypt.org/directory"

// LetsEncryptProdURL es el endpoint de Let's Encrypt producción.
const LetsEncryptProdURL = "https://acme-v02.api.letsencrypt.org/directory"

// ─────────────────────────────────────────────────────────────────────────────
// LetsEncryptProvider
// ─────────────────────────────────────────────────────────────────────────────

// LetsEncryptProvider implementa CertProvider con un servidor ACME.
//
// El nombre del provider depende del endpoint: "letsencrypt" para prod
// o "letsencrypt_staging" para staging. Se pasa explícito al constructor
// para que el caller indique cuál es. NO lo derivamos del URL porque eso
// crearía un acoplamiento frágil.
type LetsEncryptProvider struct {
	client   *acme.Client
	breaker  *CircuitBreaker
	clock    Clock
	name     string // "letsencrypt" | "letsencrypt_staging"
	tosAgree bool   // SIEMPRE true en práctica — Let's Encrypt requiere ToS
	email    string // contacto opcional registrado en la cuenta

	// dnsPropagationDelay es lo que dormimos tras SetTXT antes de
	// notificar al CA. Para DuckDNS es prácticamente instantáneo (segundos)
	// pero el polling DNS dentro del CA hace su propia espera.
	// Defaults a 0 — confiamos en que WaitAuthorization hace polling
	// hasta que el record se vea.
	dnsPropagationDelay time.Duration
}

// LetsEncryptProviderConfig agrupa los parámetros del constructor.
type LetsEncryptProviderConfig struct {
	// Name del provider; debe matchear network_certs.cert_provider:
	//   "letsencrypt" o "letsencrypt_staging".
	Name string

	// DirectoryURL del CA ACME. LetsEncryptStagingURL o LetsEncryptProdURL.
	DirectoryURL string

	// AccountKey ECDSA P-256. Obtenida vía LoadOrCreateACMEAccountKey.
	AccountKey *ecdsa.PrivateKey

	// Breaker obligatorio para envolver llamadas al CA.
	Breaker *CircuitBreaker

	// Email opcional registrado en la cuenta ACME. Let's Encrypt lo usa
	// para mandar avisos de expiración. Si vacío, se registra sin email.
	Email string

	// Clock para tests. nil → RealClock.
	Clock Clock

	// DNSPropagationDelay opcional. Default 0 (deja a ACME hacer polling).
	DNSPropagationDelay time.Duration
}

// NewLetsEncryptProvider construye el provider.
func NewLetsEncryptProvider(cfg LetsEncryptProviderConfig) (*LetsEncryptProvider, error) {
	if cfg.Name != "letsencrypt" && cfg.Name != "letsencrypt_staging" {
		return nil, fmt.Errorf("invalid name %q (expected letsencrypt | letsencrypt_staging)", cfg.Name)
	}
	if cfg.DirectoryURL == "" {
		return nil, errors.New("DirectoryURL is required")
	}
	if cfg.AccountKey == nil {
		return nil, errors.New("AccountKey is required")
	}
	if cfg.Breaker == nil {
		return nil, errors.New("Breaker is required")
	}
	if cfg.Clock == nil {
		cfg.Clock = NewRealClock()
	}
	client := &acme.Client{
		Key:          cfg.AccountKey,
		DirectoryURL: cfg.DirectoryURL,
	}
	return &LetsEncryptProvider{
		client:              client,
		breaker:             cfg.Breaker,
		clock:               cfg.Clock,
		name:                cfg.Name,
		tosAgree:            true,
		email:               cfg.Email,
		dnsPropagationDelay: cfg.DNSPropagationDelay,
	}, nil
}

// Name implementa CertProvider.
func (p *LetsEncryptProvider) Name() string { return p.name }

// SupportsChallenge implementa CertProvider. Solo DNS-01 por ahora.
func (p *LetsEncryptProvider) SupportsChallenge(challenge string) bool {
	return challenge == "dns-01"
}

// Issue implementa CertProvider. Ejecuta el flow ACME completo.
//
// req.ChallengeType debe ser "dns-01" y req.DNSChallenger no-nil.
// El provider:
//   1. Asegura que la cuenta está registrada (idempotente).
//   2. Crea un Order para el dominio.
//   3. Resuelve el challenge DNS-01 vía el DNSChallenger inyectado.
//   4. Finaliza el order y descarga el cert + chain.
//
// SIEMPRE intenta RemoveTXT al final (sea exitoso o no) para no dejar
// records colgados en DNS.
func (p *LetsEncryptProvider) Issue(ctx context.Context, req CertRequest) (*CertMaterial, error) {
	// Validaciones de entrada.
	if req.Domain == "" {
		return nil, errors.New("letsencrypt: domain is required")
	}
	if req.ChallengeType != "dns-01" {
		return nil, fmt.Errorf("letsencrypt: only dns-01 challenge supported, got %q", req.ChallengeType)
	}
	if req.DNSChallenger == nil {
		return nil, errors.New("letsencrypt: DNSChallenger is required for dns-01")
	}

	// Asegurar registro (idempotente; si ya existe no error).
	if err := p.ensureRegistered(ctx); err != nil {
		return nil, p.classifyACMEErr(err)
	}

	// Crear order.
	order, err := p.callBreaker(func() (any, error) {
		return p.client.AuthorizeOrder(ctx, acme.DomainIDs(req.Domain))
	})
	if err != nil {
		return nil, p.classifyACMEErr(err)
	}
	o := order.(*acme.Order)

	// Resolver cada authorization. En nuestro caso solo hay una
	// (un dominio = un authz).
	defer p.cleanupAllChallenges(ctx, req.DNSChallenger, req.Domain) // siempre limpiar
	for _, authzURL := range o.AuthzURLs {
		if err := p.solveAuthorization(ctx, authzURL, req.DNSChallenger, req.Domain); err != nil {
			return nil, err // ya viene clasificado
		}
	}

	// Esperar a que el order esté "ready" (todas las authorizations OK).
	orderAny, err := p.callBreaker(func() (any, error) {
		return p.client.WaitOrder(ctx, o.URI)
	})
	if err != nil {
		return nil, p.classifyACMEErr(err)
	}
	o = orderAny.(*acme.Order)

	// Generar key del cert + CSR.
	certKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("letsencrypt: generate cert key: %w", err)
	}
	csrTemplate := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: req.Domain},
		DNSNames: []string{req.Domain},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, certKey)
	if err != nil {
		return nil, fmt.Errorf("letsencrypt: create CSR: %w", err)
	}

	// Finalize: pide al CA que firme el CSR. Devuelve el cert + chain.
	certResult, err := p.callBreaker(func() (any, error) {
		ders, _, err := p.client.CreateOrderCert(ctx, o.FinalizeURL, csrDER, true) // bundle=true → incluye chain
		return ders, err
	})
	if err != nil {
		return nil, p.classifyACMEErr(err)
	}
	ders := certResult.([][]byte)
	if len(ders) == 0 {
		return nil, errors.New("letsencrypt: CA returned empty cert chain")
	}

	// Codificar fullchain (cert + intermediates) a PEM.
	var fullchainPEM []byte
	for _, der := range ders {
		fullchainPEM = append(fullchainPEM, pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: der,
		})...)
	}

	// Codificar privkey.
	keyDER := x509.MarshalPKCS1PrivateKey(certKey)
	privkeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: keyDER,
	})

	// Parsear el cert principal para extraer NotBefore/NotAfter.
	leaf, err := x509.ParseCertificate(ders[0])
	if err != nil {
		return nil, fmt.Errorf("letsencrypt: parse issued cert: %w", err)
	}

	return &CertMaterial{
		FullchainPEM: fullchainPEM,
		PrivkeyPEM:   privkeyPEM,
		NotBefore:    leaf.NotBefore.Unix(),
		NotAfter:     leaf.NotAfter.Unix(),
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internals
// ─────────────────────────────────────────────────────────────────────────────

// ensureRegistered registra la cuenta si no lo está ya. Idempotente.
// ACME devuelve un error específico si ya existe; lo tratamos como OK.
func (p *LetsEncryptProvider) ensureRegistered(ctx context.Context) error {
	acct := &acme.Account{}
	if p.email != "" {
		acct.Contact = []string{"mailto:" + p.email}
	}
	_, err := p.callBreaker(func() (any, error) {
		return p.client.Register(ctx, acct, func(string) bool { return p.tosAgree })
	})
	if err != nil {
		// "account already exists" es OK: ya estaba registrada.
		if errors.Is(err, acme.ErrAccountAlreadyExists) {
			return nil
		}
		// Otros errores se propagan tal cual.
		return err
	}
	return nil
}

// solveAuthorization gestiona el ciclo completo de un challenge DNS-01
// para una authorization concreta.
func (p *LetsEncryptProvider) solveAuthorization(ctx context.Context, authzURL string, dns DNSChallengeProvider, domain string) error {
	authzAny, err := p.callBreaker(func() (any, error) {
		return p.client.GetAuthorization(ctx, authzURL)
	})
	if err != nil {
		return p.classifyACMEErr(err)
	}
	authz := authzAny.(*acme.Authorization)

	// Si ya está válida (renovación rápida), no hacemos challenge.
	if authz.Status == acme.StatusValid {
		return nil
	}

	// Buscar challenge dns-01.
	var dnsChallenge *acme.Challenge
	for _, ch := range authz.Challenges {
		if ch.Type == "dns-01" {
			dnsChallenge = ch
			break
		}
	}
	if dnsChallenge == nil {
		return fmt.Errorf("letsencrypt: dns-01 challenge not offered for %s", domain)
	}

	// Calcular el valor TXT que esperamos publicar.
	txtValue, err := p.client.DNS01ChallengeRecord(dnsChallenge.Token)
	if err != nil {
		return fmt.Errorf("letsencrypt: compute DNS-01 record: %w", err)
	}

	// Publicar TXT.
	if err := dns.SetTXT(ctx, domain, txtValue); err != nil {
		return err // ya clasificado por el DNSChallenger
	}

	// Opcional: sleep antes de notificar al CA.
	if p.dnsPropagationDelay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.dnsPropagationDelay):
		}
	}

	// Aceptar challenge (le decimos al CA que valide).
	_, err = p.callBreaker(func() (any, error) {
		return p.client.Accept(ctx, dnsChallenge)
	})
	if err != nil {
		return p.classifyACMEErr(err)
	}

	// Esperar resultado.
	finalAny, err := p.callBreaker(func() (any, error) {
		return p.client.WaitAuthorization(ctx, authzURL)
	})
	if err != nil {
		return p.classifyACMEErr(err)
	}
	final := finalAny.(*acme.Authorization)

	if final.Status != acme.StatusValid {
		return ErrCertChallengeFailed
	}
	return nil
}

// cleanupAllChallenges intenta borrar el TXT record. Best-effort: si
// falla loguea pero no propaga error (no queremos enmascarar el error
// real del Issue).
func (p *LetsEncryptProvider) cleanupAllChallenges(ctx context.Context, dns DNSChallengeProvider, domain string) {
	// Usar context separado por si el original ya está cancelado.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := dns.RemoveTXT(cleanupCtx, domain); err != nil {
		logMsg("letsencrypt cleanup: RemoveTXT(%s): %v", domain, err)
	}
}

// callBreaker envuelve una llamada que devuelve (any, error) en el breaker.
// Necesario porque CircuitBreaker.Call solo acepta funciones que devuelven error.
func (p *LetsEncryptProvider) callBreaker(fn func() (any, error)) (any, error) {
	var result any
	var inner error
	err := p.breaker.Call(func() error {
		result, inner = fn()
		// Solo errores transient deben contar como fallos del breaker.
		if inner != nil && p.isTransientACME(inner) {
			return inner
		}
		return nil
	})
	if errors.Is(err, ErrCircuitOpen) {
		return nil, ErrCertProviderTransient
	}
	if inner != nil {
		return nil, inner
	}
	if err != nil {
		// El fn devolvió un error que no era transient pero el breaker
		// lo recibió como error. Devolvemos sin clasificar.
		return nil, err
	}
	return result, nil
}

// isTransientACME determina si un error de ACME debe contar como
// fallo transient (red, 5xx). Usado para decidir si el breaker debe
// contarlo.
func (p *LetsEncryptProvider) isTransientACME(err error) bool {
	if err == nil {
		return false
	}
	// Errores de tipo *acme.Error con código 5xx son transient.
	var aerr *acme.Error
	if errors.As(err, &aerr) {
		return aerr.StatusCode >= 500
	}
	// Errores de red genéricos suelen incluir "connection refused",
	// "no such host", "timeout", etc. No los enumeramos todos —
	// si no es *acme.Error de 4xx, lo tratamos como transient.
	return true
}

// classifyACMEErr traduce errores de la librería ACME a sentinels nuestros.
func (p *LetsEncryptProvider) classifyACMEErr(err error) error {
	if err == nil {
		return nil
	}
	// Ya viene clasificado (e.g. devuelto por DNSChallenger).
	if errors.Is(err, ErrCertChallengeFailed) ||
		errors.Is(err, ErrCertProviderTransient) ||
		errors.Is(err, ErrCertProviderRateLimited) {
		return err
	}

	var aerr *acme.Error
	if errors.As(err, &aerr) {
		// Detectar rate limit por código tipo Let's Encrypt.
		if strings.Contains(aerr.ProblemType, "rateLimited") {
			return ErrCertProviderRateLimited
		}
		// Auth/identifier/dns errors → challenge failed.
		if strings.Contains(aerr.ProblemType, "unauthorized") ||
			strings.Contains(aerr.ProblemType, "dns") ||
			strings.Contains(aerr.ProblemType, "incorrectResponse") {
			return ErrCertChallengeFailed
		}
		// 5xx → transient.
		if aerr.StatusCode >= 500 {
			return ErrCertProviderTransient
		}
		// 4xx no clasificado → permanente, sin clasificar.
		return err
	}
	// Errores de red / cancelación / etc. → transient.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err // dejar pasar tal cual
	}
	return ErrCertProviderTransient
}

// _ = crypto.Hash(0) // dummy to ensure crypto stays imported
var _ crypto.Signer = (*ecdsa.PrivateKey)(nil)
var _ = elliptic.P256
