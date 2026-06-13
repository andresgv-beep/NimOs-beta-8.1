package main

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ═══════════════════════════════════
// File Manager HTTP handlers
// ═══════════════════════════════════

func handleFilesRoutes(w http.ResponseWriter, r *http.Request) {
	urlPath := r.URL.Path
	method := r.Method

	// Upload and download are special (binary, streaming)
	if urlPath == "/api/files/upload" && method == "POST" {
		handleFileUpload(w, r)
		return
	}
	if urlPath == "/api/files/upload-chunk" && method == "POST" {
		handleChunkedUpload(w, r)
		return
	}
	if urlPath == "/api/files/upload-status" && method == "GET" {
		handleUploadStatus(w, r)
		return
	}
	if urlPath == "/api/files/upload-cancel" && method == "POST" {
		handleUploadCancel(w, r)
		return
	}
	if strings.HasPrefix(urlPath, "/api/files/download") && method == "GET" {
		handleFileDownload(w, r)
		return
	}
	// CRIT-008: Generate short-lived download token (replaces session token in URLs)
	if urlPath == "/api/files/download-token" && method == "POST" {
		session := requireAuth(w, r)
		if session == nil {
			return
		}
		body, _ := readBody(r)
		share := bodyStr(body, "share")
		path := bodyStr(body, "path")
		if share == "" || path == "" {
			jsonError(w, 400, "share and path required")
			return
		}
		token, err := dbDownloadTokenCreate(session.Username, session.Role, share, path)
		if err != nil {
			jsonError(w, 500, "Failed to create download token")
			return
		}
		jsonOk(w, map[string]interface{}{"token": token})
		return
	}

	session := requireAuth(w, r)
	if session == nil {
		return
	}

	switch {
	case strings.HasPrefix(urlPath, "/api/files") && method == "GET":
		filesBrowse(w, r, session)
	case urlPath == "/api/files/mkdir" && method == "POST":
		filesMkdir(w, r, session)
	case urlPath == "/api/files/delete" && method == "POST":
		filesDelete(w, r, session)
	case urlPath == "/api/files/rename" && method == "POST":
		filesRename(w, r, session)
	case urlPath == "/api/files/paste" && method == "POST":
		filesPaste(w, r, session)
	case urlPath == "/api/files/zip" && method == "POST":
		filesZip(w, r, session)
	case urlPath == "/api/files/unzip" && method == "POST":
		filesUnzip(w, r, session)
	default:
		jsonError(w, 404, "Not found")
	}
}

// ═══════════════════════════════════
// Permission helpers
// ═══════════════════════════════════

func getSharePermission(session *DBSession, share *ResolvedShare) string {
	// Remote shares: admin gets rw (NFS mount is already authenticated)
	if share.IsRemote() {
		if session.Role == "admin" {
			return "rw"
		}
		return "ro"
	}
	if session.Role == "admin" {
		return "rw"
	}
	if share.Permissions != nil {
		if p, ok := share.Permissions[session.Username]; ok {
			return p
		}
	}
	return "none"
}

