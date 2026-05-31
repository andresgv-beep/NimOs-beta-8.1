package main

// ═══════════════════════════════════════════════════════════════════════════════
// NimOS Storage — BTRFS Features (Snapshots, Scrub, Scheduler)
//
// Endpoints match the existing UI contract.
//
// Beta 8 note: ZFS support removed in Fase 5. Snapshot/dataset endpoints
// that were ZFS-only are now stubs returning empty/unsupported. They can
// be re-implemented for BTRFS (subvolumes) in Beta 9+ when needed.
// ═══════════════════════════════════════════════════════════════════════════════

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ─── Core snapshot primitives (used by storage UI and backup) ────────────────

// btrfsSnapshotCreate creates a BTRFS read-only subvolume snapshot.
// source is the path to the subvolume to snapshot, snapPath is the destination.
// Returns ("", nil) on success, ("error message", error) on failure.
func btrfsSnapshotCreate(source, snapPath string) (string, error) {
	opts := CmdOptions{Timeout: 30 * time.Second}
	res, err := runCmd("btrfs", []string{"subvolume", "snapshot", "-r", source, snapPath}, opts)
	if err != nil {
		return res.Stderr, err
	}
	return "", nil
}

// btrfsSnapshotDestroy destroys a BTRFS subvolume snapshot.
func btrfsSnapshotDestroy(snapPath string) (string, error) {
	opts := CmdOptions{Timeout: 30 * time.Second}
	res, err := runCmd("btrfs", []string{"subvolume", "delete", snapPath}, opts)
	if err != nil {
		return res.Stderr, err
	}
	return "", nil
}

// ─── Snapshot endpoints (BTRFS subvolume implementation pending Beta 9) ──────

// listSnapshots returns snapshots for a pool.
// Beta 8: returns empty array. BTRFS snapshot listing via subvolumes is
// pending in Beta 9 (requires walking the subvolume tree).
func listSnapshots(poolName string) map[string]interface{} {
	return map[string]interface{}{"snapshots": []interface{}{}}
}

// createSnapshot stub. Beta 8: not supported.
func createSnapshot(body map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"ok":    false,
		"error": "snapshot management not yet implemented for BTRFS (pending Beta 9)",
	}
}

// rollbackSnapshot stub. Beta 8: not supported.
func rollbackSnapshot(body map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"ok":    false,
		"error": "snapshot rollback not yet implemented for BTRFS (pending Beta 9)",
	}
}

// ─── SCRUB ───────────────────────────────────────────────────────────────────

// resolveMountPointByName busca el mount_point de un pool por nombre vía service.
// Si no encuentra el pool, devuelve el path por defecto en /nimos/pools/<name>.
func resolveMountPointByName(poolName string) string {
	if storageService == nil {
		return nimosPoolsDir + "/" + poolName
	}
	pools, err := storageService.ListPools(context.Background())
	if err != nil {
		return nimosPoolsDir + "/" + poolName
	}
	for _, p := range pools {
		if p.Name == poolName && p.MountPoint != "" {
			return p.MountPoint
		}
	}
	return nimosPoolsDir + "/" + poolName
}

// startScrub starts a BTRFS integrity check.
// POST /api/storage/scrub { pool }
func startScrub(body map[string]interface{}) map[string]interface{} {
	pool := bodyStr(body, "pool")

	// Resolve mount point via service v2
	mountPoint := resolveMountPointByName(pool)

	if _, err := runCmd("btrfs", []string{"filesystem", "show", mountPoint}, CmdOptions{Timeout: 5 * time.Second}); err != nil {
		return map[string]interface{}{"ok": false, "error": "Pool not found or not a BTRFS filesystem"}
	}

	_, err := runCmd("btrfs", []string{"scrub", "start", mountPoint}, CmdOptions{Timeout: 15 * time.Second})
	if err != nil {
		return map[string]interface{}{"ok": false, "error": fmt.Sprintf("btrfs scrub failed: %s", err)}
	}
	logMsg("BTRFS scrub started on %s", mountPoint)
	addNotification("info", "system", "Verificación iniciada",
		fmt.Sprintf("Verificación de integridad iniciada en volumen %s", pool))
	return map[string]interface{}{"ok": true, "type": "btrfs"}
}

// getScrubStatus returns detailed scrub status for a BTRFS pool.
// GET /api/storage/scrub/status?pool=NAME
func getScrubStatus(poolName string) map[string]interface{} {
	mountPoint := resolveMountPointByName(poolName)

	if _, err := runCmd("btrfs", []string{"filesystem", "show", mountPoint}, CmdOptions{Timeout: 5 * time.Second}); err != nil {
		return map[string]interface{}{"status": "error", "error": "Pool not found", "filesystem": "unknown"}
	}
	return getBtrfsScrubStatus(mountPoint, poolName)
}

