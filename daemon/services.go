package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// NimOS Service Registry — Centralized service lifecycle management
//
// Tracks which services depend on which pools, enabling safe pool destruction
// and centralized start/stop control.
//
// See: documents/NIMOS-SERVICE-REGISTRY-v1.2.md
// ═══════════════════════════════════════════════════════════════════════════════

// ─── Pool lock for destroy operations ────────────────────────────────────────

var poolLocked = map[string]bool{} // protected by storageMu

// ─── Table creation ──────────────────────────────────────────────────────────

func createServiceRegistryTables() error {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS service_instances (
		id          TEXT PRIMARY KEY,
		app_id      TEXT NOT NULL,
		pool_name   TEXT NOT NULL,
		path        TEXT NOT NULL,
		status      TEXT DEFAULT 'stopped',
		health      TEXT DEFAULT 'unknown',
		owner       TEXT DEFAULT 'system',
		config      TEXT DEFAULT '{}',
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL,
		FOREIGN KEY (app_id) REFERENCES app_registry(id)
	);

	CREATE TABLE IF NOT EXISTS service_dependencies (
		instance_id TEXT NOT NULL,
		dep_type    TEXT NOT NULL,
		target      TEXT NOT NULL,
		required    TEXT DEFAULT 'required',
		PRIMARY KEY (instance_id, dep_type, target),
		FOREIGN KEY (instance_id) REFERENCES service_instances(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_si_pool ON service_instances(pool_name);
	CREATE INDEX IF NOT EXISTS idx_si_status ON service_instances(status);
	CREATE INDEX IF NOT EXISTS idx_sd_target ON service_dependencies(target);
	`)
	return err
}

// ─── Validation helpers ──────────────────────────────────────────────────────

func validateInstanceID(id string) error {
	if !strings.Contains(id, "@") {
		return fmt.Errorf("invalid instance ID format: must be app_id@pool_name")
	}
	parts := strings.SplitN(id, "@", 2)
	if parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid instance ID: both app_id and pool_name required")
	}
	return nil
}

func validateServicePath(path, poolName string) error {
	prefix := nimosPoolsDir + "/" + poolName + "/"
	if !strings.HasPrefix(path, prefix) {
		return fmt.Errorf("path must be within pool mount point (%s)", prefix)
	}
	return nil
}

func validateDepType(depType string) error {
	switch depType {
	case "pool", "share", "path":
		return nil
	}
	return fmt.Errorf("invalid dep_type: %s (must be pool, share, or path)", depType)
}

func validateRequired(req string) error {
	switch req {
	case "required", "soft", "optional":
		return nil
	}
	return fmt.Errorf("invalid required level: %s (must be required, soft, or optional)", req)
}

// ─── DB operations ───────────────────────────────────────────────────────────

func dbServiceRegister(instance ServiceInstance, deps []ServiceDependency) error {
	if err := validateInstanceID(instance.ID); err != nil {
		return err
	}
	if err := validateServicePath(instance.Path, instance.PoolName); err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := db.Begin()
	if err != nil {
		return err
	}

	_, err = tx.Exec(`INSERT OR REPLACE INTO service_instances
		(id, app_id, pool_name, path, status, health, owner, config, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		instance.ID, instance.AppID, instance.PoolName, instance.Path,
		instance.Status, instance.Health, instance.Owner, instance.Config,
		now, now)
	if err != nil {
		tx.Rollback()
		return err
	}

	for _, dep := range deps {
		if err := validateDepType(dep.DepType); err != nil {
			tx.Rollback()
			return err
		}
		if err := validateRequired(dep.Required); err != nil {
			tx.Rollback()
			return err
		}
		_, err = tx.Exec(`INSERT OR REPLACE INTO service_dependencies
			(instance_id, dep_type, target, required) VALUES (?, ?, ?, ?)`,
			instance.ID, dep.DepType, dep.Target, dep.Required)
		if err != nil {
			tx.Rollback()
			return err
		}
	}

	tx.Commit()
	logMsg("service: registered %s (app=%s, pool=%s, status=%s)",
		instance.ID, instance.AppID, instance.PoolName, instance.Status)

	// Audit notification
	addNotification("info", "system", "Service registered",
		fmt.Sprintf("%s registered on pool %s", instance.ID, instance.PoolName))

	return nil
}

func dbServiceUpdateStatus(instanceID, status, health string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`UPDATE service_instances SET status = ?, health = ?, updated_at = ? WHERE id = ?`,
		status, health, now, instanceID)
	return err
}

func dbServiceDelete(instanceID string) error {
	_, err := db.Exec(`DELETE FROM service_instances WHERE id = ?`, instanceID)
	// dependencies cascade automatically
	return err
}

func dbServiceDeleteByPool(poolName string) error {
	_, err := db.Exec(`DELETE FROM service_instances WHERE pool_name = ?`, poolName)
	return err
}

func dbServiceGet(instanceID string) (*ServiceInstance, error) {
	var s ServiceInstance
	err := db.QueryRow(`SELECT id, app_id, pool_name, path, status, health, owner, config, created_at, updated_at
		FROM service_instances WHERE id = ?`, instanceID).
		Scan(&s.ID, &s.AppID, &s.PoolName, &s.Path, &s.Status, &s.Health, &s.Owner, &s.Config, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("service instance not found: %s", instanceID)
	}
	return &s, nil
}

func dbServiceList(poolFilter string) ([]ServiceInstance, error) {
	query := `SELECT si.id, si.app_id, si.pool_name, si.path, si.status, si.health, si.owner, si.config, si.created_at, si.updated_at
		FROM service_instances si`
	var args []interface{}
	if poolFilter != "" {
		query += ` WHERE si.pool_name = ?`
		args = append(args, poolFilter)
	}
	query += ` ORDER BY si.pool_name, si.app_id`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ServiceInstance
	for rows.Next() {
		var s ServiceInstance
		rows.Scan(&s.ID, &s.AppID, &s.PoolName, &s.Path, &s.Status, &s.Health, &s.Owner, &s.Config, &s.CreatedAt, &s.UpdatedAt)
		result = append(result, s)
	}
	if result == nil {
		result = []ServiceInstance{}
	}
	return result, nil
}

func dbServiceDependencies(instanceID string) ([]ServiceDependency, error) {
	rows, err := db.Query(`SELECT instance_id, dep_type, target, required
		FROM service_dependencies WHERE instance_id = ?`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ServiceDependency
	for rows.Next() {
		var d ServiceDependency
		rows.Scan(&d.InstanceID, &d.DepType, &d.Target, &d.Required)
		result = append(result, d)
	}
	if result == nil {
		result = []ServiceDependency{}
	}
	return result, nil
}

// ─── Pool dependency check (for destroy) ─────────────────────────────────────

// checkPoolDependencies returns active services that depend on a pool.
// Used by destroy pool to determine if destruction is safe.
func checkPoolDependencies(poolName string) ([]PoolDependencyInfo, error) {
	rows, err := db.Query(`
		SELECT si.id, si.app_id, ar.name, si.status, si.health, sd.required
		FROM service_instances si
		JOIN service_dependencies sd ON sd.instance_id = si.id
		JOIN app_registry ar ON ar.id = si.app_id
		WHERE sd.dep_type = 'pool' AND sd.target = ?
		AND si.status IN ('running', 'starting', 'stopping')
		ORDER BY sd.required DESC, ar.name`, poolName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []PoolDependencyInfo
	for rows.Next() {
		var d PoolDependencyInfo
		rows.Scan(&d.InstanceID, &d.AppID, &d.AppName, &d.Status, &d.Health, &d.Required)
		result = append(result, d)
	}
	if result == nil {
		result = []PoolDependencyInfo{}
	}
	return result, nil
}

// canDestroyPool checks if a pool can be safely destroyed.
// Returns the dependency list, whether destroy is possible, and whether force is available.
func canDestroyPool(poolName string) (deps []PoolDependencyInfo, canDestroy bool, canForce bool, err error) {
	deps, err = checkPoolDependencies(poolName)
	if err != nil {
		return nil, false, false, err
	}

	if len(deps) == 0 {
		return deps, true, false, nil
	}

	hasRequired := false
	hasSoft := false
	for _, d := range deps {
		if d.Required == "required" {
			hasRequired = true
		}
		if d.Required == "soft" {
			hasSoft = true
		}
	}

	if hasRequired {
		return deps, false, false, nil
	}
	if hasSoft {
		return deps, false, true, nil
	}
	// Only optional deps — can destroy
	return deps, true, false, nil
}

// ─── Service lifecycle (start/stop) ──────────────────────────────────────────

func serviceStop(instanceID string) error {
	instance, err := dbServiceGet(instanceID)
	if err != nil {
		return err
	}

	// Check managed_by from app_registry
	var managedBy string
	db.QueryRow(`SELECT managed_by FROM app_registry WHERE id = ?`, instance.AppID).Scan(&managedBy)

	dbServiceUpdateStatus(instanceID, "stopping", instance.Health)

	opts := CmdOptions{Timeout: 30 * time.Second}
	var stopErr error

	switch managedBy {
	case "systemd":
		// Get systemd unit name from config or convention
		unitName := getSystemdUnit(instance)
		_, stopErr = runCmd("systemctl", []string{"stop", unitName}, opts)
	case "docker":
		// Stop all containers (Docker engine stays)
		_, stopErr = runCmd("systemctl", []string{"stop", "docker.socket", "docker"}, opts)
	case "internal":
		// Handled by daemon internally — no external process to stop
		stopErr = nil
	default:
		stopErr = nil
	}

	if stopErr != nil {
		dbServiceUpdateStatus(instanceID, "failed", "degraded")
		return fmt.Errorf("failed to stop %s: %v", instanceID, stopErr)
	}

	dbServiceUpdateStatus(instanceID, "stopped", "unknown")
	addNotification("info", "system", "Service stopped", fmt.Sprintf("%s stopped", instanceID))
	return nil
}

func serviceStart(instanceID string) error {
	instance, err := dbServiceGet(instanceID)
	if err != nil {
		return err
	}

	// Check pool lock (destroy in progress)
	storageMu.Lock()
	if poolLocked[instance.PoolName] {
		storageMu.Unlock()
		return fmt.Errorf("pool %s is being destroyed — cannot start services", instance.PoolName)
	}
	storageMu.Unlock()

	// Verify pool exists and is mounted
	if !isPathOnMountedPool(nimosPoolsDir + "/" + instance.PoolName) {
		dbServiceUpdateStatus(instanceID, "error", "unreachable")
		return fmt.Errorf("pool %s is not mounted", instance.PoolName)
	}

	var managedBy string
	db.QueryRow(`SELECT managed_by FROM app_registry WHERE id = ?`, instance.AppID).Scan(&managedBy)

	dbServiceUpdateStatus(instanceID, "starting", "unknown")

	opts := CmdOptions{Timeout: 30 * time.Second}
	var startErr error

	switch managedBy {
	case "systemd":
		unitName := getSystemdUnit(instance)
		_, startErr = runCmd("systemctl", []string{"start", unitName}, opts)
	case "docker":
		_, startErr = runCmd("systemctl", []string{"start", "docker"}, opts)
	case "internal":
		startErr = nil
	default:
		startErr = nil
	}

	if startErr != nil {
		dbServiceUpdateStatus(instanceID, "failed", "degraded")
		return fmt.Errorf("failed to start %s: %v", instanceID, startErr)
	}

	dbServiceUpdateStatus(instanceID, "running", "healthy")
	addNotification("info", "system", "Service started", fmt.Sprintf("%s started", instanceID))
	return nil
}

// getSystemdUnit returns the systemd unit name for a service instance.
func getSystemdUnit(instance *ServiceInstance) string {
	switch instance.AppID {
	case "nimtorrent":
		return "nimos-torrentd.service"
	case "nimbackup":
		return "nimos-daemon.service" // backup runs inside the daemon
	default:
		return "nimos-" + instance.AppID + ".service"
	}
}

// ─── Service logs ────────────────────────────────────────────────────────────

// getServiceLogs returns the last N lines of logs for a service instance.
func getServiceLogs(instanceID string, n int) ([]map[string]interface{}, error) {
	instance, err := dbServiceGet(instanceID)
	if err != nil {
		return nil, err
	}

	var managedBy string
	db.QueryRow(`SELECT managed_by FROM app_registry WHERE id = ?`, instance.AppID).Scan(&managedBy)

	var rawOutput string

	switch managedBy {
	case "systemd":
		unitName := getSystemdUnit(instance)
		out, _ := runCmd("journalctl", []string{
			"-u", unitName,
			"-n", fmt.Sprintf("%d", n),
			"--no-pager",
			"-o", "short-iso",
		}, CmdOptions{Timeout: 5 * time.Second})
		rawOutput = out.Stdout

	case "docker":
		// Get logs from docker daemon
		out, _ := runCmd("journalctl", []string{
			"-u", "docker",
			"-n", fmt.Sprintf("%d", n),
			"--no-pager",
			"-o", "short-iso",
		}, CmdOptions{Timeout: 5 * time.Second})
		rawOutput = out.Stdout

	case "internal":
		// NimBackup and internal services log to the daemon log
		out, _ := runCmd("journalctl", []string{
			"-u", "nimos-daemon",
			"-n", fmt.Sprintf("%d", n),
			"--no-pager",
			"-o", "short-iso",
			"--grep", instance.AppID,
		}, CmdOptions{Timeout: 5 * time.Second})
		rawOutput = out.Stdout
		// If grep found nothing, fall back to daemon logs without filter
		if strings.TrimSpace(rawOutput) == "" || strings.Contains(rawOutput, "No entries") {
			out, _ = runCmd("journalctl", []string{
				"-u", "nimos-daemon",
				"-n", fmt.Sprintf("%d", n),
				"--no-pager",
				"-o", "short-iso",
			}, CmdOptions{Timeout: 5 * time.Second})
			rawOutput = out.Stdout
		}

	default:
		return []map[string]interface{}{}, nil
	}

	// Parse output into structured lines
	var lines []map[string]interface{}
	for _, line := range strings.Split(rawOutput, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-- ") {
			continue
		}
		// short-iso format: "2026-04-02T10:23:45+0200 hostname unit[pid]: message"
		// Try to split timestamp from message
		ts := ""
		msg := line
		if len(line) > 25 && (line[4] == '-' || line[10] == 'T') {
			// Find first space after timestamp+hostname+unit
			// Format: "2026-04-02T10:23:45+0200 hostname unit[123]: actual message"
			idx := strings.Index(line, "]: ")
			if idx > 0 {
				ts = line[:25] // ISO timestamp portion
				msg = strings.TrimSpace(line[idx+3:])
			} else {
				// Simpler format, just split at first colon after timestamp
				spaceIdx := strings.Index(line[25:], " ")
				if spaceIdx > 0 {
					ts = line[:25]
					msg = strings.TrimSpace(line[25+spaceIdx:])
				}
			}
		}
		lines = append(lines, map[string]interface{}{
			"timestamp": ts,
			"message":   msg,
		})
	}

	if lines == nil {
		lines = []map[string]interface{}{}
	}
	return lines, nil
}

// ─── Boot reconciliation ─────────────────────────────────────────────────────

// autoRegisterServices detects running services and registers them if not already present.
// Called once at boot, before reconcileServices.
func autoRegisterServices() {
	// ── NimTorrent ──
	// Detect from torrent.conf which pool it uses
	if _, err := dbServiceGet("nimtorrent@*"); err != nil {
		// No wildcard support — check if ANY nimtorrent instance exists
		existing, _ := dbServiceList("")
		hasTorrent := false
		for _, inst := range existing {
			if inst.AppID == "nimtorrent" {
				hasTorrent = true
				break
			}
		}
		if !hasTorrent {
			// Try to detect pool from torrent.conf
			if data, err := os.ReadFile(torrentConfPath); err == nil {
				for _, line := range strings.Split(string(data), "\n") {
					if strings.HasPrefix(line, "download_dir=") {
						dir := strings.TrimPrefix(line, "download_dir=")
						dir = strings.TrimSpace(dir)
						if strings.HasPrefix(dir, nimosPoolsDir+"/") {
							// Extract pool name: /nimos/pools/{pool}/shares → pool
							parts := strings.Split(strings.TrimPrefix(dir, nimosPoolsDir+"/"), "/")
							if len(parts) > 0 && parts[0] != "" {
								poolName := parts[0]
								instanceID := "nimtorrent@" + poolName
								path := filepath.Join(nimosPoolsDir, poolName, "shares")
								dbServiceRegister(ServiceInstance{
									ID:       instanceID,
									AppID:    "nimtorrent",
									PoolName: poolName,
									Path:     path,
									Status:   "unknown",
									Health:   "unknown",
									Owner:    "system",
									Config:   "{}",
								}, []ServiceDependency{
									{InstanceID: instanceID, DepType: "pool", Target: poolName, Required: "required"},
								})
								logMsg("service auto-register: NimTorrent on pool %s", poolName)
							}
						}
					}
				}
			}
		}
	}

	// ── Docker ──
	existing, _ := dbServiceList("")
	hasDocker := false
	for _, inst := range existing {
		if inst.AppID == "containers" {
			hasDocker = true
			break
		}
	}
	if !hasDocker {
		// Check if Docker is installed and configured on a pool
		conf := getDockerConfigGo()
		installed, _ := conf["installed"].(bool)
		dockerPath, _ := conf["path"].(string)
		if installed && dockerPath != "" && strings.HasPrefix(dockerPath, nimosPoolsDir+"/") {
			parts := strings.Split(strings.TrimPrefix(dockerPath, nimosPoolsDir+"/"), "/")
			if len(parts) > 0 && parts[0] != "" {
				poolName := parts[0]
				instanceID := "docker@" + poolName
				dbServiceRegister(ServiceInstance{
					ID:       instanceID,
					AppID:    "containers",
					PoolName: poolName,
					Path:     dockerPath,
					Status:   "unknown",
					Health:   "unknown",
					Owner:    "system",
					Config:   "{}",
				}, []ServiceDependency{
					{InstanceID: instanceID, DepType: "pool", Target: poolName, Required: "required"},
					{InstanceID: instanceID, DepType: "path", Target: dockerPath, Required: "required"},
				})
				logMsg("service auto-register: Docker on pool %s", poolName)
			}
		}
	}
}

func reconcileServices() {
	// Auto-register first — detect services that exist but aren't registered
	autoRegisterServices()

	// ── Clean orphan services whose pool no longer exists (Beta 8.1: service v2) ──
	poolNames := map[string]bool{}
	if storageService != nil {
		if pools, err := storageService.ListPools(context.Background()); err == nil {
			for _, p := range pools {
				if p.Name != "" {
					poolNames[p.Name] = true
				}
			}
		}
	}

	allInstances, _ := dbServiceList("")
	for _, inst := range allInstances {
		if inst.PoolName != "" && !poolNames[inst.PoolName] {
			db.Exec(`DELETE FROM service_dependencies WHERE instance_id = ?`, inst.ID)
			dbServiceDelete(inst.ID)
			logMsg("service reconcile: cleaned orphan %s (pool %s no longer exists)", inst.ID, inst.PoolName)
		}
	}

	// ── Reconcile remaining services ──
	instances, err := dbServiceList("")
	if err != nil {
		logMsg("service reconcile: error loading instances: %v", err)
		return
	}

	for _, inst := range instances {
		poolPath := nimosPoolsDir + "/" + inst.PoolName

		// Check pool exists and is mounted
		if !isPathOnMountedPool(poolPath) {
			if inst.Status != "error" {
				dbServiceUpdateStatus(inst.ID, "error", "unreachable")
				logMsg("service reconcile: %s → error (pool %s not mounted)", inst.ID, inst.PoolName)
			}
			continue
		}

		// Check path exists
		if _, err := runCmd("test", []string{"-d", inst.Path}, CmdOptions{Timeout: 2 * time.Second}); err != nil {
			if inst.Status != "error" {
				dbServiceUpdateStatus(inst.ID, "error", "unreachable")
				logMsg("service reconcile: %s → error (path %s missing)", inst.ID, inst.Path)
			}
			continue
		}

		// Sync real status
		var managedBy string
		db.QueryRow(`SELECT managed_by FROM app_registry WHERE id = ?`, inst.AppID).Scan(&managedBy)

		reallyRunning := false
		switch managedBy {
		case "systemd":
			unitName := getSystemdUnit(&inst)
			out, _ := runCmd("systemctl", []string{"is-active", unitName}, CmdOptions{Timeout: 5 * time.Second})
			reallyRunning = strings.TrimSpace(out.Stdout) == "active"
		case "docker":
			out, _ := runCmd("systemctl", []string{"is-active", "docker"}, CmdOptions{Timeout: 5 * time.Second})
			reallyRunning = strings.TrimSpace(out.Stdout) == "active"
		case "internal":
			reallyRunning = true // daemon is running, so internal services are too
		}

		newStatus := "stopped"
		newHealth := "unknown"
		if reallyRunning {
			newStatus = "running"
			newHealth = "healthy"
		}

		if inst.Status != newStatus || inst.Health != newHealth {
			dbServiceUpdateStatus(inst.ID, newStatus, newHealth)
			logMsg("service reconcile: %s → %s/%s", inst.ID, newStatus, newHealth)
		}
	}
}

// ─── HTTP Handlers ───────────────────────────────────────────────────────────

func handleServiceRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}

	path := r.URL.Path
	method := r.Method

	// GET /api/services — list all services
	if path == "/api/services" && method == "GET" {
		poolFilter := r.URL.Query().Get("pool")
		instances, err := dbServiceList(poolFilter)
		if err != nil {
			jsonError(w, 500, err.Error())
			return
		}

		// Enrich with app name + Docker children
		result := make([]map[string]interface{}, len(instances))
		for i, inst := range instances {
			var appName string
			db.QueryRow(`SELECT name FROM app_registry WHERE id = ?`, inst.AppID).Scan(&appName)
			deps, _ := dbServiceDependencies(inst.ID)
			result[i] = inst.ToMap()
			result[i]["appName"] = appName
			depsMap := make([]map[string]interface{}, len(deps))
			for j, d := range deps {
				depsMap[j] = d.ToMap()
			}
			result[i]["dependencies"] = depsMap

			// If this is the Docker service, inject app children + aggregate health
			if inst.AppID == "containers" && isDockerInstalledGo() {
				children, orphanCount := getDockerAppStatuses(inst.ID)
				childrenMaps := make([]map[string]interface{}, len(children))
				for j, c := range children {
					childrenMaps[j] = c.ToMap()
				}
				result[i]["children"] = childrenMaps
				result[i]["orphanCount"] = orphanCount

				// Override health with aggregate from children
				aggHealth := ComputeDockerAggregateHealth(children)
				result[i]["health"] = aggHealth
			}
		}
		jsonOk(w, map[string]interface{}{"services": result})
		return
	}

	// GET /api/services/dependencies?pool=X — check pool dependencies
	if path == "/api/services/dependencies" && method == "GET" {
		poolName := r.URL.Query().Get("pool")
		if poolName == "" {
			jsonError(w, 400, "pool parameter required")
			return
		}
		deps, canDestroy, canForce, err := canDestroyPool(poolName)
		if err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		depsMap := make([]map[string]interface{}, len(deps))
		for i, d := range deps {
			depsMap[i] = d.ToMap()
		}
		jsonOk(w, map[string]interface{}{
			"pool":         poolName,
			"dependencies": depsMap,
			"canDestroy":   canDestroy,
			"canForce":     canForce,
		})
		return
	}

	// POST /api/services/{id}/stop
	// POST /api/services/{id}/start
	// POST /api/services/{id}/restart
	// GET  /api/services/{id}/logs
	//
	// Works for both registry services (docker@pool, nimtorrent@pool) and
	// Docker app containers (jellyfin, immich, etc.) by falling through to
	// docker commands if the ID is not in the service registry.
	if strings.HasPrefix(path, "/api/services/") {
		trimmed := strings.TrimPrefix(path, "/api/services/")
		parts := strings.SplitN(trimmed, "/", 2)
		if len(parts) != 2 {
			jsonError(w, 404, "Not found")
			return
		}
		instanceID := parts[0]
		// URL-decode the instance ID (@ gets encoded as %40)
		if decoded, err := url.PathUnescape(instanceID); err == nil {
			instanceID = decoded
		}
		action := parts[1]

		// Check if this is a registered service or a docker-app container
		registeredSvc, _ := dbServiceGet(instanceID)
		isDockerApp := registeredSvc == nil && isDockerInstalledGo()

		// Validate: must be either a registered service or a known docker app
		if registeredSvc == nil && !isDockerApp {
			jsonError(w, 404, "Service not found")
			return
		}

		// For docker-app: find the actual container name
		containerName := instanceID
		if isDockerApp {
			// Look up in docker_apps DB to verify it exists.
			// containerName defaults to instanceID (the app's ID); if needed,
			// docker.go's container matching logic resolves the actual name.
			if appsRepo != nil {
				app, _ := appsRepo.GetDockerApp(r.Context(), instanceID)
				if app != nil {
					containerName = app.ID
				}
			}
		}

		// GET /api/services/{id}/logs
		if method == "GET" && action == "logs" {
			n := 50
			if nStr := r.URL.Query().Get("n"); nStr != "" {
				if parsed := parseIntDefault(nStr, 50); parsed > 0 && parsed <= 200 {
					n = parsed
				}
			}

			if isDockerApp {
				// Docker container logs
				out, _ := runSafe("docker", "logs", "--tail", fmt.Sprintf("%d", n), "--timestamps", containerName)
				var lines []map[string]interface{}
				if out != "" {
					for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
						ts := ""
						msg := line
						// Try to split timestamp from message (docker --timestamps format)
						if len(line) > 30 && line[4] == '-' {
							if idx := strings.Index(line, " "); idx > 0 && idx < 35 {
								ts = line[:idx]
								msg = line[idx+1:]
							}
						}
						lines = append(lines, map[string]interface{}{"timestamp": ts, "message": msg})
					}
				}
				if lines == nil {
					lines = []map[string]interface{}{}
				}
				jsonOk(w, map[string]interface{}{"logs": lines})
			} else {
				lines, err := getServiceLogs(instanceID, n)
				if err != nil {
					jsonError(w, 500, err.Error())
					return
				}
				jsonOk(w, map[string]interface{}{"logs": lines})
			}
			return
		}

		// POST actions require admin
		if method == "POST" {
			if session.Role != "admin" {
				jsonError(w, 403, "Admin required")
				return
			}

			if isDockerApp {
				// Docker container actions
				var cmdErr error
				switch action {
				case "stop":
					_, ok := runSafe("docker", "stop", containerName)
					if !ok {
						cmdErr = fmt.Errorf("failed to stop container %s", containerName)
					}
				case "start":
					_, ok := runSafe("docker", "start", containerName)
					if !ok {
						cmdErr = fmt.Errorf("failed to start container %s", containerName)
					}
				case "restart":
					_, ok := runSafe("docker", "restart", containerName)
					if !ok {
						cmdErr = fmt.Errorf("failed to restart container %s", containerName)
					}
				default:
					jsonError(w, 404, "Unknown action")
					return
				}
				if cmdErr != nil {
					jsonError(w, 500, cmdErr.Error())
					return
				}
				jsonOk(w, map[string]interface{}{"ok": true, "action": action, "container": containerName})
			} else {
				// Registry service actions (systemd)
				switch action {
				case "stop":
					if err := serviceStop(instanceID); err != nil {
						jsonError(w, 500, err.Error())
						return
					}
					jsonOk(w, map[string]interface{}{"ok": true, "status": "stopped"})
				case "start":
					if err := serviceStart(instanceID); err != nil {
						jsonError(w, 500, err.Error())
						return
					}
					jsonOk(w, map[string]interface{}{"ok": true, "status": "running"})
				case "restart":
					serviceStop(instanceID)
					time.Sleep(1 * time.Second)
					if err := serviceStart(instanceID); err != nil {
						jsonError(w, 500, err.Error())
						return
					}
					jsonOk(w, map[string]interface{}{"ok": true, "status": "running"})
				default:
					jsonError(w, 404, "Unknown action")
				}
			}
			return
		}
	}

	jsonError(w, 404, "Not found")
}
