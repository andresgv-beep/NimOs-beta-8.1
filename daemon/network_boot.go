// network_boot.go — Inicialización del módulo network Beta 8.1 v4.
//
// Centraliza el arranque del nuevo stack network (Repo + EventEmitter +
// Scheduler) en una sola función llamada desde main.go.
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
//   - networkReconcilers:    scheduler (vacío hasta F-004+)

package main

import (
	"context"
	"fmt"
)

// Singletons globales del módulo network.
var (
	networkRepo          *NetworkRepo
	networkEventEmitter  *EventEmitter
	networkReconcilers   *ReconcilerScheduler
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
	networkReconcilers = NewReconcilerScheduler(clock)

	// Verificación defensiva: probar una query trivial contra las tablas
	// network_*. Si el schema no está creado o la conexión está rota,
	// queremos saberlo aquí, no en el primer request HTTP.
	if _, err := networkRepo.CountObservedSnapshots(context.Background()); err != nil {
		return fmt.Errorf("initNetworkModule: defensive query failed: %w", err)
	}

	logMsg("Network module v4 ready")
	return nil
}
