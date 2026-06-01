package main

// ═══════════════════════════════════════════════════════════════════════════════
// NimOS Storage — Common helpers shared by ZFS and BTRFS pool operations
// ═══════════════════════════════════════════════════════════════════════════════

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ─── Config Helpers ──────────────────────────────────────────────────────────

// deleteSharesForPool removes all shares associated with a pool from the DB.
func deleteSharesForPool(poolName, mountPoint string) {
	shares, _ := dbSharesListRaw()
	for _, s := range shares {
		if s.Pool == poolName || s.Volume == poolName || (mountPoint != "" && strings.HasPrefix(s.Path, mountPoint)) {
			handleOp(Request{Op: "share.delete", ShareName: s.Name})
			dbSharesDelete(s.Name)
		}
	}
}

// ─── Fstab ───────────────────────────────────────────────────────────────────

// removeFstabEntry removes a mount point entry from /etc/fstab.
//
// Beta 8 bug fix: previously used strings.Contains(line, mountPoint)
// which matched any line containing the path as a substring. That
// would remove /nimos/pools/data-backup when asked to remove
// /nimos/pools/data, or /etc/cron.d/data-stuff if a path collided.
//
// fstab format: <device> <mountpoint> <fstype> <opts> <dump> <pass>
// Now we parse fields by whitespace and compare field[1] exactly.
func removeFstabEntry(mountPoint string) {
	if mountPoint == "" {
		return
	}
	data, err := os.ReadFile("/etc/fstab")
	if err != nil {
		return
	}
	var kept []string
	removed := 0
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		// Preserve comments and blank lines verbatim
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			kept = append(kept, line)
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && fields[1] == mountPoint {
			logMsg("Removing fstab entry: %s", trimmed)
			removed++
			continue
		}
		kept = append(kept, line)
	}
	if removed == 0 {
		// Nothing matched: don't bother rewriting (avoids unnecessary IO
		// and possible permission issues).
		return
	}
	// Atomic write: write to tmp, then rename. Avoids partial files if
	// the daemon dies mid-write.
	tmpPath := "/etc/fstab.nimos.tmp"
	if err := os.WriteFile(tmpPath, []byte(strings.Join(kept, "\n")), 0644); err != nil {
		logMsg("removeFstabEntry: write tmp failed: %v", err)
		return
	}
	if err := os.Rename(tmpPath, "/etc/fstab"); err != nil {
		logMsg("removeFstabEntry: rename failed: %v", err)
		os.Remove(tmpPath)
	}
}

// ─── Orphan Cleanup ──────────────────────────────────────────────────────────

// cleanOrphanPoolDirs removes directories in /nimos/pools/ that are not
// associated with any configured pool and have nothing mounted on them.
// Safe to call AFTER pool operations (destroy, create), never at startup
// before pools have mounted.
//
// Beta 8 safety guard: if the pool config is empty or unreadable, we
// REFUSE to clean. Otherwise a corrupt/missing storage.json would cause
// us to delete every directory under /nimos/pools/ — including the
// mount points of pools whose mount currently isn't visible to us due
// to a transient error.
//
// Rule: deletion is only allowed when we have a positively-known list
// of pools to compare against. "Empty list" is treated as "I don't know
// what's there", which is the safe default.
func cleanOrphanPoolDirs() {
	// Build set of known mount points (Beta 8.1: service v2)
	knownMounts := map[string]bool{}
	if storageService != nil {
		if pools, err := storageService.ListPools(context.Background()); err == nil {
			for _, p := range pools {
				if p.MountPoint != "" {
					knownMounts[p.MountPoint] = true
				}
			}
		}
	}

	// SAFETY GUARD: if we have no known pools, do nothing. A corrupt or
	// missing config would otherwise lead to mass deletion under
	// /nimos/pools/.
	if len(knownMounts) == 0 {
		// Check if there's anything at all in /nimos/pools/. If yes,
		// it's suspicious — log a warning so the admin notices.
		if entries, err := os.ReadDir(nimosPoolsDir); err == nil && len(entries) > 0 {
			logMsg("cleanOrphanPoolDirs: REFUSING to clean — config has no pools but %d directories exist in %s. Possible corrupt config.",
				len(entries), nimosPoolsDir)
		}
		return
	}

	entries, err := os.ReadDir(nimosPoolsDir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirPath := filepath.Join(nimosPoolsDir, e.Name())

		// Regla 1: saltar pools conocidos (en la BD)
		if knownMounts[dirPath] {
			continue
		}

		// Regla 2: saltar si hay algo montado aquí (pool vivo)
		if isPathOnMountedPool(dirPath) {
			continue
		}

		// Regla 3 (grace period): no borrar carpetas recién creadas — podría
		// ser un pool en proceso de creación (carpeta existe pero aún no está
		// montada ni en la BD por unos segundos). Evita una race con CreatePool.
		if info, err := e.Info(); err == nil {
			if time.Since(info.ModTime()) < 5*time.Minute {
				logMsg("cleanOrphanPoolDirs: skip '%s' — creada hace <5min (grace period)", dirPath)
				continue
			}
		}

		// Pasó las reglas: es una carpeta huérfana confirmada (no es pool
		// conocido, no está montada, no es reciente). Decisión de diseño
		// (Andrés, 01/06): destruir un pool = dejarlo limpio. Una huérfana con
		// contenido es basura de un pool ya destruido (Docker escribe al pool,
		// no a la SD, desde el fix de data-root). Se borra con contenido y todo.
		subEntries, _ := os.ReadDir(dirPath)
		if len(subEntries) > 0 {
			logMsg("cleanOrphanPoolDirs: borrando huérfana '%s' con %d items (basura de pool destruido)",
				dirPath, len(subEntries))
		}
		if err := os.RemoveAll(dirPath); err != nil {
			logMsg("cleanOrphanPoolDirs: failed to remove %s: %v", dirPath, err)
			continue
		}
		logMsg("Cleaned orphan directory: %s", dirPath)
	}
}

// ─── Torrent Config ──────────────────────────────────────────────────────────

// updateTorrentConfig updates NimTorrent's download_dir to point to the primary
// pool's shares directory. Called after create/destroy pool.
// Without this, NimTorrent writes to the system disk.
const torrentConfPath = "/etc/nimos/torrent.conf"

func updateTorrentConfig() {
	// Beta 8.1: usa service v2 para obtener primary pool
	newDir := ""
	if storageService != nil {
		if pools, err := storageService.ListPools(context.Background()); err == nil {
			for _, p := range pools {
				if p.IsPrimary && p.MountPoint != "" {
					newDir = filepath.Join(p.MountPoint, "shares")
					break
				}
			}
		}
	}

	// Read current config
	data, err := os.ReadFile(torrentConfPath)
	if err != nil {
		// No torrent config — nothing to update
		return
	}

	// Replace download_dir line
	var lines []string
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "download_dir=") {
			if newDir != "" {
				lines = append(lines, "download_dir="+newDir)
			} else {
				lines = append(lines, "download_dir=")
			}
			found = true
		} else {
			lines = append(lines, line)
		}
	}
	if !found && newDir != "" {
		lines = append(lines, "download_dir="+newDir)
	}

	os.WriteFile(torrentConfPath, []byte(strings.Join(lines, "\n")), 0644)

	// Restart torrentd to pick up new config
	runCmd("systemctl", []string{"restart", "nimos-torrentd"}, CmdOptions{Timeout: 10 * time.Second})

	if newDir != "" {
		logMsg("Updated NimTorrent download_dir to %s", newDir)
	} else {
		logMsg("Cleared NimTorrent download_dir (no pools)")
	}
}