// resolveShare looks up a share first in the local DB, then in remote_mounts.
// Returns a share-like map with at least "name" and "path" fields.
func resolveShare(name string) (*ResolvedShare, error) {
	// Try local DB first
	share, err := dbSharesGetRaw(name)
	if err == nil && share != nil {
		return &ResolvedShare{
			Name:        share.Name,
			DisplayName: share.DisplayName,
			Path:        share.Path,
			Pool:        share.Pool,
			Permissions: share.Permissions,
		}, nil
	}

	// Try remote mounts — name format: "remote:<device>/<share>"
	if strings.HasPrefix(name, "remote:") {
		parts := strings.SplitN(strings.TrimPrefix(name, "remote:"), "/", 2)
		if len(parts) == 2 {
			rows, err := db.Query(`SELECT rm.mount_point, rm.share_name, bd.name
				FROM remote_mounts rm JOIN backup_devices bd ON rm.device_id = bd.id`)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var mountPoint, shareName, devName string
					rows.Scan(&mountPoint, &shareName, &devName)
					safeDev := regexp.MustCompile(`[^a-zA-Z0-9_-]`).ReplaceAllString(devName, "_")
					if safeDev == parts[0] && shareName == parts[1] {
						return &ResolvedShare{
							Name:        name,
							DisplayName: fmt.Sprintf("%s (%s)", shareName, devName),
							Path:        mountPoint,
							Pool:        "remote",
							Remote:      &RemoteInfo{Host: devName, DeviceName: safeDev},
						}, nil
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("share not found: %s", name)
}

// isPathOnMountedPool checks that the path is actually on a mounted pool,
// not on the root filesystem. This prevents writes to the system disk
// when a pool is destroyed but shares still exist in the DB.
func isPathOnMountedPool(path string) bool {
	if path == "" {
		return false
	}
	// Debe estar bajo /nimos/pools/
	if !strings.HasPrefix(path, nimosPoolsDir+"/") {
		return false
	}

	// INVARIANTE FUNDAMENTAL DE NimOS: si el pool no está montado, NUNCA se
	// escribe — los datos jamás deben caer al disco de sistema. (Regression
	// 13/06: este check usaba `findmnt --target path`, que RESUELVE HACIA ARRIBA
	// hasta el primer mount existente. Con el pool desmontado, ese mount es `/`
	// (sda2) → devolvía el source de la raíz y, según la jerarquía, podía pasar
	// el filtro → archivos al disco de sistema. Fix: comprobar que el MOUNTPOINT
	// DEL POOL es un punto de montaje real del kernel, sin resolución hacia arriba.)
	//
	// El pool es /nimos/pools/<nombre>; extraemos ese segundo nivel exacto.
	poolMount := poolMountFromPath(path)
	if poolMount == "" {
		return false
	}

	// `mountpoint -q <ruta>` pregunta al kernel: ¿es ESTA ruta exacta un punto
	// de montaje? No resuelve hacia arriba. Si el pool no está montado → false.
	if _, ok := runSafe("mountpoint", "-q", poolMount); !ok {
		return false
	}
	return true
}

// poolMountFromPath devuelve el mountpoint del pool (/nimos/pools/<nombre>) que
// contiene `path`, o "" si path no está bajo /nimos/pools/. Función pura: NO
// extrae más allá del segundo nivel, así una ruta profunda dentro del pool
// resuelve al mountpoint del pool, no a un subdirectorio.
func poolMountFromPath(path string) string {
	if !strings.HasPrefix(path, nimosPoolsDir+"/") {
		return ""
	}
	rest := strings.TrimPrefix(path, nimosPoolsDir+"/")
	poolName := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		poolName = rest[:i]
	}
	if poolName == "" {
		return ""
	}
	return nimosPoolsDir + "/" + poolName
}

// requireShareMounted checks if a share's pool is mounted, returns error response if not
func requireShareMounted(w http.ResponseWriter, share *ResolvedShare) bool {
	// Remote shares: quick check — try to stat the directory (non-blocking)
	// Don't use mountpoint command which can hang on dead NFS
	if share.IsRemote() {
		done := make(chan bool, 1)
		go func() {
			_, err := os.Stat(share.Path)
			done <- (err == nil)
		}()
		select {
		case ok := <-done:
			if ok {
				return true
			}
		case <-time.After(2 * time.Second):
			// Timed out — NFS is dead
		}
		jsonError(w, 503, "Remote share not available — device may be offline")
		return false
	}
	if !isPathOnMountedPool(share.Path) {
		jsonError(w, 503, "Storage pool not mounted — cannot access files")
		return false
	}
	return true
}

// ═══════════════════════════════════
// GET /api/files?share=name&path=/subdir
// ═══════════════════════════════════

func filesBrowse(w http.ResponseWriter, r *http.Request, session *DBSession) {
	shareName := r.URL.Query().Get("share")
	subPath := r.URL.Query().Get("path")
	if subPath == "" {
		subPath = "/"
	}

	if shareName == "" {
		// Return list of accessible shares (local + remote)
		sharesRaw, _ := dbSharesListRaw()
		username := session.Username
		role := session.Role
		var accessible []map[string]interface{}
		for _, s := range sharesRaw {
			perm := "none"
			if role == "admin" {
				perm = "rw"
			} else if p, ok := s.Permissions[username]; ok {
				perm = p
			}
			if perm == "rw" || perm == "ro" {
				accessible = append(accessible, map[string]interface{}{
					"name":        s.Name,
					"displayName": s.DisplayName,
					"description": s.Description,
					"permission":  perm,
				})
			}
		}

		// Add remote mounted shares (admin only for now)
		// NEVER run mountpoint checks here — NFS timeouts would block the entire listing.
		// Just list what's in the DB. Actual mount status is checked when browsing.
		if role == "admin" {
			rows, qerr := db.Query(`SELECT rm.device_id, rm.share_name, rm.mount_point, bd.name
				FROM remote_mounts rm JOIN backup_devices bd ON rm.device_id = bd.id`)
			if qerr == nil {
				defer rows.Close()
				for rows.Next() {
					var devID, shareName, mountPoint, devName string
					rows.Scan(&devID, &shareName, &mountPoint, &devName)
					safeDev := regexp.MustCompile(`[^a-zA-Z0-9_-]`).ReplaceAllString(devName, "_")
					accessible = append(accessible, map[string]interface{}{
						"name":        fmt.Sprintf("remote:%s/%s", safeDev, shareName),
						"displayName": fmt.Sprintf("%s (%s)", shareName, devName),
						"description": "Carpeta remota",
						"permission":  "rw",
						"remote":      true,
						"deviceName":  devName,
					})
				}
			}
		}

		if accessible == nil {
			accessible = []map[string]interface{}{}
		}
		jsonOk(w, map[string]interface{}{"shares": accessible})
		return
	}

	share, err := resolveShare(shareName)
	if err != nil || share == nil {
		jsonError(w, 404, "Shared folder not found")
		return
	}
	if !requireShareMounted(w, share) {
		return
	}

	perm := getSharePermission(session, share)
	if perm == "none" {
		jsonError(w, 403, "Access denied")
		return
	}

	rel, err := relWithinShare(subPath)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}

	root, err := openRootAt(share.Path)
	if err != nil {
		jsonError(w, 500, "Cannot open share")
		return
	}
	defer root.Close()

	dir, err := root.Open(rel)
	if err != nil {
		jsonError(w, 400, "Cannot read directory")
		return
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		jsonError(w, 400, "Cannot read directory")
		return
	}

	var files []map[string]interface{}
	for _, e := range entries {
		info, err := e.Info()
		size := int64(0)
		var modified interface{}
		modified = nil
		if err == nil {
			size = info.Size()
			modified = info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z")
		}
		files = append(files, map[string]interface{}{
			"name":        e.Name(),
			"isDirectory": e.IsDir(),
			"size":        size,
			"modified":    modified,
		})
	}

	// Sort: directories first, then alphabetical
	sort.Slice(files, func(i, j int) bool {
		iDir := files[i]["isDirectory"].(bool)
		jDir := files[j]["isDirectory"].(bool)
		if iDir != jDir {
			return iDir
		}
		return strings.ToLower(files[i]["name"].(string)) < strings.ToLower(files[j]["name"].(string))
	})

	if files == nil {
		files = []map[string]interface{}{}
	}
	jsonOk(w, map[string]interface{}{
		"files":      files,
		"path":       subPath,
		"share":      shareName,
		"permission": perm,
	})
}

// ═══════════════════════════════════
// POST /api/files/mkdir
// ═══════════════════════════════════

func filesMkdir(w http.ResponseWriter, r *http.Request, session *DBSession) {
	body, _ := readBody(r)
	shareName := bodyStr(body, "share")
	dirPath := bodyStr(body, "path")
	dirName := bodyStr(body, "name")

	if shareName == "" || dirName == "" {
		jsonError(w, 400, "Missing share or name")
		return
	}
	if strings.Contains(dirName, "..") || strings.Contains(dirName, "/") || strings.Contains(dirName, "\\") {
		jsonError(w, 400, "Invalid directory name")
		return
	}

	share, _ := resolveShare(shareName)
	if share == nil {
		jsonError(w, 404, "Shared folder not found")
		return
	}
	if !requireShareMounted(w, share) {
		return
	}
	if getSharePermission(session, share) != "rw" {
		jsonError(w, 403, "Write access denied")
		return
	}

	rel, err := relWithinShare(filepath.Join(dirPath, dirName))
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}

	root, err := openRootAt(share.Path)
	if err != nil {
		jsonError(w, 500, "Cannot open share")
		return
	}
	defer root.Close()

	if err := mkdirAllIn(root, rel, 0755); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOk(w, map[string]interface{}{"ok": true})
}

