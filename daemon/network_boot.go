// network_boot.go — Inicialización del módulo network Beta 8.1 v4.
//
// Centraliza el arranque del nuevo stack network (Repo + EventEmitter +
// Scheduler + Observer + Secrets + DDNS) en una sola función llamada
// desde main.go.
//
// Orden de arranque del módulo:
//   1. openDB                   ← db.go
//   2. initNimosCoreSchema      ← nimos_core_schema.go (tablas globales)
//   3. initNetworkSchema        ← network_schema.go (tablas network_*)
//   4. initNetworkModule        ← este archivo
//
// Tras este punto, los siguientes singletons quedan disponibles:
//   - networkRepo:           CRUD de Ports/Ddns/Certs + audit tables
//   - networkEventEmitter:   Emit() con dedupe + rate limit
//   - networkSecretsStore:   Cifrado de tokens DDNS y similares
//   - networkProbe:          lee realidad del sistema (puertos, certs)
//   - networkObserver:       singleton observer; registrado en scheduler
//   - networkDDNSReconciler: reconciler DDNS con providers registrados
//   - networkReconcilers:    scheduler con observer + ddns_updater
//
// El scheduler NO se arranca automáticamente — el caller (main.go)
// debe invocar networkReconcilers.Start(ctx) cuando esté listo.

package main

import (
	"context"
	"fmt"
)

// Singletons globales del módulo network.
var (
	networkRepo            *NetworkRepo
	networkEventEmitter    *EventEmitter
	networkSecretsStore    *SecretsStore
	networkProbe           NetworkProbe
	networkObserver        *NetworkObserver
	networkDDNSReconciler  *DDNSReconciler
	networkCertReconciler  *CertReconciler
	networkReconcilers     *ReconcilerScheduler
)

