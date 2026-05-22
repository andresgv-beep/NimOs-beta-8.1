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

	// DuckDNS provider con su breaker propio.
	duckBreaker := NewCircuitBreaker(DefaultBreakerConfig("ddns.duckdns"))
	duckProvider, err := NewDuckDNSProvider(DuckDNSProviderConfig{
		Breaker: duckBreaker,
	})
	if err != nil {
		return fmt.Errorf("initNetworkModule: build duckdns provider: %w", err)
	}
	networkDDNSReconciler.RegisterProvider(duckProvider)

	// Scheduler con observer + ddns reconciler.
	networkReconcilers = NewReconcilerScheduler(clock)
	if err := networkReconcilers.Register(networkObserver); err != nil {
		return fmt.Errorf("initNetworkModule: register observer: %w", err)
	}
	if err := networkReconcilers.Register(networkDDNSReconciler); err != nil {
		return fmt.Errorf("initNetworkModule: register ddns reconciler: %w", err)
	}

	// Verificación defensiva: probar una query trivial contra las tablas
	// network_*. Si el schema no está creado o la conexión está rota,
	// queremos saberlo aquí, no en el primer request HTTP.
	if _, err := networkRepo.CountObservedSnapshots(context.Background()); err != nil {
		return fmt.Errorf("initNetworkModule: defensive query failed: %w", err)
	}

	logMsg("Network module v4 ready (2 reconcilers: network_observer, ddns_updater)")
	return nil
}