// ═══════════════════════════════════
// POST /api/files/delete
// ═══════════════════════════════════

func filesDelete(w http.ResponseWriter, r *http.Request, session *DBSession) {
	body, _ := readBody(r)
	shareName := bodyStr(body, "share")
	filePath := bodyStr(body, "path")

	if shareName == "" || filePath == "" {
		jsonError(w, 400, "Missing share or path")
		return
	}

	share, _ := resolveShare(shareName)
	if share == nil {
		jsonError(w, 404, "Shared folder not found")
		return
	}
	if !requireShareMounted(w, share) {
		return
	}
	if getSharePermission(session, share) != "rw" {
		jsonError(w, 403, "Write access denied")
		return
	}

	// Camino TOCTOU-safe: ruta relativa + os.Root anclado al share.
	rel, err := relWithinShare(filePath)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if rel == "." {
		jsonError(w, 400, "Cannot delete share root")
		return
	}

	root, err := openRootAt(share.Path)
	if err != nil {
		jsonError(w, 500, "Cannot open share")
		return
	}
	defer root.Close()

	if _, serr := root.Lstat(rel); serr != nil {
		jsonError(w, 404, "File not found")
		return
	}
	if err := removeAllIn(root, rel); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOk(w, map[string]interface{}{"ok": true})
}

// ═══════════════════════════════════
// POST /api/files/rename
// ═══════════════════════════════════

func filesRename(w http.ResponseWriter, r *http.Request, session *DBSession) {
	body, _ := readBody(r)
	shareName := bodyStr(body, "share")
	oldPath := bodyStr(body, "oldPath")
	newPath := bodyStr(body, "newPath")

	if shareName == "" || oldPath == "" || newPath == "" {
		jsonError(w, 400, "Missing params")
		return
	}

	share, _ := resolveShare(shareName)
	if share == nil {
		jsonError(w, 404, "Shared folder not found")
		return
	}
	if !requireShareMounted(w, share) {
		return
	}
	if getSharePermission(session, share) != "rw" {
		jsonError(w, 403, "Write access denied")
		return
	}

	relOld, err := relWithinShare(oldPath)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	relNew, err := relWithinShare(newPath)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if relOld == "." || relNew == "." {
		jsonError(w, 400, "Cannot rename share root")
		return
	}

	root, err := openRootAt(share.Path)
	if err != nil {
		jsonError(w, 500, "Cannot open share")
		return
	}
	defer root.Close()

	if err := renameIn(root, relOld, relNew); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOk(w, map[string]interface{}{"ok": true})
}

// ═══════════════════════════════════
// POST /api/files/paste (copy or move)
// ═══════════════════════════════════

func filesPaste(w http.ResponseWriter, r *http.Request, session *DBSession) {
	body, _ := readBody(r)
	srcShareName := bodyStr(body, "srcShare")
	srcPath := bodyStr(body, "srcPath")
	destShareName := bodyStr(body, "destShare")
	destPath := bodyStr(body, "destPath")
	action := bodyStr(body, "action")

	if srcShareName == "" || srcPath == "" || destShareName == "" || destPath == "" {
		jsonError(w, 400, "Missing params")
		return
	}

	srcShare, _ := resolveShare(srcShareName)
	destShare, _ := resolveShare(destShareName)
	if srcShare == nil || destShare == nil {
		jsonError(w, 404, "Share not found")
		return
	}
	if !requireShareMounted(w, destShare) {
		return
	}

	if getSharePermission(session, destShare) != "rw" {
		jsonError(w, 403, "Write access denied on destination")
		return
	}
	srcPerm := getSharePermission(session, srcShare)
	if srcPerm == "none" {
		jsonError(w, 403, "Read access denied on source")
		return
	}

	relSrc, err := relWithinShare(srcPath)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	relDest, err := relWithinShare(destPath)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if relSrc == "." {
		jsonError(w, 400, "Cannot move/copy share root")
		return
	}

	srcRoot, err := openRootAt(srcShare.Path)
	if err != nil {
		jsonError(w, 500, "Cannot open source share")
		return
	}
	defer srcRoot.Close()

	sameShare := srcShare.Path == destShare.Path
	var destRoot *os.Root
	if sameShare {
		destRoot = srcRoot
	} else {
		destRoot, err = openRootAt(destShare.Path)
		if err != nil {
			jsonError(w, 500, "Cannot open destination share")
			return
		}
		defer destRoot.Close()
	}

	srcInfo, statErr := srcRoot.Lstat(relSrc)
	if statErr != nil {
		jsonError(w, 404, "Source not found")
		return
	}

	// ── CUT (move) ──────────────────────────────────────────────────────
	if action == "cut" {
		if sameShare {
			// Mismo share/pool: rename atómico (mismo inode, instantáneo).
			if err := renameIn(srcRoot, relSrc, relDest); err != nil {
				jsonError(w, 500, err.Error())
				return
			}
			jsonOk(w, map[string]interface{}{"ok": true})
			return
		}

		// Cross-share: copia segura + borrado. Verificar espacio antes (SEC-3).
		srcSize := pasteSrcSize(srcRoot, relSrc, srcInfo)
		if !checkDestSpace(w, destShare.Path, srcSize) {
			return
		}
		if err := crossRootCopyTree(srcRoot, relSrc, destRoot, relDest); err != nil {
			// Limpieza parcial del destino ante fallo
			removeAllIn(destRoot, relDest)
			jsonError(w, 500, "Copy failed during cross-share move")
			return
		}
		if err := removeAllIn(srcRoot, relSrc); err != nil {
			logMsg("WARNING paste cut: copia OK pero borrado de origen falló: %s", err)
		}
		jsonOk(w, map[string]interface{}{"ok": true})
		return
	}

	// ── COPY ────────────────────────────────────────────────────────────
	srcSize := pasteSrcSize(srcRoot, relSrc, srcInfo)
	if !checkDestSpace(w, destShare.Path, srcSize) {
		return
	}
	if sameShare {
		if err := copyTreeIn(srcRoot, relSrc, relDest); err != nil {
			removeAllIn(srcRoot, relDest)
			jsonError(w, 500, "Copy failed")
			return
		}
	} else {
		if err := crossRootCopyTree(srcRoot, relSrc, destRoot, relDest); err != nil {
			removeAllIn(destRoot, relDest)
			jsonError(w, 500, "Copy failed")
			return
		}
	}
	jsonOk(w, map[string]interface{}{"ok": true})
}

