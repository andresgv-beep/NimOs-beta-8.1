// network_probe.go — Acceso a la "realidad" del sistema para el observer.
//
// El NetworkObserver no toca syscalls ni archivos directamente: todo
// pasa por NetworkProbe. Esto permite:
//
//   - Tests sin abrir sockets reales ni escribir certs en disco.
//   - Substitución del probe en boot/recovery sin tocar el observer.
//   - Aislamiento de detalles de OS (rutas /proc, comportamiento de
//     parsers de cert).
//
// Lo que el probe debe responder (F-002 scope):
//
//   - ¿Qué ports HTTP/HTTPS está escuchando el daemon AHORA?
//     (no qué dice la DB que debería escuchar — qué escucha realmente)
//
//   - ¿Existen los archivos de cert? ¿Qué NotBefore/NotAfter tienen?
//     Esto permite detectar:
//       a) cert borrado externamente → divergence
//       b) cert renovado externamente con fecha distinta → divergence
//       c) cert corrupto → divergence + warning
//
// Lo que NO hace este probe (deferred):
//
//   - DDNS IP real (HTTP call al provider) → F-004.
//   - DNS challenge probing → F-005.
//   - UPnP port mapping check → F-007.
//   - Capability detection runtime → vive en nimos_capabilities.go.
//
// Un cambio externo en cualquiera de los puntos diferidos NO genera
// divergence en F-002. Eso es deliberado: cada reconciler concreto se
// hará cargo de "su" realidad cuando aparezca.

package main

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Tipos públicos
// ─────────────────────────────────────────────────────────────────────────────

// ProbedPort describe el estado real de un listener HTTP/HTTPS del
// daemon. ID coincide con el ID en la tabla network_ports ("http" o
// "https").
//
// Listening=false significa que el probe no detectó listener para ese
// ID, sea porque está desactivado o porque crashó.
type ProbedPort struct {
	ID          string // "http" o "https"
	Listening   bool
	Port        int    // puerto real escuchando (0 si !Listening)
	BindAddress string // "0.0.0.0", "127.0.0.1", etc (vacío si !Listening)
}

// ProbedCert describe el estado real de un certificado en disco. Los
// campos NotBefore/NotAfter están en zero value si Exists=false o si el
// parse falló (en cuyo caso ParseError lleva la razón).
type ProbedCert struct {
	ID            string // matches network_certs.id
	Domain        string
	FullchainPath string
	PrivkeyPath   string

	FullchainExists bool
	PrivkeyExists   bool

	// Parsed cert metadata (zero si !FullchainExists o ParseError != nil).
	NotBefore time.Time
	NotAfter  time.Time

	// ParseError es nil si el parse fue exitoso; no-nil con motivo si
	// FullchainExists=true pero el contenido no se pudo leer/parsear.
	ParseError error
}

// ProbeResult es lo que el probe devuelve en una pasada completa.
// El observer compara este snapshot con la DB para detectar divergencias.
type ProbeResult struct {
	ProbedAt time.Time
	Ports    []ProbedPort
	Certs    []ProbedCert
}

// ─────────────────────────────────────────────────────────────────────────────
// Interfaz
// ─────────────────────────────────────────────────────────────────────────────

// NetworkProbe abstrae el acceso a la realidad. La implementación real
// vive en este mismo archivo (RealNetworkProbe). Los tests usan un mock.
//
// El método Probe DEBE ser idempotente y rápido — el observer lo llama
// cada N segundos. No abrir conexiones de red ni operaciones largas.
type NetworkProbe interface {
	Probe(certInputs []CertProbeInput, portInputs []PortProbeInput) ProbeResult
}

// CertProbeInput le dice al probe qué certs evaluar (ID + paths).
// El observer rellena esto desde network_certs antes de llamar al probe.
type CertProbeInput struct {
	ID            string
	Domain        string
	FullchainPath string
	PrivkeyPath   string
}

