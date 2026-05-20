package main

// ═══════════════════════════════════════════════════════════════════════════════
// NimOS Storage — BTRFS Pool Create & Destroy
// ═══════════════════════════════════════════════════════════════════════════════

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"
)

// ─── Destroy Pool BTRFS ──────────────────────────────────────────────────────

func destroyPoolBtrfs(poolName string) map[string]interface{} {
	storageMu.Lock()
	defer storageMu.Unlock()

	// Check service dependencies before destroying
	poolLocked[poolName] = true
	defer delete(poolLocked, poolName)

	deps, canDestroy, _, err := canDestroyPool(poolName)
	if err == nil && !canDestroy {
		names := []string{}
		for _, d := range deps {
			names = append(names, d.AppName)
		}
		return map[string]interface{}{"error": fmt.Sprintf("Active services depend on this pool: %s. Stop them first.", strings.Join(names, ", "))}
	}

	// ── Buscar pool vía service v2 (Beta 8.1) ──
	if storageService == nil {
		return map[string]interface{}{"error": "storage service not initialized"}
	}
	pools, err := storageService.ListPools(context.Background())
	if err != nil {
		return map[string]interface{}{"error": fmt.Sprintf("listing pools: %s", err)}
	}
	var targetPool *Pool
	for _, p := range pools {
		if p.Name == poolName {
			targetPool = p
			break
		}
	}
	if targetPool == nil {
		return map[string]interface{}{"error": fmt.Sprintf(`Pool "%s" not found`, poolName)}
	}

	mountPoint := targetPool.MountPoint
	opts := CmdOptions{Timeout: 30 * time.Second}

	logMsg("Destroying BTRFS pool '%s' (mount: %s)", poolName, mountPoint)

	// 1. Delete shares from DB
	deleteSharesForPool(poolName, mountPoint)

	// 2. Unmount — verify it actually unmounted
	if mountPoint != "" {
		runCmd("umount", []string{"-f", mountPoint}, opts)
		time.Sleep(1 * time.Second)

		// Verify unmount
		verifyRes, _ := runCmd("findmnt", []string{"-n", "-o", "TARGET", mountPoint}, opts)
		if strings.TrimSpace(verifyRes.Stdout) != "" {
			// Still mounted — try lazy unmount as last resort
			logMsg("WARNING: %s still mounted after umount -f, trying lazy umount", mountPoint)
			runCmd("umount", []string{"-f", "-l", mountPoint}, opts)
			time.Sleep(2 * time.Second)
		}
	}

	// 3. Clean up mount point
	if mountPoint != "" && strings.HasPrefix(mountPoint, nimosPoolsDir) {
		os.RemoveAll(mountPoint)
	}

	// 4. Remove fstab entry
	removeFstabEntry(mountPoint)

	// 5. Release BTRFS multi-device lock and wipe disks
	runCmd("btrfs", []string{"device", "scan", "--forget"}, opts)
	for _, dev := range targetPool.Devices {
		if dev.CurrentPath != "" {
			runCmd("wipefs", []string{"-af", dev.CurrentPath}, opts)
		}
	}

	// 6. Remove pool from SQLite directamente (Beta 8.1 Bloque C: sin adapter)
	// Si era primary, transferir flag al primer pool restante o limpiar metadata.
	// La lógica transaccional vive en removePoolFromDB (testeada en isolation).
	ctx := context.Background()
	err = removePoolFromDB(ctx, storageService, targetPool.ID)
	if err != nil {
		logMsg("destroyPoolBtrfs: SQLite update failed: %v", err)
		return map[string]interface{}{"error": fmt.Sprintf("DB update failed: %s", err)}
	}

	// 7. Rescan
	runCmd("partprobe", nil, opts)
	rescanSCSIBuses()

	// 8. Clean orphans
	cleanOrphanPoolDirs()

	logMsg("BTRFS pool '%s' destroyed", poolName)
	updateTorrentConfig()

	// Bloque C2: notificar al observer — el pool ya no está, los discos
	// vuelven a ser loose devices.
	notifyStorageChanged()

	// Clean up service registry for this pool
	dbServiceDeleteByPool(poolName)

	return map[string]interface{}{"ok": true}
}