// pasteSrcSize devuelve el tamaño del origen (fichero o árbol) de forma
// TOCTOU-safe vía root, para los checks de quota. Reemplaza el shell-out a `du`.
func pasteSrcSize(root *os.Root, rel string, info os.FileInfo) int64 {
	if info.IsDir() {
		sz, err := dirSizeIn(root, rel)
		if err != nil {
			return 0
		}
		return sz
	}
	return info.Size()
}

// checkDestSpace verifica que destSharePath tenga hueco para srcSize bytes.
// Escribe el error HTTP y devuelve false si no cabe. availableBytes==-1
// (desconocido) permite la operación.
func checkDestSpace(w http.ResponseWriter, destSharePath string, srcSize int64) bool {
	availableBytes := getAvailableBytes(destSharePath)
	if availableBytes == 0 {
		jsonError(w, 507, "Disk quota exceeded — no space available on destination")
		return false
	}
	if srcSize > 0 && availableBytes > 0 && srcSize > availableBytes {
		jsonError(w, 507, fmt.Sprintf("Not enough space. Source: %s, Available: %s",
			fmtSizeFiles(srcSize), fmtSizeFiles(availableBytes)))
		return false
	}
	return true
}

// ═══════════════════════════════════
// POST /api/files/upload (multipart)
// ═══════════════════════════════════

func handleFileUpload(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}

	// Legacy multipart upload — ONLY for small files (Notes save, config import, etc.)
	// Large files (20GB+) MUST use /api/files/upload-chunk which streams to disk
	// via io.Copy without RAM buffering. Caddy streams request bodies by default.
	if r.ContentLength > 50*1024*1024 {
		jsonError(w, 413, "File too large. Use chunked upload for files over 50MB.")
		return
	}

	// Hard limit on request body to prevent RAM abuse
	r.Body = http.MaxBytesReader(w, r.Body, 50*1024*1024)

	// Parse multipart — buffer 8MB in RAM max, rest spills to temp files
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		jsonError(w, 400, "Failed to parse upload")
		return
	}

	shareName := r.FormValue("share")
	uploadPath := r.FormValue("path")

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, 400, "No file in upload")
		return
	}
	defer file.Close()

	if shareName == "" {
		jsonError(w, 400, "Missing share")
		return
	}

	share, _ := resolveShare(shareName)
	if share == nil {
		jsonError(w, 404, "Share not found")
		return
	}
	if !requireShareMounted(w, share) {
		return
	}
	if getSharePermission(session, share) != "rw" {
		jsonError(w, 403, "Write access denied")
		return
	}

	// Reject filenames with path traversal attempts in the raw input
	rawFilename := header.Filename
	if strings.Contains(rawFilename, "..") || strings.Contains(rawFilename, "/") || strings.Contains(rawFilename, "\\") {
		jsonError(w, 400, "Invalid filename")
		return
	}

	// Sanitize filename
	fileName := sanitizeFileName(rawFilename)
	if fileName == "" || len(fileName) > 255 {
		jsonError(w, 400, "Invalid filename")
		return
	}

	// Reject path traversal in upload path
	if strings.Contains(uploadPath, "..") {
		jsonError(w, 400, "Invalid upload path")
		return
	}

	sharePath := share.Path
	rel, err := relWithinShare(filepath.Join(uploadPath, fileName))
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}

	root, err := openRootAt(sharePath)
	if err != nil {
		jsonError(w, 500, "Cannot open share")
		return
	}
	defer root.Close()

	// Check available space before writing
	availableBytes := getAvailableBytes(sharePath)
	fileSize := header.Size

	logMsg("upload: share=%s path=%s fileSize=%d availableBytes=%d", shareName, sharePath, fileSize, availableBytes)

	// Reject if we know the file is too big
	if fileSize > 0 && availableBytes >= 0 && fileSize > availableBytes {
		jsonError(w, 507, fmt.Sprintf("Not enough space. File: %s, Available: %s",
			fmtSizeFiles(fileSize), fmtSizeFiles(availableBytes)))
		return
	}

	// Also reject if available is 0 (quota full)
	if availableBytes == 0 {
		jsonError(w, 507, "Disk quota exceeded — no space available")
		return
	}

	// Cap write at available space
	maxWrite := availableBytes
	if maxWrite <= 0 {
		maxWrite = 500 * 1024 * 1024 // fallback 500MB if check fails
	}

	// Ensure parent dir exists (vía root, TOCTOU-safe)
	if err := mkdirAllIn(root, relDir(rel), 0755); err != nil {
		jsonError(w, 500, err.Error())
		return
	}

	dst, err := root.Create(rel)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}

	// Write with size limit — never write more than available space
	written, copyErr := io.CopyN(dst, file, maxWrite)
	dst.Close()

	if copyErr != nil && copyErr != io.EOF {
		// Write failed — clean up partial file
		root.Remove(rel)
		jsonError(w, 507, "Write failed — disk full or quota exceeded")
		return
	}

	// Check if the file was truncated (more data remains but we hit the limit)
	if copyErr != io.EOF {
		// We wrote maxWrite bytes but there's more data — file was too big
		root.Remove(rel)
		jsonError(w, 507, fmt.Sprintf("File too large for available space. Written: %s, Available: %s",
			fmtSizeFiles(written), fmtSizeFiles(availableBytes)))
		return
	}

	jsonOk(w, map[string]interface{}{"ok": true, "name": fileName})
}

// ═══════════════════════════════════
// POST /api/files/upload-chunk (streaming chunked upload)
// ═══════════════════════════════════
//
// Receives file in chunks. Each request sends one chunk with headers:
//   X-Share, X-Path, X-Filename, X-Chunk-Index, X-Total-Chunks, X-Total-Size
// Body = raw binary chunk data (no multipart)

