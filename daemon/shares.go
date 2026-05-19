package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════
// SHARES HTTP HANDLERS · NimOS Beta 8.1 · BTRFS-only
// ═══════════════════════════════════════════════════════════════════════
//
// Esta capa gestiona las carpetas compartidas (SMB/NFS/FTP).
// El estado vive en SQLite (tablas `shares` + `share_permissions`).
// El sistema de archivos usa subvolúmenes BTRFS bajo pool/shares/.
//
// Decisión arquitectónica Beta 8.1:
//   · ZFS deprecado por completo. NO existe path ZFS aquí.
//   · `findTargetPool` (legacy JSON) eliminado.
//   · Pools se consultan vía `storageService.ListPools()` (SQLite V2).
//   · Cada share = un subvolumen BTRFS con qgroup quota.
//
// Endpoint contract:
//   GET    /api/shares             — lista shares (filtrado por permisos)
//   POST   /api/shares             — crea (body: {name, description, pool, quotaBytes})
//   PUT    /api/shares/:name       — actualiza (description, recycleBin, quota, permissions, appPermissions)
//   DELETE /api/shares/:name       — elimina + destruye subvolumen
//
// ═══════════════════════════════════════════════════════════════════════

func handleSharesRoutes(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	method := r.Method

	// GET /api/shares — list all shared folders
	if path == "/api/shares" && method == "GET" {
		sharesListHTTP(w, r)
		return
	}

	// POST /api/shares — create shared folder
	if path == "/api/shares" && method == "POST" {
		sharesCreateHTTP(w, r)
		return
	}

	// Match /api/shares/:name
	shareMatch := regexp.MustCompile(`^/api/shares/([a-zA-Z0-9_-]+)$`)
	matches := shareMatch.FindStringSubmatch(path)
	if matches == nil {
		jsonError(w, 404, "Not found")
		return
	}
	target := matches[1]

	switch method {
	case "PUT":
		sharesUpdateHTTP(w, r, target)
	case "DELETE":
		sharesDeleteHTTP(w, r, target)
	default:
		jsonError(w, 405, "Method not allowed")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// POOL LOOKUP HELPERS · stack V2 BTRFS
// ═══════════════════════════════════════════════════════════════════════

// resolveSharePool busca un pool por nombre en el stack V2.
// Si poolName está vacío, devuelve el primer pool managed disponible (el "primary").
// Retorna error si no hay pools o el pool indicado no existe.
//
// Nota: el frontend manda el `name` del pool (legible), no el `id` UUID.
// Esto es coherente con la UX (el usuario ve "data", no "550e8400-...").
func resolveSharePool(ctx context.Context, poolName string) (*Pool, error) {
	if storageService == nil {
		return nil, fmt.Errorf("storage service not initialized")
	}
	pools, err := storageService.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing pools: %w", err)
	}
	if len(pools) == 0 {
		return nil, fmt.Errorf("no storage pools available. Create a pool in Storage Manager first")
	}
	// Si se especificó pool, buscarlo por nombre
	if poolName != "" {
		for _, p := range pools {
			if p.Name == poolName {
				return p, nil
			}
		}
		return nil, fmt.Errorf("pool '%s' not found", poolName)
	}
	// Sin pool específico → primer pool managed
	for _, p := range pools {
		if p.ControlState == ControlStateManaged {
			return p, nil
		}
	}
	// Fallback: primer pool de la lista
	return pools[0], nil
}

// ═══════════════════════════════════════════════════════════════════════
// GET /api/shares
// ═══════════════════════════════════════════════════════════════════════

func sharesListHTTP(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}

	dbShares, err := dbSharesListRaw()
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}

	// Build enriched views with quota/stats from filesystem
	views := buildShareViews(r.Context(), dbShares)

	if session.Role != "admin" {
		// Filter: only shares where this user has permission
		var filtered []map[string]interface{}
		for _, v := range views {
			if perm, ok := v.Permissions[session.Username]; ok && (perm == "rw" || perm == "ro") {
				m := v.ToMap()
				m["myPermission"] = perm
				filtered = append(filtered, m)
			}
		}
		if filtered == nil {
			filtered = []map[string]interface{}{}
		}
		jsonOk(w, filtered)
		return
	}

	// Admin: return all shares
	result := make([]map[string]interface{}, len(views))
	for i, v := range views {
		result[i] = v.ToMap()
	}
	jsonOk(w, result)
}

