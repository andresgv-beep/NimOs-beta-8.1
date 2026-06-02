// storage_layout_reconcile.go — STOR-01-A · Detección de drift de layout.
//
// Las operaciones de layout (AddDevice/RemoveDevice/ReplaceDevice/
// ConvertProfile) que se interrumpen por un crash quedan en `inconclusive`
// (ver storage_recovery.go). BTRFS es crash-safe en ellas (un balance es
// resumible, no corrompe), así que NO hay pérdida de datos — pero la BD de
// NimOS puede quedar divergente de la realidad física: la DB dice `raid1`
// pero el balance a `raid10` quedó a medias.
//
// Esta fase (01-A) SOLO DETECTA y MARCA. Al arrancar, compara el profile que
// dice la BD contra el profile real que reporta BTRFS (`btrfs fi df`). Si
// divergen, marca el pool en estado `recovery` (visible, accionable) en vez
// de dejar la operación huérfana suelta. NO toca el layout — eso es 01-C,
// una acción explícita del usuario.
//
// Filosofía: detectar y exponer con honestidad. Nunca actuar sobre el layout
// de forma automática (un balance mal resumido sí podría dar problemas).

package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// LayoutDriftResult resume lo que encontró detectLayoutDrift.
type LayoutDriftResult struct {
	Inspected    int      // pools managed examinados
	Drifted      int      // pools con divergencia BD vs realidad
	MarkedRecov  int      // pools marcados en estado recovery
	DriftedNames []string // nombres de los pools con drift (para log/UI)
}

// layoutHasDrifted decide si hay drift comparando el profile esperado (BD)
// con el real (BTRFS). Función pura, sin side-effects, para poder testearla
// exhaustivamente sin discos. Devuelve (hayDrift, motivo).
//
// Reglas:
//   - real vacío → NO drift (no se pudo leer; ante duda, no marcar falso positivo)
//   - expected vacío → NO drift (pool sin profile en BD, caso anómalo, no tocar)
//   - comparación case-insensitive
func layoutHasDrifted(expected, real string) (bool, string) {
	real = strings.ToLower(strings.TrimSpace(real))
	expected = strings.ToLower(strings.TrimSpace(expected))
	if real == "" {
		return false, "no se pudo leer profile real"
	}
	if expected == "" {
		return false, "pool sin profile en BD"
	}
	if real != expected {
		return true, fmt.Sprintf("BD=%s realidad=%s", expected, real)
	}
	return false, ""
}

// detectLayoutDrift compara, para cada pool managed, el profile registrado en
// la BD contra el profile real del filesystem. Si divergen, marca el pool en
// estado `recovery`. Llamar al arranque, tras reconcileMountState (los pools
// ya están montados, condición necesaria para leer su profile real).
func detectLayoutDrift(ctx context.Context) (*LayoutDriftResult, error) {
	if storageService == nil {
		return nil, fmt.Errorf("detectLayoutDrift: storage service not initialized")
	}
	pools, err := storageService.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("detectLayoutDrift: list pools: %w", err)
	}

	result := &LayoutDriftResult{}
	for _, p := range pools {
		// Solo pools managed: los observed no los gestiona NimOS, su layout
		// no es asunto nuestro. Los que ya están en recovery se saltan (no
		// re-marcar). Sin mountpoint montado no podemos leer el profile real.
		if p.ControlState != ControlStateManaged {
			continue
		}
		if p.MountPoint == "" || !isPathOnMountedPool(p.MountPoint) {
			continue
		}
		result.Inspected++

		realProfile := readRealDataProfile(p.MountPoint)
		drifted, reason := layoutHasDrifted(string(p.Profile), realProfile)
		if !drifted {
			if realProfile == "" {
				logMsg("LayoutDrift: no se pudo leer profile real de '%s', se omite", p.Name)
			}
			continue
		}

		// Drift confirmado: la BD dice una cosa, el disco otra.
		result.Drifted++
		result.DriftedNames = append(result.DriftedNames, p.Name)
		logMsg("LayoutDrift: pool '%s' DIVERGE — %s (posible op de layout interrumpida)",
			p.Name, reason)

		if markPoolRecovery(ctx, p.ID) {
			result.MarkedRecov++
		}
	}

	if result.Drifted > 0 {
		logMsg("LayoutDrift: %d/%d pools con drift de layout, %d marcados en recovery: %s",
			result.Drifted, result.Inspected, result.MarkedRecov, strings.Join(result.DriftedNames, ", "))
	}
	return result, nil
}