func handleChunkedUpload(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}

	shareName := r.Header.Get("X-Share")
	uploadPath := r.Header.Get("X-Path")
	rawFilename := r.Header.Get("X-Filename")
	chunkIdx := r.Header.Get("X-Chunk-Index")
	totalChunks := r.Header.Get("X-Total-Chunks")
	totalSizeStr := r.Header.Get("X-Total-Size")

	if shareName == "" || rawFilename == "" || chunkIdx == "" || totalChunks == "" {
		jsonError(w, 400, "Missing chunk headers")
		return
	}

	idx, _ := strconv.Atoi(chunkIdx)
	total, _ := strconv.Atoi(totalChunks)
	totalSize, _ := strconv.ParseInt(totalSizeStr, 10, 64)

	if idx < 0 || total <= 0 {
		jsonError(w, 400, "Invalid chunk index/total")
		return
	}

	// Validate share
	share, _ := resolveShare(shareName)
	if share == nil {
		jsonError(w, 404, "Share not found")
		return
	}
	if !requireShareMounted(w, share) {
		return
	}
	if getSharePermission(session, share) != "rw" {
		jsonError(w, 403, "Write access denied")
		return
	}

	// Sanitize
	fileName := sanitizeFileName(rawFilename)
	if fileName == "" || len(fileName) > 255 {
		jsonError(w, 400, "Invalid filename")
		return
	}
	if strings.Contains(uploadPath, "..") {
		jsonError(w, 400, "Invalid upload path")
		return
	}

	sharePath := share.Path
	rel, err := relWithinShare(filepath.Join(uploadPath, fileName))
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}

	root, err := openRootAt(sharePath)
	if err != nil {
		jsonError(w, 500, "Cannot open share")
		return
	}
	defer root.Close()

	// On first chunk, check available space
	if idx == 0 && totalSize > 0 {
		availableBytes := getAvailableBytes(sharePath)
		if availableBytes == 0 {
			jsonError(w, 507, "Disk quota exceeded — no space available")
			return
		}
		if availableBytes > 0 && totalSize > availableBytes {
			jsonError(w, 507, fmt.Sprintf("Not enough space. File: %s, Available: %s",
				fmtSizeFiles(totalSize), fmtSizeFiles(availableBytes)))
			return
		}
	}

	// Store chunks on the destination pool (not system disk), vía root.
	tmpDirRel := joinRel(".nimchunks", fmt.Sprintf("%x", hashStr(uploadPath+fileName)))
	if err := mkdirAllIn(root, tmpDirRel, 0755); err != nil {
		jsonError(w, 500, "Cannot create chunk dir")
		return
	}

	// Write this chunk to temp file
	chunkRel := joinRel(tmpDirRel, fmt.Sprintf("chunk_%05d", idx))
	dst, err := root.Create(chunkRel)
	if err != nil {
		jsonError(w, 500, "Cannot create chunk file")
		return
	}
	_, err = io.Copy(dst, r.Body)
	dst.Close()
	if err != nil {
		root.Remove(chunkRel)
		jsonError(w, 500, "Chunk write failed")
		return
	}

	// If this is the last chunk, assemble the file
	if idx == total-1 {
		if err := mkdirAllIn(root, relDir(rel), 0755); err != nil {
			jsonError(w, 500, err.Error())
			removeAllIn(root, tmpDirRel)
			return
		}

		finalFile, err := root.Create(rel)
		if err != nil {
			jsonError(w, 500, err.Error())
			removeAllIn(root, tmpDirRel)
			return
		}

		// Concatenate all chunks in order
		var writeErr error
		for i := 0; i < total; i++ {
			cfRel := joinRel(tmpDirRel, fmt.Sprintf("chunk_%05d", i))
			chunk, err := root.Open(cfRel)
			if err != nil {
				writeErr = fmt.Errorf("missing chunk %d", i)
				break
			}
			_, err = io.Copy(finalFile, chunk)
			chunk.Close()
			if err != nil {
				writeErr = fmt.Errorf("write error at chunk %d: %v", i, err)
				break
			}
		}
		finalFile.Close()

		// Cleanup temp chunks
		removeAllIn(root, tmpDirRel)

		if writeErr != nil {
			root.Remove(rel)
			jsonError(w, 500, writeErr.Error())
			return
		}

		jsonOk(w, map[string]interface{}{"ok": true, "name": fileName, "assembled": true})
		return
	}

	// Not the last chunk — acknowledge
	jsonOk(w, map[string]interface{}{"ok": true, "chunk": idx})
}

// ═══════════════════════════════════
// POST /api/files/zip — compress selected files/folders into a .zip
// ═══════════════════════════════════
// Body: { share, paths: ["/file1", "/dir1", ...], name?: "archive.zip" }
// Creates the zip in the same directory as the first path.