// initNetworkModule inicializa el módulo network v4.
// Debe llamarse DESPUÉS de initNimosCoreSchema() e initNetworkSchema()
// (tablas creadas) y ANTES de cualquier código que use los singletons.
func initNetworkModule() error {
	if db == nil {
		return fmt.Errorf("initNetworkModule: db is nil (call openDB first)")
	}

	// Clock real para producción. Tests inyectan FakeClock vía construcción
	// directa de los structs.
	clock := NewRealClock()

	networkRepo = NewNetworkRepo(db, clock)
	networkEventEmitter = NewEventEmitter(db, clock, DefaultEventEmitterConfig())

	// SecretsStore: carga (o crea) la master key desde el path canónico.
	store, err := NewSecretsStore(db, DefaultMasterKeyPath, clock)
	if err != nil {
		return fmt.Errorf("initNetworkModule: build secrets store: %w", err)
	}
	networkSecretsStore = store

	// Probe real. Las funciones HTTPListener/HTTPSListener se inyectarán
	// cuando F-003 wirees el HTTP server — hasta entonces, el probe
	// reporta los listeners como no-listening, lo que es seguro: el
	// observer NO marcará drift porque los ports aún tienen applied=0.
	networkProbe = NewRealNetworkProbe(clock)

	obs, err := NewNetworkObserver(networkRepo, networkEventEmitter,
		networkProbe, clock, DefaultObserverConfig())
	if err != nil {
		return fmt.Errorf("initNetworkModule: build observer: %w", err)
	}
	networkObserver = obs

	// DDNS Reconciler con sus providers.
	ddnsRec, err := NewDDNSReconciler(networkRepo, networkSecretsStore,
		networkEventEmitter, clock, DefaultDDNSReconcilerConfig())
	if err != nil {
		return fmt.Errorf("initNetworkModule: build ddns reconciler: %w", err)
	}
	networkDDNSReconciler = ddnsRec

	// DuckDNS update provider (para el reconciler DDNS).
	duckUpdateBreaker := NewCircuitBreaker(DefaultBreakerConfig("ddns.duckdns"))
	duckUpdateProvider, err := NewDuckDNSProvider(DuckDNSProviderConfig{
		Breaker: duckUpdateBreaker,
	})
	if err != nil {
		return fmt.Errorf("initNetworkModule: build duckdns provider: %w", err)
	}
	networkDDNSReconciler.RegisterProvider(duckUpdateProvider)

	// Cert Reconciler con su factory de DNSChallengeProviders.
	// La factory crea el challenger correcto según el nombre del DNS
	// provider y el token DDNS descifrado.
	dnsChallengerFactory := func(name, token string) (DNSChallengeProvider, error) {
		switch name {
		case "duckdns":
			// Breaker separado del de update — son interacciones distintas
			// con el mismo servidor pero con expectativas de fallo y
			// frecuencia distintas (DNS challenge se invoca solo al
			// renovar cert, update se invoca cada 15min).
			breaker := NewCircuitBreaker(DefaultBreakerConfig("ddns.duckdns.challenge"))
			return NewDuckDNSChallengeProvider(DuckDNSChallengeProviderConfig{
				Token:   token,
				Breaker: breaker,
			})
		default:
			return nil, fmt.Errorf("DNS challenger %q not implemented", name)
		}
	}

	certRec, err := NewCertReconciler(networkRepo, networkSecretsStore,
		networkEventEmitter, clock, dnsChallengerFactory,
		DefaultCertReconcilerConfig())
	if err != nil {
		return fmt.Errorf("initNetworkModule: build cert reconciler: %w", err)
	}
	networkCertReconciler = certRec

	// SelfSigned provider (sin red, sin breaker).
	selfsignedProvider := NewSelfSignedProvider(SelfSignedProviderConfig{Clock: clock})
	networkCertReconciler.RegisterProvider(selfsignedProvider)

	// Let's Encrypt providers: prod + staging. Cada uno con su breaker
	// y su account key (compartida entre prod y staging porque ACME
	// usa la pubkey para identificar la cuenta, pero los CAs distintos
	// tienen registros distintos — la misma key se puede registrar en
	// ambos).
	acctKey, err := LoadOrCreateACMEAccountKey(DefaultACMEAccountKeyPath)
	if err != nil {
		// No-fatal: si no se puede cargar la account key, los CertProviders
		// ACME no funcionarán, pero el resto del módulo sí. SelfSigned
		// queda como fallback. Log y seguimos.
		logMsg("Network module: ACME account key unavailable (%v); ACME providers not registered", err)
	} else {
		leStagingBreaker := NewCircuitBreaker(DefaultBreakerConfig("cert.letsencrypt_staging"))
		leStaging, err := NewLetsEncryptProvider(LetsEncryptProviderConfig{
			Name:         "letsencrypt_staging",
			DirectoryURL: LetsEncryptStagingURL,
			AccountKey:   acctKey,
			Breaker:      leStagingBreaker,
			Clock:        clock,
		})
		if err == nil {
			networkCertReconciler.RegisterProvider(leStaging)
		} else {
			logMsg("Network module: register letsencrypt_staging: %v", err)
		}

		leProdBreaker := NewCircuitBreaker(DefaultBreakerConfig("cert.letsencrypt"))
		leProd, err := NewLetsEncryptProvider(LetsEncryptProviderConfig{
			Name:         "letsencrypt",
			DirectoryURL: LetsEncryptProdURL,
			AccountKey:   acctKey,
			Breaker:      leProdBreaker,
			Clock:        clock,
		})
		if err == nil {
			networkCertReconciler.RegisterProvider(leProd)
		} else {
			logMsg("Network module: register letsencrypt: %v", err)
		}
	}

	// Scheduler con observer + ddns reconciler + cert reconciler.
	networkReconcilers = NewReconcilerScheduler(clock)
	if err := networkReconcilers.Register(networkObserver); err != nil {
		return fmt.Errorf("initNetworkModule: register observer: %w", err)
	}
	if err := networkReconcilers.Register(networkDDNSReconciler); err != nil {
		return fmt.Errorf("initNetworkModule: register ddns reconciler: %w", err)
	}
	if err := networkReconcilers.Register(networkCertReconciler); err != nil {
		return fmt.Errorf("initNetworkModule: register cert reconciler: %w", err)
	}

	// Verificación defensiva: probar una query trivial contra las tablas
	// network_*. Si el schema no está creado o la conexión está rota,
	// queremos saberlo aquí, no en el primer request HTTP.
	if _, err := networkRepo.CountObservedSnapshots(context.Background()); err != nil {
		return fmt.Errorf("initNetworkModule: defensive query failed: %w", err)
	}

	logMsg("Network module v4 ready (3 reconcilers: network_observer, ddns_updater, cert_renewer)")
	return nil
}