// readRealDataProfile lee el profile REAL de los datos del pool desde
// `btrfs fi df <mp>`, devolviendo p.ej. "raid1" lowercase, o "" si falla.
// Reutiliza parseProfileFromDfLine (storage_btrfs_probe.go).
func readRealDataProfile(mountPoint string) string {
	out, ok := runSafe("btrfs", "filesystem", "df", mountPoint)
	if !ok {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Data,") {
			return parseProfileFromDfLine(line)
		}
	}
	return ""
}

// markPoolRecovery pone el pool en estado recovery (visible/accionable).
// Devuelve true si se marcó correctamente.
func markPoolRecovery(ctx context.Context, poolID string) bool {
	err := storageService.runInTx(ctx, func(tx *sql.Tx) error {
		return storageService.repo.SetPoolControlState(ctx, tx, poolID, ControlStateRecovery)
	})
	if err != nil {
		logMsg("LayoutDrift: no se pudo marcar pool %s en recovery: %v", poolID, err)
		return false
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// STOR-01-C · Resolución asistida del drift (acción explícita del usuario)
// ─────────────────────────────────────────────────────────────────────────────

// resolvePoolRecoveryAccept resuelve un pool en recovery ACEPTANDO el estado
// real del disco: lee el profile real de BTRFS, lo persiste en la BD, y devuelve
// el pool a estado managed. Es la opción segura — no toca el layout, solo hace
// que la BD refleje la verdad física. Para cuando el balance interrumpido en
// realidad sí terminó, o cuando el usuario acepta el layout actual.
func resolvePoolRecoveryAccept(ctx context.Context, poolID string) error {
	pool, err := storageService.repo.GetPool(ctx, poolID)
	if err != nil {
		return fmt.Errorf("accept: get pool: %w", err)
	}
	if pool.ControlState != ControlStateRecovery {
		return fmt.Errorf("accept: el pool no está en estado recovery (está en %s)", pool.ControlState)
	}
	if pool.MountPoint == "" || !isPathOnMountedPool(pool.MountPoint) {
		return fmt.Errorf("accept: el pool no está montado, no se puede leer su profile real")
	}

	realProfile := readRealDataProfile(pool.MountPoint)
	if realProfile == "" {
		return fmt.Errorf("accept: no se pudo leer el profile real del filesystem")
	}

	// Persistir profile real + volver a managed, atómico.
	err = storageService.runInTx(ctx, func(tx *sql.Tx) error {
		if e := storageService.repo.SetPoolProfile(ctx, tx, poolID, Profile(realProfile)); e != nil {
			return e
		}
		return storageService.repo.SetPoolControlState(ctx, tx, poolID, ControlStateManaged)
	})
	if err != nil {
		return fmt.Errorf("accept: persist: %w", err)
	}
	logMsg("LayoutRecovery: pool '%s' aceptado — profile BD actualizado a %s, vuelto a managed",
		pool.Name, realProfile)
	return nil
}

// resolvePoolRecoveryResume reanuda un balance de BTRFS que quedó a medias.
// BTRFS soporta `btrfs balance resume` para continuar un balance interrumpido.
// Tras reanudar y completar, lee el profile real y actualiza la BD a managed.
// Más arriesgado que accept (ejecuta layout), por eso es acción explícita.
func resolvePoolRecoveryResume(ctx context.Context, poolID string) error {
	pool, err := storageService.repo.GetPool(ctx, poolID)
	if err != nil {
		return fmt.Errorf("resume: get pool: %w", err)
	}
	if pool.ControlState != ControlStateRecovery {
		return fmt.Errorf("resume: el pool no está en estado recovery (está en %s)", pool.ControlState)
	}
	if pool.MountPoint == "" || !isPathOnMountedPool(pool.MountPoint) {
		return fmt.Errorf("resume: el pool no está montado")
	}

	// btrfs balance resume. Si no hay balance pausado, BTRFS devuelve error
	// "balance not running" — lo tratamos como "ya no hay nada que reanudar"
	// y procedemos a aceptar el estado actual.
	out, ok := runSafe("btrfs", "balance", "resume", pool.MountPoint)
	if !ok && !strings.Contains(strings.ToLower(out), "not running") &&
		!strings.Contains(strings.ToLower(out), "nothing to resume") {
		return fmt.Errorf("resume: btrfs balance resume falló: %s", strings.TrimSpace(out))
	}

	// Tras reanudar (o si no había nada que reanudar), el layout real es la
	// verdad. Adoptamos ese profile y volvemos a managed.
	return resolvePoolRecoveryAccept(ctx, poolID)
}