func filesZip(w http.ResponseWriter, r *http.Request, session *DBSession) {
	body, err := readBody(r)
	if err != nil {
		jsonError(w, 400, "Invalid request")
		return
	}

	shareName := bodyStr(body, "share")
	zipName := bodyStr(body, "name")

	rawPaths, ok := body["paths"].([]interface{})
	if !ok || len(rawPaths) == 0 || shareName == "" {
		jsonError(w, 400, "Missing share or paths")
		return
	}

	share, _ := resolveShare(shareName)
	if share == nil {
		jsonError(w, 404, "Share not found")
		return
	}
	if !requireShareMounted(w, share) {
		return
	}
	if getSharePermission(session, share) != "rw" {
		jsonError(w, 403, "Write access denied")
		return
	}

	root, err := openRootAt(share.Path)
	if err != nil {
		jsonError(w, 500, "Cannot open share")
		return
	}
	defer root.Close()

	// Collect and validate paths (relativas al share)
	var relPaths []string
	var relNames []string
	for _, rp := range rawPaths {
		p, ok := rp.(string)
		if !ok || p == "" {
			continue
		}
		rel, err := relWithinShare(p)
		if err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		if rel == "." {
			jsonError(w, 400, "Cannot zip share root")
			return
		}
		if _, err := root.Lstat(rel); err != nil {
			jsonError(w, 404, fmt.Sprintf("Not found: %s", relBase(rel)))
			return
		}
		relPaths = append(relPaths, rel)
		relNames = append(relNames, relBase(rel))
	}

	if len(relPaths) == 0 {
		jsonError(w, 400, "No valid paths")
		return
	}

	// Determine zip file destination (same dir as first path)
	destDirRel := relDir(relPaths[0])
	if zipName == "" {
		if len(relPaths) == 1 {
			zipName = relNames[0] + ".zip"
		} else {
			zipName = "archive.zip"
		}
	}
	if !strings.HasSuffix(strings.ToLower(zipName), ".zip") {
		zipName += ".zip"
	}
	zipName = sanitizeFileName(zipName)
	zipRel := joinRel(destDirRel, zipName)

	// Avoid overwriting — add suffix if exists
	if _, err := root.Lstat(zipRel); err == nil {
		base := strings.TrimSuffix(zipName, ".zip")
		for i := 1; i < 100; i++ {
			candidate := joinRel(destDirRel, fmt.Sprintf("%s (%d).zip", base, i))
			if _, err := root.Lstat(candidate); err != nil {
				zipRel = candidate
				zipName = relBase(candidate)
				break
			}
		}
	}

	// Create zip file (vía root)
	zipFile, err := root.Create(zipRel)
	if err != nil {
		jsonError(w, 500, "Cannot create zip file")
		return
	}

	zw := zip.NewWriter(zipFile)

	var walkErr error
	for i, srcRel := range relPaths {
		baseName := relNames[i]

		entries, err := walkIn(root, srcRel)
		if err != nil {
			walkErr = err
			break
		}

		for _, e := range entries {
			// Skip symlinks (anti-fuga)
			if e.Info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			// Skip the zip file itself
			if e.Rel == zipRel {
				continue
			}
			// Skip .nimchunks
			if e.IsDir && relBase(e.Rel) == ".nimchunks" {
				continue
			}

			// Nombre de entrada dentro del zip: baseName + ruta relativa al srcRel
			var entryName string
			if e.Rel == srcRel {
				entryName = baseName
			} else {
				sub := strings.TrimPrefix(e.Rel, srcRel+"/")
				entryName = baseName + "/" + sub
			}

			if e.IsDir {
				if _, err := zw.Create(entryName + "/"); err != nil {
					walkErr = err
					break
				}
				continue
			}

			header, err := zip.FileInfoHeader(e.Info)
			if err != nil {
				walkErr = err
				break
			}
			header.Name = entryName
			header.Method = zip.Deflate

			writer, err := zw.CreateHeader(header)
			if err != nil {
				walkErr = err
				break
			}

			f, err := root.Open(e.Rel)
			if err != nil {
				walkErr = err
				break
			}
			_, err = io.Copy(writer, f)
			f.Close()
			if err != nil {
				walkErr = err
				break
			}
		}

		if walkErr != nil {
			break
		}
	}

	zw.Close()
	zipFile.Close()

	if walkErr != nil {
		root.Remove(zipRel)
		jsonError(w, 500, fmt.Sprintf("Zip failed: %v", walkErr))
		return
	}

	logMsg("zip: created %s in share %s", zipName, shareName)
	jsonOk(w, map[string]interface{}{"ok": true, "name": zipName})
}

// ═══════════════════════════════════
// POST /api/files/unzip — extract a .zip file
// ═══════════════════════════════════
// Body: { share, path: "/path/to/file.zip" }
// Extracts into a folder with the same name (without .zip) in the same directory.

func filesUnzip(w http.ResponseWriter, r *http.Request, session *DBSession) {
	body, err := readBody(r)
	if err != nil {
		jsonError(w, 400, "Invalid request")
		return
	}

	shareName := bodyStr(body, "share")
	filePath := bodyStr(body, "path")

	if shareName == "" || filePath == "" {
		jsonError(w, 400, "Missing share or path")
		return
	}
	if strings.Contains(filePath, "..") {
		jsonError(w, 400, "Invalid path")
		return
	}

	share, _ := resolveShare(shareName)
	if share == nil {
		jsonError(w, 404, "Share not found")
		return
	}
	if !requireShareMounted(w, share) {
		return
	}
	if getSharePermission(session, share) != "rw" {
		jsonError(w, 403, "Write access denied")
		return
	}

	relZip, err := relWithinShare(filePath)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}

	// Verify it's a zip file
	if !strings.HasSuffix(strings.ToLower(relZip), ".zip") {
		jsonError(w, 400, "Not a zip file")
		return
	}

	root, err := openRootAt(share.Path)
	if err != nil {
		jsonError(w, 500, "Cannot open share")
		return
	}
	defer root.Close()

	// Abrir el zip vía root (TOCTOU-safe) y leerlo como ReaderAt.
	zf, err := root.Open(relZip)
	if err != nil {
		jsonError(w, 404, "Zip file not found")
		return
	}
	zfStat, err := zf.Stat()
	if err != nil {
		zf.Close()
		jsonError(w, 500, "Cannot stat zip")
		return
	}
	zr, err := zip.NewReader(zf, zfStat.Size())
	if err != nil {
		zf.Close()
		jsonError(w, 400, fmt.Sprintf("Cannot open zip: %v", err))
		return
	}
	defer zf.Close()

	// Carpeta destino (relativa), evitando sobrescritura
	baseName := strings.TrimSuffix(relBase(relZip), ".zip")
	baseName = strings.TrimSuffix(baseName, ".ZIP")
	parentRel := relDir(relZip)
	destRel := joinRel(parentRel, baseName)

	if _, err := root.Lstat(destRel); err == nil {
		for i := 1; i < 100; i++ {
			candidate := joinRel(parentRel, fmt.Sprintf("%s (%d)", baseName, i))
			if _, err := root.Lstat(candidate); err != nil {
				destRel = candidate
				break
			}
		}
	}

	if err := mkdirAllIn(root, destRel, 0755); err != nil {
		jsonError(w, 500, "Cannot create destination folder")
		return
	}

	var count, skipped int
	for _, f := range zr.File {
		// Defensa Zip Slip nivel 1: rechazar nombres con "..".
		// (Nivel 2: os.Root ancla la escritura al share igualmente.)
		entryRel, rerr := relWithinShare(joinRel(destRel, f.Name))
		if rerr != nil {
			skipped++
			continue
		}

		if f.FileInfo().IsDir() {
			if err := mkdirAllIn(root, entryRel, 0755); err != nil {
				skipped++
			}
			continue
		}

		// Asegurar padre
		if err := mkdirAllIn(root, relDir(entryRel), 0755); err != nil {
			skipped++
			continue
		}

		rc, err := f.Open()
		if err != nil {
			skipped++
			continue
		}
		dst, err := root.Create(entryRel)
		if err != nil {
			rc.Close()
			skipped++
			continue
		}
		_, copyErr := io.Copy(dst, rc)
		dst.Close()
		rc.Close()
		if copyErr != nil {
			root.Remove(entryRel)
			skipped++
			continue
		}
		count++
	}

	logMsg("unzip: extracted %d files (%d skipped) to %s in share %s", count, skipped, destRel, shareName)
	resp := map[string]interface{}{"ok": true, "count": count, "folder": relBase(destRel)}
	if skipped > 0 {
		resp["skipped"] = skipped
	}
	jsonOk(w, resp)
}

