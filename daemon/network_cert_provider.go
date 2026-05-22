// network_cert_provider.go — Interfaz CertProvider y tipos comunes.
//
// Cada proveedor de certificados (Let's Encrypt prod/staging,
// self-signed, ZeroSSL futuro, ...) implementa esta interfaz. El
// reconciler las consume sin acoplarse a una implementación concreta.
//
// F-005 entrega:
//   - F-005a: La interfaz + SelfSignedProvider (este archivo + selfsigned).
//   - F-005b: DNSChallengeProvider + DuckDNSChallengeProvider.
//   - F-005c: LetsEncryptProvider (ACME real).
//   - F-005d: CertReconciler.
//   - F-005e: HTTP handlers.
//
// Diseño deliberado:
//
//   - El provider NO toca DB ni filesystem en sus propios constructors.
//     Recibe parámetros del reconciler y devuelve material en memoria.
//     Quien escribe en disco es el reconciler, NO el provider — así
//     centralizamos paths, permisos, y atomic writes.
//
//   - El provider NO conoce el dominio de la entidad NetworkCert.
//     Recibe un CertRequest con todo lo que necesita.
//
//   - Los errores se clasifican vía sentinels (ErrCert*) para que el
//     reconciler decida si reintentar, abortar, o pedir intervención.

package main

import (
	"context"
	"errors"
)

// ─────────────────────────────────────────────────────────────────────────────
// Errors sentinels
// ─────────────────────────────────────────────────────────────────────────────

// ErrCertProviderUnknown se devuelve cuando el reconciler ve una fila
// network_certs con cert_provider="X" que no está registrado.
var ErrCertProviderUnknown = errors.New("cert provider not registered")

// ErrCertChallengeFailed indica que el proveedor ACME no pudo validar
// el control del dominio (DNS-01 incorrecto, HTTP-01 inalcanzable, etc).
// No abre breaker — es responsabilidad del usuario arreglar config.
var ErrCertChallengeFailed = errors.New("cert challenge failed")

// ErrCertProviderTransient marca fallos recuperables (timeout, 5xx,
// red caída). El breaker los cuenta y eventualmente abre.
var ErrCertProviderTransient = errors.New("cert provider transient failure")

// ErrCertProviderRateLimited indica que el proveedor ha rechazado por
// rate limit (típico en Let's Encrypt prod: 5 fallos/hora, 50 certs/sem).
// El reconciler debería esperar largo antes de reintentar.
var ErrCertProviderRateLimited = errors.New("cert provider rate limited")

// ─────────────────────────────────────────────────────────────────────────────
// Tipos comunes
// ─────────────────────────────────────────────────────────────────────────────

// CertRequest agrupa todo lo que un CertProvider necesita para emitir.
//
//   - Domain: el FQDN a certificar (e.g. "test.example.com"). Sin SAN
//     adicionales por ahora — un cert = un dominio. Si en F-005+
//     aparece necesidad de SAN, se añade como []string opcional.
//
//   - ChallengeType: solo relevante para providers ACME ("http-01" o
//     "dns-01"). Ignorado por SelfSigned.
//
//   - DNSChallenger: si ChallengeType="dns-01", el provider llamará
//     a este objeto para crear/borrar registros TXT durante la
//     validación. nil si http-01 o si el provider no es ACME.
type CertRequest struct {
	Domain        string
	ChallengeType string           // "http-01" | "dns-01" | "" para selfsigned
	DNSChallenger DNSChallengeProvider // solo si dns-01
}

// CertMaterial es lo que el provider devuelve tras emitir un cert.
//
//   - FullchainPEM: PEM con el cert + intermediates (formato estándar
//     usado por NGINX, Apache, etc.).
//   - PrivkeyPEM: clave privada en PEM.
//   - NotBefore/NotAfter: validez parsed del cert. Útil para que el
//     reconciler decida cuándo renovar sin tener que reparsear.
type CertMaterial struct {
	FullchainPEM []byte
	PrivkeyPEM   []byte
	NotBefore    int64 // unix seconds
	NotAfter     int64 // unix seconds
}

// ─────────────────────────────────────────────────────────────────────────────
// CertProvider
// ─────────────────────────────────────────────────────────────────────────────

// CertProvider es el contrato mínimo de un proveedor de certificados.
//
// IMPORTANTE: el provider NO escribe a disco, NO toca la DB, NO emite
// eventos. Es una función pura (ctx + request) → (material en memoria).
// El reconciler hace la persistencia.
type CertProvider interface {
	// Name devuelve el identificador estable del proveedor.
	// Debe matchear el valor en network_certs.cert_provider:
	//   "letsencrypt", "letsencrypt_staging", "selfsigned", ...
	Name() string

	// SupportsChallenge devuelve true si este provider entiende el
	// challenge dado. Ejemplo:
	//   SelfSigned:  todo false (no usa challenges).
	//   LetsEncrypt: true para "http-01" y "dns-01".
	//
	// El reconciler usa esto para validar config antes de llamar a Issue.
	SupportsChallenge(challenge string) bool

	// Issue emite o renueva un certificado para el dominio dado.
	//
	// Devuelve:
	//   - (material, nil) en éxito.
	//   - (nil, ErrCertChallengeFailed) si el dominio no se pudo validar.
	//   - (nil, ErrCertProviderTransient) si el provider falló de forma
	//     recuperable (red, 5xx).
	//   - (nil, ErrCertProviderRateLimited) si el provider nos blocked
	//     por límite.
	//   - (nil, otro error) para fallos inesperados.
	//
	// El context se DEBE respetar: cancelación → abortar limpiamente.
	Issue(ctx context.Context, req CertRequest) (*CertMaterial, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// DNSChallengeProvider
// ─────────────────────────────────────────────────────────────────────────────

// DNSChallengeProvider abstrae el manejo de records TXT para el
// challenge DNS-01 de ACME.
//
// Diseño:
//
//   - El CertProvider (LetsEncrypt) recibe un DNSChallengeProvider y
//     lo llama para crear/borrar el record TXT durante la validación.
//
//   - SetTXT/RemoveTXT son operaciones idempotentes: si el record ya
//     existe con el mismo valor, no error. Si no existe al borrar,
//     no error.
//
//   - El provider real (DuckDNS) usa el token DDNS para autenticar.
//     El reconciler resuelve el token de nimos_secrets y lo pasa al
//     constructor del provider, NO en cada llamada (DNS challenges
//     pueden hacer múltiples calls por emisión).
//
// F-005b implementa DuckDNSChallengeProvider. Otros se añaden cuando
// haya consumidor.
type DNSChallengeProvider interface {
	// Name devuelve el identificador estable: "duckdns", "cloudflare", etc.
	// Debe matchear network_certs.dns_provider.
	Name() string

	// SetTXT crea (o actualiza) un record TXT en el subdominio
	// "_acme-challenge.<domain>". El valor es el opaque token que
	// ACME pide.
	//
	// Idempotente: si ya existe con el mismo valor, no error.
	SetTXT(ctx context.Context, domain, value string) error

	// RemoveTXT borra el record TXT del subdominio _acme-challenge.
	// Idempotente: si no existe, no error.
	//
	// El CertProvider DEBE llamar a esto al final de cada validación
	// (sea exitosa o fallida) para no dejar records colgados.
	RemoveTXT(ctx context.Context, domain string) error
}