// PortProbeInput le dice al probe qué ports evaluar (típicamente
// 'http' y 'https'). El observer lo rellena desde network_ports.
type PortProbeInput struct {
	ID string // "http" o "https"
}

// ─────────────────────────────────────────────────────────────────────────────
// Implementación real
// ─────────────────────────────────────────────────────────────────────────────

// RealNetworkProbe lee del sistema real. Es stateless — se puede crear
// una instancia compartida.
//
// HTTPListenerSource y HTTPSListenerSource son funciones inyectables
// que devuelven el estado actual del listener. En el daemon real, esto
// lo expondrá el HTTP server (algo como `getActiveListener(id)` que
// devuelva `port, bindAddr, ok`). Para el día de hoy, si no están
// configuradas, el probe devuelve Listening=false (forzando el observer
// a marcar drift, lo que es seguro).
type RealNetworkProbe struct {
	clock Clock

	// Inyectables. Si nil, el probe asume "no listening".
	HTTPListener  ListenerStateFn
	HTTPSListener ListenerStateFn
}

// ListenerStateFn devuelve el estado actual de un listener. Lo
// proveerá el HTTP server cuando esté integrado.
type ListenerStateFn func() (port int, bindAddress string, ok bool)

// NewRealNetworkProbe construye un probe real. clock nil → RealClock.
func NewRealNetworkProbe(clock Clock) *RealNetworkProbe {
	if clock == nil {
		clock = NewRealClock()
	}
	return &RealNetworkProbe{clock: clock}
}

// Probe ejecuta una pasada: examina cada port y cada cert pedidos.
func (p *RealNetworkProbe) Probe(certInputs []CertProbeInput, portInputs []PortProbeInput) ProbeResult {
	res := ProbeResult{
		ProbedAt: p.clock.Now().UTC(),
		Ports:    make([]ProbedPort, 0, len(portInputs)),
		Certs:    make([]ProbedCert, 0, len(certInputs)),
	}
	for _, pi := range portInputs {
		res.Ports = append(res.Ports, p.probePort(pi))
	}
	for _, ci := range certInputs {
		res.Certs = append(res.Certs, probeCertOnDisk(ci))
	}
	return res
}

// probePort consulta el listener inyectable para el ID dado.
func (p *RealNetworkProbe) probePort(pi PortProbeInput) ProbedPort {
	out := ProbedPort{ID: pi.ID}
	var fn ListenerStateFn
	switch pi.ID {
	case "http":
		fn = p.HTTPListener
	case "https":
		fn = p.HTTPSListener
	default:
		return out // listening=false
	}
	if fn == nil {
		return out
	}
	port, bind, ok := fn()
	if !ok {
		return out
	}
	out.Listening = true
	out.Port = port
	out.BindAddress = bind
	return out
}

// probeCertOnDisk es función libre (sin estado) — útil porque el
// probe puede llamarla y los tests también para construir fixtures
// con paths reales.
func probeCertOnDisk(ci CertProbeInput) ProbedCert {
	out := ProbedCert{
		ID:            ci.ID,
		Domain:        ci.Domain,
		FullchainPath: ci.FullchainPath,
		PrivkeyPath:   ci.PrivkeyPath,
	}

	// Existencia de archivos.
	if _, err := os.Stat(ci.FullchainPath); err == nil {
		out.FullchainExists = true
	}
	if _, err := os.Stat(ci.PrivkeyPath); err == nil {
		out.PrivkeyExists = true
	}

	if !out.FullchainExists {
		return out
	}

	// Parse de NotBefore/NotAfter.
	raw, err := os.ReadFile(ci.FullchainPath)
	if err != nil {
		out.ParseError = fmt.Errorf("read fullchain: %w", err)
		return out
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		out.ParseError = errors.New("fullchain: no PEM block found")
		return out
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		out.ParseError = fmt.Errorf("parse cert: %w", err)
		return out
	}
	out.NotBefore = cert.NotBefore.UTC()
	out.NotAfter = cert.NotAfter.UTC()
	return out
}