// exportPoolBtrfs unmounts a BTRFS pool without wiping disks.
func exportPoolBtrfs(poolName string) map[string]interface{} {
	storageMu.Lock()
	defer storageMu.Unlock()

	deps, canDestroy, _, err := canDestroyPool(poolName)
	if err == nil && !canDestroy {
		names := []string{}
		for _, d := range deps {
			names = append(names, d.AppName)
		}
		return map[string]interface{}{"error": "services_active", "services": names}
	}

	// ── Buscar pool vía service v2 (Beta 8.1) ──
	if storageService == nil {
		return map[string]interface{}{"error": "storage service not initialized"}
	}
	pools, err := storageService.ListPools(context.Background())
	if err != nil {
		return map[string]interface{}{"error": fmt.Sprintf("listing pools: %s", err)}
	}
	var targetPool *Pool
	for _, p := range pools {
		if p.Name == poolName {
			targetPool = p
			break
		}
	}
	if targetPool == nil {
		return map[string]interface{}{"error": fmt.Sprintf(`Pool "%s" not found`, poolName)}
	}

	mountPoint := targetPool.MountPoint
	opts := CmdOptions{Timeout: 30 * time.Second}

	logMsg("Exporting BTRFS pool '%s' — data preserved", poolName)

	// 1. Delete shares from DB
	deleteSharesForPool(poolName, mountPoint)

	// 2. Unmount
	if mountPoint != "" {
		runCmd("umount", []string{"-f", mountPoint}, opts)
		time.Sleep(500 * time.Millisecond)
		verifyRes, _ := runCmd("findmnt", []string{"-n", "-o", "TARGET", mountPoint}, opts)
		if strings.TrimSpace(verifyRes.Stdout) != "" {
			runCmd("umount", []string{"-f", "-l", mountPoint}, opts)
		}
	}

	// 3. Remove fstab entry (will be re-added on import)
	removeFstabEntry(mountPoint)

	// 4. Remove pool from SQLite directamente (Beta 8.1 Bloque C: sin adapter)
	// Misma lógica transaccional que destroyPoolBtrfs — extraída a removePoolFromDB.
	ctx := context.Background()
	err = removePoolFromDB(ctx, storageService, targetPool.ID)
	if err != nil {
		logMsg("exportPoolBtrfs: SQLite update failed: %v", err)
		return map[string]interface{}{"error": fmt.Sprintf("DB update failed: %s", err)}
	}

	dbServiceDeleteByPool(poolName)
	logMsg("BTRFS pool '%s' exported — data preserved, re-import via Restaurar volumen", poolName)
	updateTorrentConfig()

	// Bloque C2: notificar al observer — el pool desaparece del managed
	// pero el filesystem sigue en disco. Pasará a ser orphan_filesystem
	// en el próximo snapshot.
	notifyStorageChanged()

	return map[string]interface{}{"ok": true}
}

// ─── Helpers internos ────────────────────────────────────────────────────────

// removePoolFromDB elimina el pool de SQLite y reasigna o limpia la metadata
// `primary_pool` de forma atómica. Es el "core transaccional" compartido por
// destroyPoolBtrfs y exportPoolBtrfs.
//
// Semántica:
//   - DELETE del pool (CASCADE limpia pool_devices y capabilities).
//   - Si el pool destruido era el primary_pool actual:
//   - Busca otro pool en estado 'managed'. Si existe → transfiere primary.
//   - Si no hay otro managed → borra primary_pool y configured_at.
//   - Toda la operación es atómica vía runInTx. Cualquier fallo intermedio
//     hace rollback completo (CRIT-2 fix: errores ya no se ignoran).
//
// Pre-condiciones:
//   - svc != nil
//   - poolID es el ID en DB del pool a borrar (no el nombre).
//
// Post-condiciones (en caso de éxito):
//   - El pool ya no existe en storage_pools.
//   - storage_metadata.primary_pool apunta a un pool real o no existe.
//
// Esta función NO toca disco, ni shell, ni observer. Sólo DB.
// Eso la hace 100% testeable con DB temporal (ver storage_btrfs_pool_test.go).
func removePoolFromDB(ctx context.Context, svc *StorageService, poolID string) error {
	return svc.runInTx(ctx, func(tx *sql.Tx) error {
		// Borrar el pool (CASCADE limpia pool_devices, capabilities)
		if err := svc.repo.DeletePool(ctx, tx, poolID); err != nil {
			return err
		}

		// Si era el primario, transferir a otro pool managed o limpiar metadata.
		// CRIT-2 fix: propagamos errores DB para que la tx haga rollback
		// si algo falla. Antes se ignoraban con `_`, lo que podía dejar
		// primary_pool apuntando a un pool inexistente tras un commit
		// "exitoso" pero parcial.
		var currentPrimaryID string
		if err := tx.QueryRowContext(ctx,
			`SELECT value FROM storage_metadata WHERE key = 'primary_pool'`).Scan(&currentPrimaryID); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("read primary_pool: %w", err)
		}

		// Si este pool no era el primary, no hay nada más que hacer.
		if currentPrimaryID != poolID {
			return nil
		}

		// Buscar otro pool managed para transferir el primary.
		var newPrimaryID string
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM storage_pools WHERE control_state = 'managed' LIMIT 1`).Scan(&newPrimaryID); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("find new primary: %w", err)
		}

		if newPrimaryID != "" {
			// Hay otro pool managed: transferir primary.
			if _, err := tx.ExecContext(ctx,
				`INSERT OR REPLACE INTO storage_metadata (key, value) VALUES ('primary_pool', ?)`, newPrimaryID); err != nil {
				return fmt.Errorf("transfer primary_pool: %w", err)
			}
			return nil
		}

		// No quedan pools managed: limpiar metadata.
		if _, err := tx.ExecContext(ctx, `DELETE FROM storage_metadata WHERE key = 'primary_pool'`); err != nil {
			return fmt.Errorf("delete primary_pool: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM storage_metadata WHERE key = 'configured_at'`); err != nil {
			return fmt.Errorf("delete configured_at: %w", err)
		}
		return nil
	})
}