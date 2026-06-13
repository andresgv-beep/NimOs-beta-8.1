package main

import (
	"context"
	"testing"
)

// La tarea de limpieza de huérfanos hereda las guardas de
// cleanOrphanPoolDirsResult. Lo crítico a verificar: metadatos correctos y que
// la GUARDA DE SEGURIDAD se respeta (no borra nada si no hay pools conocidos).

func TestOrphanDirSweep_Metadata(t *testing.T) {
	task := &orphanDirSweepTask{}
	if task.ID() != "orphan_dir_sweep" {
		t.Errorf("ID inesperado: %s", task.ID())
	}
	if task.Name() == "" || task.Description() == "" {
		t.Error("Name/Description no deben estar vacíos (se muestran en la UI)")
	}
	if task.DefaultSchedule().Kind != ScheduleAtBoot {
		t.Errorf("schedule por defecto: got %v, want at_boot", task.DefaultSchedule().Kind)
	}
}

// SEGURIDAD: sin storageService (ningún pool conocido), la tarea DEBE saltarse
// — nunca borrar a ciegas. Es la guarda anti-config-corrupta.
func TestOrphanDirSweep_SkipsWhenNoPoolsKnown(t *testing.T) {
	// Guardar y anular storageService para simular "no hay pools conocidos".
	orig := storageService
	storageService = nil
	defer func() { storageService = orig }()

	task := &orphanDirSweepTask{}
	res := task.Run(context.Background())

	// Debe saltarse (Skipped), no borrar nada, no fallar con error.
	if !res.Skipped {
		t.Errorf("sin pools conocidos debe SKIP por seguridad; got Skipped=%v", res.Skipped)
	}
	if res.ItemsRemoved != 0 {
		t.Errorf("no debe borrar nada en modo skip; got ItemsRemoved=%d", res.ItemsRemoved)
	}
	if res.Err != nil {
		t.Errorf("skip no es error; got Err=%v", res.Err)
	}
}
