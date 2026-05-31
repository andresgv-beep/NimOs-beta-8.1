// storage_scheduler.go — Gestión global del DeviceReconciler en background.
//
// Mantiene un singleton del reconciler arrancado por StartStorageScheduler
// al boot.
//
// El reconciler se ejecuta con la configuración por defecto: scan cada
// 30s, marca missing tras 90s sin ver el device.

package main

import (
	"context"
	"sync"
)

var (
	globalReconciler   *DeviceReconciler
	globalReconcilerMu sync.Mutex
)

// StartStorageScheduler arranca el DeviceReconciler global en background.
// Idempotente: una segunda llamada es no-op.
//
// El reconciler usa NewRealClock() (no inyectable aquí — para tests se usa
// directamente NewDeviceReconciler con FakeClock).
//
// Llamar después de runStorageStartupTasks (que hace el scan inicial).
// El background loop se encarga de los scans periódicos posteriores.
func StartStorageScheduler(ctx context.Context) {
	globalReconcilerMu.Lock()
	defer globalReconcilerMu.Unlock()

	if globalReconciler != nil {
		// Ya arrancado
		return
	}
	if storageService == nil {
		logMsg("StartStorageScheduler: service not initialized, refusing to start")
		return
	}

	globalReconciler = NewDeviceReconciler(storageService, NewRealClock(),
		DefaultReconcilerConfig())
	globalReconciler.Start(ctx)
	logMsg("Storage scheduler started (interval=%v, missing_threshold=%v)",
		DefaultReconcilerConfig().Interval,
		DefaultReconcilerConfig().MissingThreshold)
}
