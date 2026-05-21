// network_boot.go — Inicialización del módulo network Beta 8.1 v4.
//
// Centraliza el arranque del nuevo stack network (Repo + EventEmitter +
// Scheduler + Observer) en una sola función llamada desde main.go.
//
// Orden de arranque del módulo:
//   1. openDB                   ← db.go
//   2. initNimosCoreSchema      ← nimos_core_schema.go (tablas globales)
//   3. initNetworkSchema        ← network_schema.go (tablas network_*)
//   4. initNetworkModule        ← este archivo
//
// Tras este punto:
//   - networkRepo:           CRUD de Ports/Ddns/Certs + audit tables
//   - networkEventEmitter:   Emit() con dedupe + rate limit
//   - networkProbe:          lee realidad del sistema (puertos, certs en disco)
//   - networkObserver:       singleton observer; registrado en el scheduler
//   - networkReconcilers:    scheduler con el observer ya registrado
//
// El scheduler NO se arranca automáticamente — el caller debe invocar
// networkReconcilers.Start(ctx) cuando esté listo (típicamente tras el
// HTTP server, para no observar mientras el daemon aún se inicializa).

package main

import (
	"context"
	"fmt"
)

// Singletons globales del módulo network.
var (
	networkRepo         *NetworkRepo
	networkEventEmitter *EventEmitter
	networkProbe        NetworkProbe
	networkObserver     *NetworkObserver
	networkReconcilers  *ReconcilerScheduler
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

	networkReconcilers = NewReconcilerScheduler(clock)
	if err := networkReconcilers.Register(networkObserver); err != nil {
		return fmt.Errorf("initNetworkModule: register observer: %w", err)
	}

	// Verificación defensiva: probar una query trivial contra las tablas
	// network_*. Si el schema no está creado o la conexión está rota,
	// queremos saberlo aquí, no en el primer request HTTP.
	if _, err := networkRepo.CountObservedSnapshots(context.Background()); err != nil {
		return fmt.Errorf("initNetworkModule: defensive query failed: %w", err)
	}

	logMsg("Network module v4 ready (1 reconciler registered: network_observer)")
	return nil
}