// ═══════════════════════════════════════════════════════════════════════
// POST /api/shares
// ═══════════════════════════════════════════════════════════════════════

func sharesCreateHTTP(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}

	body, err := readBody(r)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}

	name := strings.TrimSpace(bodyStr(body, "name"))
	description := bodyStr(body, "description")
	poolName := bodyStr(body, "pool")
	quotaBytes := int64(0)
	if qb, ok := body["quotaBytes"].(float64); ok {
		quotaBytes = int64(qb)
	}

	// ── Validación del nombre ──
	if name == "" {
		jsonError(w, 400, "Folder name required")
		return
	}
	if len(name) > 64 {
		jsonError(w, 400, "Folder name too long (max 64 characters)")
		return
	}
	if matched, _ := regexp.MatchString(`[^a-zA-Z0-9_\- ]`, name); matched {
		jsonError(w, 400, "Name can only contain letters, numbers, spaces, -, _")
		return
	}

	safeName := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", "-"))

	// ── Check si ya existe ──
	if existing, _ := dbSharesGetRaw(safeName); existing != nil {
		jsonError(w, 400, "Shared folder already exists")
		return
	}

	// ── Resolver pool desde V2 stack (BTRFS · SQLite) ──
	pool, err := resolveSharePool(r.Context(), poolName)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}

	mountPoint := pool.MountPoint
	volumeName := pool.Name

	// ── Verificar que el pool está realmente montado ──
	if !isPathOnMountedPool(mountPoint) {
		jsonError(w, 503, "Storage pool is not mounted. Check Storage Manager for pool status.")
		return
	}

	folderPath := filepath.Join(mountPoint, "shares", safeName)

	// ── Crear subvolumen BTRFS ──
	// Cada carpeta compartida = un subvolumen BTRFS bajo pool/shares/.
	// Permite snapshots, qgroup quotas y compresión por carpeta.
	opts := CmdOptions{Timeout: 15 * time.Second}

	// ¿Ya existe el subvolumen? (puede pasar si la DB se perdió pero los datos quedaron)
	existing, _ := runCmd("btrfs", []string{"subvolume", "show", folderPath}, opts)
	if existing.Stdout == "" || existing.Code != 0 {
		// Crear subvolumen — auto-mounted al estar dentro del filesystem BTRFS
		_, createErr := runCmd("btrfs", []string{"subvolume", "create", folderPath}, opts)
		if createErr != nil {
			logMsg("ERROR share.create BTRFS subvolume '%s': %s", folderPath, createErr)
			jsonError(w, 500, fmt.Sprintf("Failed to create BTRFS subvolume: %s", createErr))
			return
		}
		logMsg("Created BTRFS subvolume '%s' for share '%s'", folderPath, safeName)

		// Aplicar quota si se especificó
		if quotaBytes > 0 {
			quotaStr := fmt.Sprintf("%d", quotaBytes)
			runCmd("btrfs", []string{"qgroup", "limit", quotaStr, folderPath}, opts)
			logMsg("Set BTRFS quota %d bytes on subvolume '%s'", quotaBytes, folderPath)
		}
	}

	// ── Daemon ops: crear share con permisos de filesystem ──
	daemonResult := handleOp(Request{
		Op:        "share.create",
		ShareName: safeName,
		PoolPath:  mountPoint,
	})

	if !daemonResult.Ok {
		logMsg("ERROR share.create handleOp failed for '%s': %s", safeName, daemonResult.Error)
		jsonError(w, 500, fmt.Sprintf("Failed to create share: %s", daemonResult.Error))
		return
	}

	// ── ACL para que el usuario nimos pueda escribir (NimTorrent etc.) ──
	// Inmediato vía ACL · no requiere reiniciar el daemon
	runCmd("setfacl", []string{"-m", "u:nimos:rwx", folderPath}, CmdOptions{Timeout: 5 * time.Second})
	runCmd("setfacl", []string{"-d", "-m", "u:nimos:rwx", folderPath}, CmdOptions{Timeout: 5 * time.Second})

	// ── Registrar en SQLite ──
	username := session.Username
	if err := dbSharesCreate(safeName, name, description, folderPath, volumeName, volumeName, username); err != nil {
		logMsg("ERROR dbSharesCreate '%s': %s", safeName, err)
		jsonError(w, 500, err.Error())
		return
	}

	// Permiso rw para el creador
	dbShareSetPermission(safeName, username, "rw")

	jsonOk(w, map[string]interface{}{
		"ok":   true,
		"name": safeName,
		"path": folderPath,
		"pool": volumeName,
	})
}

