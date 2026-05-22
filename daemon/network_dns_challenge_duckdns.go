// network_dns_challenge_duckdns.go — Implementación de
// DNSChallengeProvider para DuckDNS.
//
// DuckDNS expone un endpoint específico para records TXT:
//
//   https://www.duckdns.org/update?domains=<sub>&token=<tok>&txt=<value>
//
// Y para borrar:
//
//   https://www.duckdns.org/update?domains=<sub>&token=<tok>&txt=removed&clear=true
//
// Notas importantes:
//
//   - DuckDNS NO permite múltiples records TXT por dominio: una sola
//     entrada por subdominio. Esto es suficiente para ACME DNS-01
//     porque solo necesitamos un record _acme-challenge.<domain> con
//     el valor que ACME nos da.
//
//   - Tiempo de propagación: DuckDNS suele actualizar inmediato, pero
//     ACME requiere que el record sea visible cuando llega el reto.
//     La librería ACME que usemos en F-005c hará polling de DNS hasta
//     que aparezca, así que el provider no necesita esperar aquí.
//
//   - Aunque ACME usa el FQDN "_acme-challenge.<dominio>", DuckDNS no
//     necesita ese prefijo: el record se setea bajo el subdominio
//     principal. ACME verifica resolución DNS, así que mientras
//     "_acme-challenge.foo.duckdns.org" devuelva el valor, OK.
//
//   - El token reutiliza el mismo formato que el DDNS update normal.
//     Si el usuario ya tiene un token DDNS para foo.duckdns.org, ese
//     mismo token sirve para gestionar el TXT.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// DuckDNSChallengeProvider
// ─────────────────────────────────────────────────────────────────────────────

// DuckDNSChallengeProvider implementa DNSChallengeProvider para DuckDNS.
//
// IMPORTANTE: este provider recibe el token en el constructor, no en
// cada llamada. Esto es porque ACME hace múltiples calls (set, espera,
// remove) durante una emisión, y queremos descifrar el token una vez.
//
// El caller (CertReconciler) lee el token de nimos_secrets y construye
// el provider justo antes de pasarlo al CertProvider. Tras la emisión,
// el provider se descarta — no se reutiliza entre emisiones distintas.
type DuckDNSChallengeProvider struct {
	httpClient *http.Client
	breaker    *CircuitBreaker
	endpoint   string
	token      string
}

// DuckDNSChallengeProviderConfig configura el constructor.
type DuckDNSChallengeProviderConfig struct {
	// Token DuckDNS plaintext. Obligatorio. El reconciler lo obtiene
	// de nimos_secrets.
	Token string

	// HTTPClient inyectable para tests.
	HTTPClient *http.Client

	// Breaker obligatorio (mismo registry global que el DDNS updater).
	Breaker *CircuitBreaker

	// Endpoint override para tests (default: real DuckDNS).
	Endpoint string
}

// NewDuckDNSChallengeProvider construye el provider.
func NewDuckDNSChallengeProvider(cfg DuckDNSChallengeProviderConfig) (*DuckDNSChallengeProvider, error) {
	if cfg.Token == "" {
		return nil, errors.New("NewDuckDNSChallengeProvider: Token is required")
	}
	if cfg.Breaker == nil {
		return nil, errors.New("NewDuckDNSChallengeProvider: Breaker is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultDuckDNSEndpoint
	}
	return &DuckDNSChallengeProvider{
		httpClient: cfg.HTTPClient,
		breaker:    cfg.Breaker,
		endpoint:   cfg.Endpoint,
		token:      cfg.Token,
	}, nil
}

// Name implementa DNSChallengeProvider.
func (p *DuckDNSChallengeProvider) Name() string { return "duckdns" }

// SetTXT implementa DNSChallengeProvider. Idempotente.
func (p *DuckDNSChallengeProvider) SetTXT(ctx context.Context, domain, value string) error {
	if value == "" {
		return errors.New("duckdns challenge: empty TXT value")
	}
	sub, err := duckdnsSubdomain(domain)
	if err != nil {
		return err
	}

	q := url.Values{}
	q.Set("domains", sub)
	q.Set("token", p.token)
	q.Set("txt", value)
	// IMPORTANTE: NO incluir clear=true en set.
	reqURL := p.endpoint + "?" + q.Encode()

	return p.doRequest(ctx, reqURL)
}

// RemoveTXT implementa DNSChallengeProvider. Idempotente.
func (p *DuckDNSChallengeProvider) RemoveTXT(ctx context.Context, domain string) error {
	sub, err := duckdnsSubdomain(domain)
	if err != nil {
		return err
	}

	q := url.Values{}
	q.Set("domains", sub)
	q.Set("token", p.token)
	q.Set("txt", "removed")
	q.Set("clear", "true")
	reqURL := p.endpoint + "?" + q.Encode()

	return p.doRequest(ctx, reqURL)
}

// ─────────────────────────────────────────────────────────────────────────────
// Internals
// ─────────────────────────────────────────────────────────────────────────────

// doRequest hace la llamada HTTP wrapped en el breaker. DuckDNS responde
// "OK" o "KO" igual que el endpoint de update. La semántica es la misma
// que en network_ddns_duckdns.go:
//   - "OK" → success.
//   - "KO" → token rechazado (no es transient, no abre breaker).
//   - 5xx / red → transient (abre breaker).
//   - cualquier otro → error no-transient.
func (p *DuckDNSChallengeProvider) doRequest(ctx context.Context, reqURL string) error {
	var providerErr error

	breakerErr := p.breaker.Call(func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			providerErr = fmt.Errorf("duckdns challenge: build request: %w", err)
			return providerErr
		}
		resp, err := p.httpClient.Do(req)
		if err != nil {
			providerErr = ErrCertProviderTransient
			return ErrCertProviderTransient
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 500 {
			providerErr = ErrCertProviderTransient
			return ErrCertProviderTransient
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			providerErr = ErrCertProviderTransient
			return ErrCertProviderTransient
		}

		if resp.StatusCode >= 400 {
			providerErr = fmt.Errorf("duckdns challenge: HTTP %d: %s",
				resp.StatusCode, strings.TrimSpace(string(body)))
			return nil // no transient — no abrir breaker
		}

		trimmed := strings.TrimSpace(string(body))
		switch trimmed {
		case "OK":
			return nil
		case "KO":
			providerErr = ErrCertChallengeFailed
			return nil // no abrir breaker (es auth, no fallo del provider)
		default:
			providerErr = fmt.Errorf("duckdns challenge: unexpected response %q", trimmed)
			return nil
		}
	})

	if errors.Is(breakerErr, ErrCircuitOpen) {
		return ErrCertProviderTransient
	}
	if providerErr != nil {
		return providerErr
	}
	if breakerErr != nil {
		return ErrCertProviderTransient
	}
	return nil
}