func getBtrfsScrubStatus(mountPoint, poolName string) map[string]interface{} {
	opts := CmdOptions{Timeout: 10 * time.Second}
	res, _ := runCmd("btrfs", []string{"scrub", "status", mountPoint}, opts)
	output := res.Stdout

	result := map[string]interface{}{
		"status":       "idle",
		"progress":     0,
		"errors":       0,
		"duration":     "—",
		"lastScrub":    nil,
		"lastDuration": nil,
		"lastErrors":   nil,
		"dataErrors":   "—",
		"filesystem":   "btrfs",
	}

	if strings.Contains(output, "no stats available") || strings.Contains(output, "not started") {
		result["status"] = "never"
		return result
	}

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "Status:") {
			status := strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
			switch status {
			case "running":
				result["status"] = "scrubbing"
			case "finished":
				result["status"] = "done"
			case "aborted":
				result["status"] = "canceled"
			}
		}

		if strings.HasPrefix(line, "Scrub started:") {
			timeStr := strings.TrimSpace(strings.TrimPrefix(line, "Scrub started:"))
			for _, layout := range []string{
				"Mon Jan  2 15:04:05 2006",
				"Mon Jan 2 15:04:05 2006",
				"2006-01-02 15:04:05",
			} {
				if t, err := time.Parse(layout, timeStr); err == nil {
					result["lastScrub"] = t.Format(time.RFC3339)
					break
				}
			}
		}

		if strings.HasPrefix(line, "Duration:") {
			dur := strings.TrimSpace(strings.TrimPrefix(line, "Duration:"))
			result["duration"] = dur
			result["lastDuration"] = dur
		}

		if strings.HasPrefix(line, "Rate:") {
			result["speed"] = strings.TrimSpace(strings.TrimPrefix(line, "Rate:"))
		}

		if strings.HasPrefix(line, "Error summary:") {
			errStr := strings.TrimSpace(strings.TrimPrefix(line, "Error summary:"))
			result["dataErrors"] = errStr
			if strings.Contains(errStr, "no errors") {
				result["errors"] = 0
				result["lastErrors"] = 0
			} else {
				totalErrs := 0
				for _, part := range strings.Split(errStr, " ") {
					if strings.Contains(part, "=") {
						kv := strings.SplitN(part, "=", 2)
						if len(kv) == 2 {
							n, _ := strconv.Atoi(kv[1])
							totalErrs += n
						}
					}
				}
				result["errors"] = totalErrs
				result["lastErrors"] = totalErrs
			}
		}

		if strings.HasPrefix(line, "Total to scrub:") {
			result["totalSize"] = strings.TrimSpace(strings.TrimPrefix(line, "Total to scrub:"))
		}
	}

	return result
}

// ─── SCRUB SCHEDULER ─────────────────────────────────────────────────────────
//
// Beta 8.1: tabla scrub_schedule definida en storage_schema.sql §8.

func calculateNextRun(freq string, hour, minute, dow, dom int) interface{} {
	now := time.Now()
	target := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())

	switch freq {
	case "daily":
		if !target.After(now) {
			target = target.Add(24 * time.Hour)
		}
	case "weekly":
		daysUntil := (dow - int(now.Weekday()) + 7) % 7
		if daysUntil == 0 && !target.After(now) {
			daysUntil = 7
		}
		target = target.AddDate(0, 0, daysUntil)
	case "monthly":
		target = time.Date(now.Year(), now.Month(), dom, hour, minute, 0, 0, now.Location())
		if !target.After(now) {
			target = target.AddDate(0, 1, 0)
		}
	default:
		return nil
	}
	return target.Format(time.RFC3339)
}

// startScrubScheduler starts a background goroutine that periodically
// checks the scrub_schedule table and runs scheduled scrubs.
func startScrubScheduler() {
	go func() {
		// Wait a bit on startup so we don't run immediately after boot
		time.Sleep(60 * time.Second)
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			checkAndRunScheduledScrubs()
		}
	}()
	logMsg("Scrub scheduler started (check interval: 60s)")
}

func checkAndRunScheduledScrubs() {
	rows, err := db.Query(`SELECT pool_name, frequency, day_of_week, day_of_month, hour, minute, last_run
		FROM scrub_schedule WHERE enabled = 1`)
	if err != nil {
		return
	}
	defer rows.Close()

	now := time.Now()
	for rows.Next() {
		var (
			poolName       string
			freq           string
			dow, dom, h, m int
			lastRun        *string
		)
		if err := rows.Scan(&poolName, &freq, &dow, &dom, &h, &m, &lastRun); err != nil {
			continue
		}
		lastRunStr := ""
		if lastRun != nil {
			lastRunStr = *lastRun
		}
		if shouldRunNow(freq, h, m, dow, dom, lastRunStr, now) {
			logMsg("Scrub scheduler: starting scheduled scrub on %s", poolName)
			startScrub(map[string]interface{}{"pool": poolName})
			_, _ = db.Exec(`UPDATE scrub_schedule SET last_run = ?, next_run = ? WHERE pool_name = ?`,
				now.Format(time.RFC3339),
				calculateNextRun(freq, h, m, dow, dom),
				poolName)
		}
	}
}

func shouldRunNow(freq string, hour, minute, dow, dom int, lastRun string, now time.Time) bool {
	// Avoid double-runs within the same minute window
	if lastRun != "" {
		if last, err := time.Parse(time.RFC3339, lastRun); err == nil {
			if now.Sub(last) < 50*time.Second {
				return false
			}
		}
	}
	if now.Hour() != hour || now.Minute() != minute {
		return false
	}
	switch freq {
	case "daily":
		return true
	case "weekly":
		return int(now.Weekday()) == dow
	case "monthly":
		return now.Day() == dom
	}
	return false
}