// ═══════════════════════════════════════════════════════════════════════
// PUT /api/shares/:name
// ═══════════════════════════════════════════════════════════════════════

func sharesUpdateHTTP(w http.ResponseWriter, r *http.Request, target string) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}

	share, err := dbSharesGetRaw(target)
	if err != nil || share == nil {
		jsonError(w, 404, "Shared folder not found")
		return
	}

	body, _ := readBody(r)

	// ── Update simple fields (description, recycleBin) ──
	var su ShareUpdate
	hasUpdates := false
	if desc, ok := body["description"].(string); ok {
		su.Description = strPtr(desc)
		hasUpdates = true
	}
	if rb, ok := body["recycleBin"].(bool); ok {
		su.RecycleBin = boolPtr(rb)
		hasUpdates = true
	}
	if hasUpdates {
		dbSharesUpdate(target, su)
	}

	// ── Handle quota change (BTRFS qgroup) ──
	if quotaRaw, ok := body["quota"]; ok {
		quotaBytes := int64(0)
		if qb, ok := quotaRaw.(float64); ok {
			quotaBytes = int64(qb)
		}

		sharPool := share.Pool
		if sharPool == "" {
			sharPool = share.Volume
		}

		pool, err := resolveSharePool(r.Context(), sharPool)
		if err == nil {
			subvolPath := filepath.Join(pool.MountPoint, "shares", target)
			opts := CmdOptions{Timeout: 10 * time.Second}
			if quotaBytes > 0 {
				runCmd("btrfs", []string{"qgroup", "limit", fmt.Sprintf("%d", quotaBytes), subvolPath}, opts)
				logMsg("Updated BTRFS quota to %d bytes on '%s'", quotaBytes, subvolPath)
			} else {
				runCmd("btrfs", []string{"qgroup", "limit", "none", subvolPath}, opts)
				logMsg("Removed BTRFS quota on '%s'", subvolPath)
			}
		}
	}

	// ── Handle permission changes ──
	if permsRaw, ok := body["permissions"]; ok {
		if newPermsMap, ok := permsRaw.(map[string]interface{}); ok {
			oldPerms := share.Permissions
			if oldPerms == nil {
				oldPerms = map[string]string{}
			}

			// Recoger todos los usuarios involucrados (old + new)
			allUsers := map[string]bool{}
			for u := range oldPerms {
				allUsers[u] = true
			}
			for u := range newPermsMap {
				allUsers[u] = true
			}

			for username := range allUsers {
				oldPerm := oldPerms[username]
				newPerm := ""
				if v, ok := newPermsMap[username]; ok {
					newPerm, _ = v.(string)
				}
				if newPerm == "" {
					newPerm = "none"
				}
				if oldPerm == newPerm {
					continue
				}

				switch newPerm {
				case "none":
					handleOp(Request{Op: "share.remove_user", ShareName: target, Username: username})
				case "rw":
					handleOp(Request{Op: "share.add_user_rw", ShareName: target, Username: username})
				case "ro":
					handleOp(Request{Op: "share.add_user_ro", ShareName: target, Username: username})
				}

				dbShareSetPermission(target, username, newPerm)
			}
		}
	}

	// ── Handle app permission changes ──
	if appsRaw, ok := body["appPermissions"]; ok {
		if newApps, ok := appsRaw.([]interface{}); ok {
			// Eliminar apps viejas no presentes en la nueva lista
			for _, oldApp := range share.AppPermissions {
				found := false
				for _, na := range newApps {
					if naMap, ok := na.(map[string]interface{}); ok {
						if uid, err := checkUid(naMap["uid"]); err == nil && uid == oldApp.Uid {
							found = true
							break
						}
					}
				}
				if !found {
					handleOp(Request{Op: "share.remove_app", ShareName: target, AppId: oldApp.AppId, Uid: oldApp.Uid})
				}
			}

			// Añadir/actualizar nuevas apps
			for _, na := range newApps {
				if naMap, ok := na.(map[string]interface{}); ok {
					perm, _ := naMap["permission"].(string)
					appId, _ := naMap["appId"].(string)
					if uid, err := checkUid(naMap["uid"]); err == nil && perm != "" {
						handleOp(Request{Op: "share.add_app", ShareName: target, AppId: appId, Uid: uid, Permission: perm})
					}
				}
			}
		}
	}

	jsonOk(w, map[string]interface{}{"ok": true})
}