func hashStr(s string) uint32 {
	var h uint32
	for _, c := range s {
		h = h*31 + uint32(c)
	}
	return h
}

// GET /api/files/upload-status?share=X&path=Y&filename=Z
// Returns which chunks already exist for a partial upload (for resume).
func handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}

	shareName := r.URL.Query().Get("share")
	uploadPath := r.URL.Query().Get("path")
	fileName := sanitizeFileName(r.URL.Query().Get("filename"))

	if shareName == "" || fileName == "" {
		jsonError(w, 400, "Missing share or filename")
		return
	}

	share, _ := resolveShare(shareName)
	if share == nil {
		jsonError(w, 404, "Share not found")
		return
	}
	if getSharePermission(session, share) != "rw" {
		jsonError(w, 403, "Write access denied")
		return
	}

	root, err := openRootAt(share.Path)
	if err != nil {
		jsonOk(w, map[string]interface{}{"ok": true, "chunks": []int{}, "count": 0})
		return
	}
	defer root.Close()

	tmpDirRel := joinRel(".nimchunks", fmt.Sprintf("%x", hashStr(uploadPath+fileName)))
	dir, err := root.Open(tmpDirRel)
	if err != nil {
		// No chunks exist — fresh upload
		jsonOk(w, map[string]interface{}{"ok": true, "chunks": []int{}, "count": 0})
		return
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		jsonOk(w, map[string]interface{}{"ok": true, "chunks": []int{}, "count": 0})
		return
	}

	var existing []int
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "chunk_") {
			idx, err := strconv.Atoi(strings.TrimPrefix(e.Name(), "chunk_"))
			if err == nil {
				existing = append(existing, idx)
			}
		}
	}
	sort.Ints(existing)

	jsonOk(w, map[string]interface{}{"ok": true, "chunks": existing, "count": len(existing)})
}

// POST /api/files/upload-cancel { share, path, filename }
// Cleans up partial chunks for a cancelled upload.
func handleUploadCancel(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}

	body, _ := readBody(r)
	shareName := bodyStr(body, "share")
	uploadPath := bodyStr(body, "path")
	fileName := sanitizeFileName(bodyStr(body, "filename"))

	if shareName == "" || fileName == "" {
		jsonError(w, 400, "Missing share or filename")
		return
	}

	share, _ := resolveShare(shareName)
	if share == nil {
		jsonError(w, 404, "Share not found")
		return
	}
	if getSharePermission(session, share) != "rw" {
		jsonError(w, 403, "Write access denied")
		return
	}

	root, err := openRootAt(share.Path)
	if err != nil {
		jsonOk(w, map[string]interface{}{"ok": true})
		return
	}
	defer root.Close()

	tmpDirRel := joinRel(".nimchunks", fmt.Sprintf("%x", hashStr(uploadPath+fileName)))
	removeAllIn(root, tmpDirRel)

	jsonOk(w, map[string]interface{}{"ok": true})
}

func sanitizeFileName(name string) string {
	// Extract only the base filename — strip any directory path components
	name = filepath.Base(name)
	// Reject . and .. explicitly
	if name == "." || name == ".." || name == "" {
		return ""
	}
	// Remove dangerous characters
	re := regexp.MustCompile(`[\/\\:*?"<>|]`)
	name = re.ReplaceAllString(name, "_")
	name = strings.ReplaceAll(name, "..", "")
	// Remove null bytes
	name = strings.ReplaceAll(name, "\x00", "")
	// Trim leading dots (hidden files on Linux)
	// This is optional — uncomment if you want to prevent hidden file creation
	// name = strings.TrimLeft(name, ".")
	if name == "" {
		return ""
	}
	return name
}

// ═══════════════════════════════════
// GET /api/files/download?share=...&path=...&token=...
// ═══════════════════════════════════

func handleFileDownload(w http.ResponseWriter, r *http.Request) {
	// CRIT-008: Try one-time download token first (short-lived, no browser history leak)
	dlToken := r.URL.Query().Get("dl")
	if dlToken != "" {
		username, role, dlShare, dlPath, err := dbDownloadTokenConsume(dlToken)
		if err != nil {
			jsonError(w, 401, "Invalid or expired download token")
			return
		}
		// Token is valid and consumed — serve the file
		share, _ := resolveShare(dlShare)
		if share == nil {
			jsonError(w, 404, "Share not found")
			return
		}
		// SEC-2: preservar el role del token para que un admin conserve su
		// acceso rw automático (antes se descartaba y caía al map de perms).
		tempSession := &DBSession{Username: username, Role: role}
		if getSharePermission(tempSession, share) == "none" {
			jsonError(w, 403, "Access denied")
			return
		}
		rel, pathErr := relWithinShare(dlPath)
		if pathErr != nil {
			jsonError(w, 400, pathErr.Error())
			return
		}
		root, oerr := openRootAt(share.Path)
		if oerr != nil {
			jsonError(w, 500, "Cannot open share")
			return
		}
		defer root.Close()
		serveFileDownload(w, r, root, rel)
		return
	}

	// Fallback: Auth via session token (legacy — will be removed)
	token := r.URL.Query().Get("token")
	if token == "" {
		token = getBearerToken(r)
	}
	if token == "" {
		jsonError(w, 401, "Not authenticated")
		return
	}
	hashed := sha256Hex(token)
	session, err := dbSessionGet(hashed)
	if err != nil {
		jsonError(w, 401, "Not authenticated")
		return
	}

	shareName := r.URL.Query().Get("share")
	filePath := r.URL.Query().Get("path")
	if shareName == "" || filePath == "" {
		jsonError(w, 400, "Missing params")
		return
	}

	share, _ := resolveShare(shareName)
	if share == nil {
		jsonError(w, 404, "Share not found")
		return
	}
	if getSharePermission(session, share) == "none" {
		jsonError(w, 403, "Access denied")
		return
	}

	rel, pathErr := relWithinShare(filePath)
	if pathErr != nil {
		jsonError(w, 400, pathErr.Error())
		return
	}

	root, oerr := openRootAt(share.Path)
	if oerr != nil {
		jsonError(w, 500, "Cannot open share")
		return
	}
	defer root.Close()

	serveFileDownload(w, r, root, rel)
}

