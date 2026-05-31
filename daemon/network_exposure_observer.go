// network_exposure_observer.go — Observa el estado de los certs de Caddy.
//
// NimOS NO gestiona los certs (eso lo hace Caddy: solicitud, renovación y
// reintentos ACME). Pero para dar buena UX, el control plane DEBE saber el
// estado de los certs y mostrarlo al usuario: "immich.dominio · válido ·
// expira en 67 días". Sin esto, el usuario no sabría si su HTTPS funciona
// hasta que algo se rompe.
//
// Este observer consulta periódicamente GET /pki/certificates de la API
// admin de Caddy, parsea la respuesta y publica un snapshot atómico
// (lock-free para los handlers HTTP). Si Caddy no responde, el snapshot
// queda marcado como "desconocido" — no es un fallo crítico.
//
// El snapshot se sirve por el endpoint de exposición para que la UI pinte
// el estado de cada cert junto a su app.

package main

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"
)

// ExposureCertStatus describe el estado de un cert gestionado por Caddy,
// derivado de /pki/certificates. Es lo que la UI muestra.
type ExposureCertStatus struct {
	Subject   string    `json:"subject"`    // dominio principal del cert
	Issuer    string    `json:"issuer"`     // "Let's Encrypt", etc.
	NotAfter  time.Time `json:"not_after"`  // expiración
	Managed   bool      `json:"managed"`    // gestionado por Caddy (ACME)
	DaysLeft  int       `json:"days_left"`  // días hasta expirar (puede ser <0)
}

// ExposureCertSnapshot es el último resultado del observer.
type ExposureCertSnapshot struct {
	ObservedAt time.Time            `json:"observed_at"`
	Reachable  bool                 `json:"reachable"` // ¿respondió Caddy?
	Certs      []ExposureCertStatus `json:"certs"`
}

// caddyCertFetcher es el subconjunto del cliente Caddy que el observer usa.
// Interfaz para mockear en tests.
type caddyCertFetcher interface {
	FetchCertificates(ctx context.Context) ([]byte, error)
}

// NetworkExposureObserver consulta Caddy y mantiene un snapshot atómico.
type NetworkExposureObserver struct {
	repo    *NetworkRepo
	clock   Clock
	config  NetworkExposureObserverConfig

	// fetcherFor crea un fetcher para la URL admin dada. Inyectable.
	fetcherFor func(adminURL string) caddyCertFetcher

	last atomic.Pointer[ExposureCertSnapshot]
}

// NetworkExposureObserverConfig agrupa parámetros tunables.
type NetworkExposureObserverConfig struct {
	Interval time.Duration
}

// DefaultNetworkExposureObserverConfig devuelve config de producción.
func DefaultNetworkExposureObserverConfig() NetworkExposureObserverConfig {
	return NetworkExposureObserverConfig{
		Interval: 5 * time.Minute, // los certs cambian lento; no hace falta más
	}
}

// NewNetworkExposureObserver construye el observer. clock nil → RealClock.
func NewNetworkExposureObserver(repo *NetworkRepo, clock Clock, config NetworkExposureObserverConfig) *NetworkExposureObserver {
	if clock == nil {
		clock = NewRealClock()
	}
	if config.Interval == 0 {
		config.Interval = DefaultNetworkExposureObserverConfig().Interval
	}
	o := &NetworkExposureObserver{
		repo:   repo,
		clock:  clock,
		config: config,
	}
	o.fetcherFor = func(adminURL string) caddyCertFetcher {
		return NewCaddyAdminClient(adminURL, nil)
	}
	return o
}

func (o *NetworkExposureObserver) Name() string            { return "exposure_certs" }
func (o *NetworkExposureObserver) Tier() ReconcilerTier    { return TierLow }
func (o *NetworkExposureObserver) Interval() time.Duration { return o.config.Interval }

// Snapshot devuelve el último estado observado, o nil si nunca corrió.
// Lectura lock-free — apta para handlers HTTP.
func (o *NetworkExposureObserver) Snapshot() *ExposureCertSnapshot {
	return o.last.Load()
}

// Reconcile (nombre por la interfaz Reconciler) ejecuta una observación.
// Consulta Caddy, parsea certs, publica snapshot. Nunca devuelve error
// fatal: si Caddy no responde, publica un snapshot "no alcanzable".
func (o *NetworkExposureObserver) Reconcile(ctx context.Context) error {
	cfg, err := o.repo.GetExposureConfig(ctx)
	if err != nil {
		// Sin config no podemos saber la URL admin. Snapshot vacío.
		o.publish(&ExposureCertSnapshot{ObservedAt: o.clock.Now().UTC(), Reachable: false})
		return nil
	}

	fetcher := o.fetcherFor(cfg.CaddyAdminURL)
	raw, ferr := fetcher.FetchCertificates(ctx)
	if ferr != nil {
		// Caddy caído o sin certs todavía: estado desconocido, no fatal.
		o.publish(&ExposureCertSnapshot{ObservedAt: o.clock.Now().UTC(), Reachable: false})
		return nil
	}

	certs := parseCaddyCertificates(raw, o.clock.Now())
	o.publish(&ExposureCertSnapshot{
		ObservedAt: o.clock.Now().UTC(),
		Reachable:  true,
		Certs:      certs,
	})
	return nil
}

func (o *NetworkExposureObserver) publish(s *ExposureCertSnapshot) {
	o.last.Store(s)
}

// ─────────────────────────────────────────────────────────────────────────────
// Parsing de /pki/certificates
// ─────────────────────────────────────────────────────────────────────────────

// caddyPKIResponse refleja el subset de la respuesta de Caddy que usamos.
// Caddy devuelve la lista bajo distintas formas según versión; soportamos
// tanto {"result":[...]} como [...] directo.
type caddyPKIResponse struct {
	Result []caddyPKICert `json:"result"`
}

type caddyPKICert struct {
	Subjects []string `json:"subjects"`
	Issuer   string   `json:"issuer"`
	NotAfter int64    `json:"not_after"` // unix seconds (Caddy lo da así)
	Managed  bool     `json:"managed"`
}

// parseCaddyCertificates convierte el JSON crudo de Caddy en una lista de
// ExposureCertStatus. Tolera ambos formatos (envuelto en "result" o lista
// directa) y entradas malformadas (las omite).
func parseCaddyCertificates(raw []byte, now time.Time) []ExposureCertStatus {
	var pkiCerts []caddyPKICert

	// Intento 1: {"result": [...]}.
	var wrapped caddyPKIResponse
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Result != nil {
		pkiCerts = wrapped.Result
	} else {
		// Intento 2: lista directa [...].
		_ = json.Unmarshal(raw, &pkiCerts)
	}

	out := make([]ExposureCertStatus, 0, len(pkiCerts))
	for _, c := range pkiCerts {
		subject := ""
		if len(c.Subjects) > 0 {
			subject = c.Subjects[0]
		}
		if subject == "" {
			continue // entrada sin dominio, inútil para la UI
		}
		notAfter := time.Time{}
		daysLeft := 0
		if c.NotAfter > 0 {
			notAfter = time.Unix(c.NotAfter, 0).UTC()
			daysLeft = int(notAfter.Sub(now).Hours() / 24)
		}
		out = append(out, ExposureCertStatus{
			Subject:  subject,
			Issuer:   c.Issuer,
			NotAfter: notAfter,
			Managed:  c.Managed,
			DaysLeft: daysLeft,
		})
	}
	return out
}