// ═══════════════════════════════════════════════════════════════════════
// DELETE /api/shares/:name
// ═══════════════════════════════════════════════════════════════════════

func sharesDeleteHTTP(w http.ResponseWriter, r *http.Request, target string) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}

	share, _ := dbSharesGetRaw(target)
	if share == nil {
		jsonError(w, 404, "Shared folder not found")
		return
	}

	// Remove group y permisos de filesystem
	handleOp(Request{Op: "share.delete", ShareName: target})

	// Destruir el subvolumen BTRFS (con todos los datos dentro)
	sharPool := share.Pool
	if sharPool == "" {
		sharPool = share.Volume
	}

	pool, err := resolveSharePool(r.Context(), sharPool)
	if err == nil {
		subvolPath := filepath.Join(pool.MountPoint, "shares", target)
		opts := CmdOptions{Timeout: 15 * time.Second}

		// Verificar que el subvolumen existe antes de borrarlo
		existing, _ := runCmd("btrfs", []string{"subvolume", "show", subvolPath}, opts)
		if existing.Code == 0 {
			_, delErr := runCmd("btrfs", []string{"subvolume", "delete", subvolPath}, opts)
			if delErr != nil {
				logMsg("WARNING: failed to delete BTRFS subvolume '%s': %s", subvolPath, delErr)
				// No abortamos · seguimos eliminando de DB para mantener consistencia
			} else {
				logMsg("Deleted BTRFS subvolume '%s' for share '%s'", subvolPath, target)
			}
		}
	}

	// Eliminar de la DB
	dbSharesDelete(target)

	jsonOk(w, map[string]interface{}{"ok": true})
}

// ═══════════════════════════════════════════════════════════════════════
// buildShareViews · enriquece DBShares con info del filesystem (quota, uso, stats)
// ═══════════════════════════════════════════════════════════════════════

func buildShareViews(ctx context.Context, dbShares []DBShare) []ShareView {
	opts := CmdOptions{Timeout: 5 * time.Second}
	views := make([]ShareView, 0, len(dbShares))

	for _, s := range dbShares {
		v := ShareView{DBShare: s}

		sharPool := s.Pool
		if sharPool == "" {
			sharPool = s.Volume
		}
		if sharPool == "" || s.Name == "" {
			views = append(views, v)
			continue
		}

		pool, err := resolveSharePool(ctx, sharPool)
		if err != nil {
			// Pool desaparecido · share huérfano · seguimos sin metadata fs
			views = append(views, v)
			continue
		}

		subvolPath := filepath.Join(pool.MountPoint, "shares", s.Name)
		v.PoolType = "btrfs"
		v.MountPoint = subvolPath

		// ── BTRFS qgroup info ──
		res, err := runCmd("btrfs", []string{"subvolume", "show", subvolPath}, opts)
		if err == nil {
			for _, line := range strings.Split(res.Stdout, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "Limit referenced:") {
					valStr := strings.TrimPrefix(line, "Limit referenced:")
					valStr = strings.TrimSpace(valStr)
					if valStr != "-" && valStr != "none" {
						v.Quota = parseHumanBytes(valStr)
					}
				}
				if strings.HasPrefix(line, "Usage referenced:") {
					valStr := strings.TrimPrefix(line, "Usage referenced:")
					v.Used = parseHumanBytes(strings.TrimSpace(valStr))
				}
			}
		}

		// ── Available space desde df ──
		dfRes, err := runCmd("df", []string{"-B1", "--output=avail", subvolPath}, opts)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(dfRes.Stdout), "\n")
			if len(lines) > 1 {
				fmt.Sscanf(strings.TrimSpace(lines[1]), "%d", &v.Available)
			}
		}

		// ── File stats por categoría (video, image, doc, music, archive, code, other) ──
		v.FileStats = getFileStatsByCategory(subvolPath)

		views = append(views, v)
	}

	return views
}