// serveFileDownload sends a file to the client with appropriate headers.
// Opera vía os.Root: rel es la ruta relativa al share, ya validada.
func serveFileDownload(w http.ResponseWriter, r *http.Request, root *os.Root, rel string) {
	stat, err := root.Stat(rel)
	if err != nil {
		jsonError(w, 404, "File not found")
		return
	}

	fileName := relBase(rel)
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext != "" {
		ext = ext[1:] // remove dot
	}

	mimeTypes := map[string]string{
		"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif",
		"webp": "image/webp", "svg": "image/svg+xml", "bmp": "image/bmp", "ico": "image/x-icon",
		"mp4": "video/mp4", "webm": "video/webm", "ogg": "video/ogg", "mov": "video/quicktime",
		"mkv": "video/x-matroska", "avi": "video/x-msvideo", "ogv": "video/ogg",
		"mp3": "audio/mpeg", "wav": "audio/wav", "flac": "audio/flac", "aac": "audio/aac",
		"m4a": "audio/mp4", "wma": "audio/x-ms-wma", "opus": "audio/opus",
		"pdf": "application/pdf",
		"txt": "text/plain", "md": "text/plain", "log": "text/plain", "csv": "text/plain",
		"json": "application/json", "xml": "text/xml", "yml": "text/yaml", "yaml": "text/yaml",
		"js": "text/javascript", "jsx": "text/javascript", "ts": "text/javascript",
		"py": "text/plain", "sh": "text/plain", "css": "text/css", "html": "text/html",
		"c": "text/plain", "cpp": "text/plain", "h": "text/plain", "java": "text/plain",
		"rs": "text/plain", "go": "text/plain", "rb": "text/plain", "php": "text/plain",
		"sql": "text/plain", "toml": "text/plain", "ini": "text/plain", "conf": "text/plain",
		"srt": "text/plain", "sub": "text/plain", "ass": "text/plain", "vtt": "text/vtt",
		"zip": "application/zip", "tar": "application/x-tar", "gz": "application/gzip",
		"7z": "application/x-7z-compressed", "rar": "application/x-rar-compressed",
	}

	contentType := "application/octet-stream"
	if ct, ok := mimeTypes[ext]; ok {
		contentType = ct
	}
	isDownload := contentType == "application/octet-stream"

	// Range request support (audio/video seeking)
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" {
		re := regexp.MustCompile(`bytes=(\d+)-(\d*)`)
		m := re.FindStringSubmatch(rangeHeader)
		if m != nil {
			start, _ := strconv.ParseInt(m[1], 10, 64)
			end := stat.Size() - 1
			if m[2] != "" {
				end, _ = strconv.ParseInt(m[2], 10, 64)
			}
			chunkSize := end - start + 1

			f, err := root.Open(rel)
			if err != nil {
				jsonError(w, 500, "Cannot open file")
				return
			}
			defer f.Close()
			f.Seek(start, 0)

			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, stat.Size()))
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", chunkSize))
			w.WriteHeader(206)
			io.CopyN(w, f, chunkSize)
			return
		}
	}

	// Full file
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	w.Header().Set("Accept-Ranges", "bytes")
	if isDownload {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
	}
	w.WriteHeader(200)

	f, err := root.Open(rel)
	if err != nil {
		return
	}
	defer f.Close()
	io.Copy(w, f)
}

// getAvailableBytes returns available bytes for writing to the given path.
// For BTRFS subvolumes with quota, uses btrfs subvolume show (quota limit - usage).
// For ZFS datasets with quota, uses zfs get.
// Falls back to df for other filesystems.
// Returns -1 if space cannot be determined (caller should allow the operation).
func getAvailableBytes(path string) int64 {
	// Try BTRFS quota first
	if out, ok := runSafe("btrfs", "subvolume", "show", path); ok && out != "" {
		var limitBytes, usedBytes int64
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Limit referenced:") {
				val := strings.TrimSpace(strings.TrimPrefix(line, "Limit referenced:"))
				if val != "-" && val != "none" {
					limitBytes = parseHumanBytesFiles(val)
				}
			}
			if strings.HasPrefix(line, "Usage referenced:") {
				val := strings.TrimSpace(strings.TrimPrefix(line, "Usage referenced:"))
				usedBytes = parseHumanBytesFiles(val)
			}
		}
		if limitBytes > 0 {
			avail := limitBytes - usedBytes
			if avail < 0 {
				avail = 0
			}
			return avail
		}
		// BTRFS subvolume without quota — fall through to df
	}

	// Beta 8.1: rama ZFS eliminada. La función ahora intenta:
	//   1. BTRFS qgroup quota (arriba)
	//   2. df como fallback (abajo) — funciona para cualquier FS montado
	//
	// La rama ZFS antigua ejecutaba `zfs get available <dataset>` para
	// resolver quotas a nivel de subvolume. Ya no aplica.

	// Fallback to df
	out, ok := runSafe("df", "-B1", "--output=avail", path)
	if !ok || strings.TrimSpace(out) == "" {
		out, ok = runSafe("sudo", "df", "-B1", "--output=avail", path)
	}
	if ok {
		// Parse the last non-empty line (skip header)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			s := strings.TrimSpace(lines[i])
			if s != "" && s != "Avail" {
				var n int64
				fmt.Sscanf(s, "%d", &n)
				if n > 0 {
					return n
				}
				break
			}
		}
	}

	// Cannot determine available space — return -1 to signal "unknown"
	return -1
}

// parseHumanBytesFiles parses strings like "4.66GiB", "7.20GiB", "500.00MiB" into bytes.
func parseHumanBytesFiles(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" || s == "none" {
		return 0
	}

	multiplier := int64(1)
	if strings.HasSuffix(s, "TiB") {
		multiplier = 1024 * 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "TiB")
	} else if strings.HasSuffix(s, "GiB") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GiB")
	} else if strings.HasSuffix(s, "MiB") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MiB")
	} else if strings.HasSuffix(s, "KiB") {
		multiplier = 1024
		s = strings.TrimSuffix(s, "KiB")
	} else if strings.HasSuffix(s, "B") {
		s = strings.TrimSuffix(s, "B")
	}

	var val float64
	fmt.Sscanf(strings.TrimSpace(s), "%f", &val)
	return int64(val * float64(multiplier))
}

func fmtSizeFiles(b int64) string {
	if b >= 1e9 {
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	}
	if b >= 1e6 {
		return fmt.Sprintf("%.0f MB", float64(b)/1e6)
	}
	return fmt.Sprintf("%.0f KB", float64(b)/1e3)
}