// ═══════════════════════════════════════════════════════════════════════
// getFileStatsByCategory · escanea un dir y agrupa bytes por tipo de archivo
// ═══════════════════════════════════════════════════════════════════════

func getFileStatsByCategory(dirPath string) map[string]int64 {
	stats := map[string]int64{
		"video":    0,
		"image":    0,
		"document": 0,
		"music":    0,
		"archive":  0,
		"code":     0,
		"other":    0,
	}

	// Ejecutar `find` con timeout corto para no bloquear si hay muchos archivos
	opts := CmdOptions{Timeout: 3 * time.Second}
	res, err := runCmd("find", []string{dirPath, "-type", "f", "-printf", "%s %p\n"}, opts)
	if err != nil || res.Stdout == "" {
		return stats
	}

	videoExts := map[string]bool{
		".mp4": true, ".mkv": true, ".avi": true, ".mov": true,
		".webm": true, ".flv": true, ".wmv": true, ".m4v": true, ".mpg": true, ".mpeg": true,
	}
	imageExts := map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
		".webp": true, ".bmp": true, ".svg": true, ".tiff": true, ".heic": true,
	}
	musicExts := map[string]bool{
		".mp3": true, ".flac": true, ".wav": true, ".ogg": true,
		".m4a": true, ".aac": true, ".wma": true, ".opus": true,
	}
	docExts := map[string]bool{
		".pdf": true, ".doc": true, ".docx": true, ".odt": true,
		".txt": true, ".rtf": true, ".xls": true, ".xlsx": true,
		".ppt": true, ".pptx": true, ".csv": true, ".md": true,
	}
	archiveExts := map[string]bool{
		".zip": true, ".rar": true, ".7z": true, ".tar": true,
		".gz": true, ".bz2": true, ".xz": true, ".iso": true,
	}
	codeExts := map[string]bool{
		".go": true, ".py": true, ".js": true, ".ts": true,
		".rs": true, ".c": true, ".cpp": true, ".h": true,
		".html": true, ".css": true, ".json": true, ".xml": true,
		".sh": true, ".rb": true, ".java": true, ".svelte": true,
	}

	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		var size int64
		fmt.Sscanf(parts[0], "%d", &size)
		ext := strings.ToLower(filepath.Ext(parts[1]))

		switch {
		case videoExts[ext]:
			stats["video"] += size
		case imageExts[ext]:
			stats["image"] += size
		case musicExts[ext]:
			stats["music"] += size
		case docExts[ext]:
			stats["document"] += size
		case archiveExts[ext]:
			stats["archive"] += size
		case codeExts[ext]:
			stats["code"] += size
		default:
			stats["other"] += size
		}
	}

	return stats
}

// ═══════════════════════════════════════════════════════════════════════
// parseHumanBytes · convierte "1.5GiB", "500MB", etc. a int64 bytes
// Usado para parsear output de btrfs subvolume show
// ═══════════════════════════════════════════════════════════════════════

func parseHumanBytes(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" || s == "none" {
		return 0
	}

	// Separar número y unidad
	var numStr strings.Builder
	var unitStr strings.Builder
	parsingNum := true
	for _, c := range s {
		if parsingNum && (c >= '0' && c <= '9' || c == '.' || c == ',') {
			numStr.WriteRune(c)
		} else {
			parsingNum = false
			unitStr.WriteRune(c)
		}
	}

	num := 0.0
	fmt.Sscanf(strings.ReplaceAll(numStr.String(), ",", "."), "%f", &num)

	multiplier := int64(1)
	unit := strings.ToUpper(strings.TrimSpace(unitStr.String()))
	switch unit {
	case "K", "KB", "KIB":
		multiplier = 1024
	case "M", "MB", "MIB":
		multiplier = 1024 * 1024
	case "G", "GB", "GIB":
		multiplier = 1024 * 1024 * 1024
	case "T", "TB", "TIB":
		multiplier = 1024 * 1024 * 1024 * 1024
	}

	return int64(num * float64(multiplier))
}
