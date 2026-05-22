package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// ═══════════════════════════════════
// HTTP API Server (runs alongside Unix socket)
// ═══════════════════════════════════

const httpPort = 5000

// JSON response helper
func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func jsonOk(w http.ResponseWriter, data interface{}) {
	jsonResponse(w, 200, data)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	jsonResponse(w, status, map[string]string{"error": msg})
}

// Read and parse JSON body (max 10MB)
func readBody(r *http.Request) (map[string]interface{}, error) {
	if r.Body == nil {
		return map[string]interface{}{}, nil
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return map[string]interface{}{}, nil
	}
	if len(data) >= 10*1024*1024 {
		return nil, fmt.Errorf("request body too large")
	}
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("invalid JSON: %v", err)
	}
	return body, nil
}

// Helper to get string from body map
func bodyStr(body map[string]interface{}, key string) string {
	if v, ok := body[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// Helper to get bool from body map
func bodyBool(body map[string]interface{}, key string) (bool, bool) {
	if v, ok := body[key]; ok {
		if b, ok := v.(bool); ok {
			return b, true
		}
	}
	return false, false
}

// Extract Bearer token from Authorization header
func getBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return auth[7:]
	}
	// Fallback: check cookie (needed for iframe sub-requests in /app/ proxy)
	if cookie, err := r.Cookie("nimos_token"); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	return ""
}

// Authenticate request — returns session data or nil
func authenticate(r *http.Request) *DBSession {
	token := getBearerToken(r)
	if token == "" {
		return nil
	}
	hashed := sha256Hex(token)
	session, err := dbSessionGet(hashed)
	if err != nil {
		return nil
	}
	return session
}

// Require authentication middleware helper — returns session or sends 401
func requireAuth(w http.ResponseWriter, r *http.Request) *DBSession {
	session := authenticate(r)
	if session == nil {
		jsonError(w, 401, "Not authenticated")
		return nil
	}
	return session
}

// Require admin role — returns session or sends error
func requireAdmin(w http.ResponseWriter, r *http.Request) *DBSession {
	session := requireAuth(w, r)
	if session == nil {
		return nil
	}
	if session.Role != "admin" {
		jsonError(w, 403, "Unauthorized")
		return nil
	}
	return session
}

// Require app access — checks if user has permission to use a specific app
// Admin always passes. Non-admin users need explicit grant in user_app_access.
func requireAppAccess(w http.ResponseWriter, r *http.Request, appId string) *DBSession {
	session := requireAuth(w, r)
	if session == nil {
		return nil
	}
	if !dbUserHasAppAccess(session.Username, session.Role, appId) {
		jsonError(w, 403, "No tienes acceso a esta aplicación")
		return nil
	}
	return session
}

// Get client IP — only trusts proxy headers from localhost (nginx)
func clientIP(r *http.Request) string {
	// RemoteAddr is host:port
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		addr = addr[:idx]
	}
	// SECURITY: Only trust X-Forwarded-For/X-Real-IP if request comes from
	// local proxy (nginx on 127.0.0.1). External clients can spoof these headers.
	if addr == "127.0.0.1" || addr == "::1" || addr == "@" {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[0])
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return xri
		}
	}
	return addr
}

// CORS middleware + security headers
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ── NimShield: check blocks, honeypots, payloads ──
		if shieldMiddleware(w, r) {
			return // request handled (blocked)
		}

		// Block TRACE method (prevents XST attacks)
		if r.Method == "TRACE" {
			w.WriteHeader(405)
			return
		}

		// Block path traversal at the middleware level BEFORE mux normalizes
		// Go's ServeMux normalizes /app/../ to / silently — we catch it here
		rawPath := r.URL.RawPath
		if rawPath == "" {
			rawPath = r.URL.Path
		}
		requestURI := r.RequestURI
		if strings.Contains(rawPath, "..") || strings.Contains(requestURI, "..") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"Invalid path"}`))
			return
		}

		// Security headers
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; style-src-elem 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data: blob: https://raw.githubusercontent.com; connect-src 'self' https://raw.githubusercontent.com; frame-src 'self' http://127.0.0.1:* http://localhost:*")
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")

		// CORS — only reflect same-host or local origins
		origin := r.Header.Get("Origin")
		if origin != "" {
			// Allow origins from local network OR the same host the request came to
			if isLocalOrigin(origin) || isSameHostOrigin(origin, r) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Vary", "Origin")
			}
		}
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLocalOrigin checks if the origin is localhost, LAN IP, or the NAS's own .local domain
// Uses proper URL parsing to prevent bypass via substrings (e.g. localhost.evil.com)
func isLocalOrigin(origin string) bool {
	origin = strings.ToLower(origin)

	// Parse the origin to extract the hostname properly
	// Origins are like "http://hostname:port"
	host := origin
	// Strip scheme
	if idx := strings.Index(host, "://"); idx != -1 {
		host = host[idx+3:]
	}
	// Strip port
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		portPart := host[idx+1:]
		if matched, _ := regexp.MatchString(`^\d+$`, portPart); matched {
			host = host[:idx]
		}
	}
	// Strip trailing slash
	host = strings.TrimRight(host, "/")

	if host == "" {
		return false
	}

	// Exact matches for localhost
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]" {
		return true
	}

	// Only allow THIS machine's .local hostname (mDNS)
	// e.g. if hostname is "nimos", allow "nimos.local"
	if strings.HasSuffix(host, ".local") {
		sysHostname, err := os.Hostname()
		if err == nil {
			sysHostname = strings.ToLower(strings.Split(sysHostname, ".")[0])
			if host == sysHostname+".local" {
				return true
			}
		}
		return false
	}

	// LAN IPs — validate they are actual IPs, not subdomains
	if matched, _ := regexp.MatchString(`^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$`, host); matched {
		// Check private ranges
		if strings.HasPrefix(host, "192.168.") ||
			strings.HasPrefix(host, "10.") ||
			strings.HasPrefix(host, "172.16.") || strings.HasPrefix(host, "172.17.") ||
			strings.HasPrefix(host, "172.18.") || strings.HasPrefix(host, "172.19.") ||
			strings.HasPrefix(host, "172.20.") || strings.HasPrefix(host, "172.21.") ||
			strings.HasPrefix(host, "172.22.") || strings.HasPrefix(host, "172.23.") ||
			strings.HasPrefix(host, "172.24.") || strings.HasPrefix(host, "172.25.") ||
			strings.HasPrefix(host, "172.26.") || strings.HasPrefix(host, "172.27.") ||
			strings.HasPrefix(host, "172.28.") || strings.HasPrefix(host, "172.29.") ||
			strings.HasPrefix(host, "172.30.") || strings.HasPrefix(host, "172.31.") {
			return true
		}
	}

	return false
}

// isSameHostOrigin checks if the origin matches the Host header of the request.
// This allows CORS for DDNS domains (e.g. nimosbarraca.duckdns.org:5009)
// without hardcoding them — if the browser sent the request to that host,
// the origin from that same host is legitimate.
func isSameHostOrigin(origin string, r *http.Request) bool {
	if origin == "" {
		return false
	}

	// Extract hostname from origin
	originHost := strings.ToLower(origin)
	if idx := strings.Index(originHost, "://"); idx != -1 {
		originHost = originHost[idx+3:]
	}
	originHost = strings.TrimRight(originHost, "/")

	// Get the Host header (this is what the browser connected to)
	requestHost := strings.ToLower(r.Host)

	// Direct match: origin host == request host (including port)
	if originHost == requestHost {
		return true
	}

	// Match without port: origin hostname == request hostname
	originHostNoPort := originHost
	if idx := strings.LastIndex(originHostNoPort, ":"); idx != -1 {
		originHostNoPort = originHostNoPort[:idx]
	}
	requestHostNoPort := requestHost
	if idx := strings.LastIndex(requestHostNoPort, ":"); idx != -1 {
		requestHostNoPort = requestHostNoPort[:idx]
	}
	if originHostNoPort == requestHostNoPort {
		return true
	}

	return false
}

// Start HTTP API server (non-blocking)
func startHTTPServer() {
	mux := http.NewServeMux()

	// ── Auth routes ──
	mux.HandleFunc("/api/auth/", handleAuthRoutes)
	mux.HandleFunc("/api/user/", handleUserRoutes)
	mux.HandleFunc("/api/users", handleUsersRoutes)
	mux.HandleFunc("/api/users/", handleUsersRoutes)

	// ── Shares routes ──
	mux.HandleFunc("/api/shares", handleSharesRoutes)
	mux.HandleFunc("/api/shares/", handleSharesRoutes)

	// ── Native Apps routes ──
	mux.HandleFunc("/api/native-apps", handleNativeAppsRoutes)
	mux.HandleFunc("/api/native-apps/", handleNativeAppsRoutes)

	// ── Installed Apps routes ──
	mux.HandleFunc("/api/installed-apps", handleInstalledAppsRoutes)
	mux.HandleFunc("/api/installed-apps/", handleInstalledAppsRoutes)

	// ── Hardware / System monitoring routes ──
	mux.HandleFunc("/api/system", handleHardwareRoutes)
	mux.HandleFunc("/api/system/", handleHardwareRoutes)
	mux.HandleFunc("/api/cpu", handleHardwareRoutes)
	mux.HandleFunc("/api/memory", handleHardwareRoutes)
	mux.HandleFunc("/api/gpu", handleHardwareRoutes)
	mux.HandleFunc("/api/temps", handleHardwareRoutes)
	mux.HandleFunc("/api/network", handleHardwareRoutes)
	mux.HandleFunc("/api/disks", handleHardwareRoutes)
	mux.HandleFunc("/api/disks/smart", handleHardwareRoutes)
	mux.HandleFunc("/api/disks/smart/summary", handleHardwareRoutes)
	mux.HandleFunc("/api/uptime", handleHardwareRoutes)
	mux.HandleFunc("/api/containers", handleHardwareRoutes)
	mux.HandleFunc("/api/containers/", handleContainerAction)
	mux.HandleFunc("/api/hostname", handleHardwareRoutes)
	mux.HandleFunc("/api/hardware/", handleHardwareRoutes)

	// ── System power + update + terminal ──
	mux.HandleFunc("/api/system/reboot-service", handleHardwareRoutes)
	mux.HandleFunc("/api/system/reboot", handleHardwareRoutes)
	mux.HandleFunc("/api/system/shutdown", handleHardwareRoutes)
	mux.HandleFunc("/api/system/update/", handleHardwareRoutes)
	mux.HandleFunc("/api/terminal", handleHardwareRoutes)

	// ── Files routes ──
	mux.HandleFunc("/api/files", handleFilesRoutes)
	mux.HandleFunc("/api/files/", handleFilesRoutes)

	// ── Storage v2 routes (Beta 8 stack) ──
	// El stack legacy /api/storage (Beta 7) fue eliminado en Sesión 4.
	// Todas las rutas storage viven ahora bajo /api/storage/v2/.
	if storageHTTPHandler != nil {
		storageHTTPHandler.Register(mux)
	}

	// ── Docker routes ──
	mux.HandleFunc("/api/docker/", handleDockerRoutes)
	mux.HandleFunc("/api/docker", handleDockerRoutes)
	mux.HandleFunc("/api/permissions/", handleDockerRoutes)
	mux.HandleFunc("/api/firewall/add-rule", handleDockerRoutes)
	mux.HandleFunc("/api/firewall/remove-rule", handleDockerRoutes)
	mux.HandleFunc("/api/firewall/toggle", handleDockerRoutes)

	// ── Network + VMs routes ──
	registerNetworkRoutes(mux)

	// ── Network module v4 (Beta 8.1) ──
	// Endpoints bajo /api/v4/network/* separados del API legacy para
	// permitir migración progresiva. El módulo v4 vive en network_*.go.
	mux.HandleFunc("/api/v4/network/ports", handleNetworkPortsRoutes)
	mux.HandleFunc("/api/v4/network/ports/", handleNetworkPortsRoutes)
	mux.HandleFunc("/api/v4/network/ddns", handleNetworkDdnsRoutes)
	mux.HandleFunc("/api/v4/network/ddns/", handleNetworkDdnsRoutes)
	mux.HandleFunc("/api/v4/network/certs", handleNetworkCertsRoutes)
	mux.HandleFunc("/api/v4/network/certs/", handleNetworkCertsRoutes)
	mux.HandleFunc("/api/v4/network/capabilities", handleNetworkCapabilitiesRoutes)
	mux.HandleFunc("/api/v4/network/capabilities/refresh", handleNetworkCapabilitiesRoutes)
	mux.HandleFunc("/api/v4/network/diagnose/cert", handleNetworkDiagnoseRoutes)

	// ── App Access management (admin only) ──
	mux.HandleFunc("/api/app-access", handleAppAccessRoutes)
	mux.HandleFunc("/api/app-access/", handleAppAccessRoutes)
	mux.HandleFunc("/api/app-access/apps", handleAppAccessRoutes)
	mux.HandleFunc("/api/my-apps", handleMyAppsRoute)

	// ── Backup routes ──
	mux.HandleFunc("/api/backup", handleBackupRoutes)
	mux.HandleFunc("/api/backup/", handleBackupRoutes)

	// Notifications
	mux.HandleFunc("/api/notifications", handleNotificationRoutes)
	mux.HandleFunc("/api/notifications/", handleNotificationRoutes)

	// ── Service Registry ──
	mux.HandleFunc("/api/services", handleServiceRoutes)
	mux.HandleFunc("/api/services/", handleServiceRoutes)

	// ── Torrent proxy to NimTorrent ──
	mux.HandleFunc("/api/torrent/", handleTorrentProxy)
	mux.HandleFunc("/api/torrent", handleTorrentProxy)

	// ── NimShield security module ──
	mux.HandleFunc("/api/shield/", handleShieldRoutes)
	mux.HandleFunc("/api/shield", handleShieldRoutes)

	// ── App reverse proxy (Docker apps via /app/{id}/) ──
	mux.HandleFunc("/app/", handleAppProxy)

	// ── Static file serving (frontend) — must be last ──
	mux.HandleFunc("/", serveStatic)

	server := &http.Server{
		Addr:         fmt.Sprintf("0.0.0.0:%d", httpPort),
		Handler:      corsMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	go func() {
		logMsg("HTTP server listening on 0.0.0.0:%d", httpPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logMsg("HTTP server error: %v", err)
		}
	}()
}

// Container action handler: POST /api/containers/:id/:action
func handleContainerAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jsonError(w, 405, "Method not allowed")
		return
	}
	session := requireAdmin(w, r)
	if session == nil {
		return
	}

	// Parse /api/containers/:id/:action
	re := regexp.MustCompile(`^/api/containers/([a-zA-Z0-9_.-]+)/(start|stop|restart|pause|unpause)$`)
	matches := re.FindStringSubmatch(r.URL.Path)
	if matches == nil {
		jsonError(w, 404, "Not found")
		return
	}

	result := containerAction(matches[1], matches[2])
	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		jsonError(w, 400, errMsg)
		return
	}
	jsonOk(w, result)
}

// ═══════════════════════════════════
// App Access Routes (admin manages user app permissions)
// ═══════════════════════════════════

func handleAppAccessRoutes(w http.ResponseWriter, r *http.Request) {
	urlPath := r.URL.Path
	method := r.Method

	// GET /api/app-access — list all grants (admin)
	// GET /api/app-access/apps — list available apps with metadata
	// GET /api/app-access?username=X — list grants for a specific user
	if method == "GET" {
		session := requireAdmin(w, r)
		if session == nil {
			return
		}

		if urlPath == "/api/app-access/apps" {
			// Return list of registered apps from DB
			apps, err := dbListAppRegistry()
			if err != nil {
				jsonError(w, 500, err.Error())
				return
			}
			result := make([]map[string]interface{}, len(apps))
			for i, a := range apps {
				result[i] = a.ToMap()
			}
			jsonOk(w, map[string]interface{}{"apps": result})
			return
		}

		username := r.URL.Query().Get("username")
		if username != "" {
			grants, err := dbUserListAppAccess(username)
			if err != nil {
				jsonError(w, 500, err.Error())
				return
			}
			result := make([]map[string]interface{}, len(grants))
			for i, g := range grants {
				result[i] = g.ToMap()
			}
			jsonOk(w, map[string]interface{}{"grants": result})
			return
		}

		grants, err := dbAppAccessListAll()
		if err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		result := make([]map[string]interface{}, len(grants))
		for i, g := range grants {
			result[i] = g.ToMap()
		}
		jsonOk(w, map[string]interface{}{"grants": result})
		return
	}

	// POST /api/app-access — grant access { username, appId, permission }
	if method == "POST" {
		session := requireAdmin(w, r)
		if session == nil {
			return
		}
		body, _ := readBody(r)
		username := bodyStr(body, "username")
		appId := bodyStr(body, "appId")
		permission := bodyStr(body, "permission")
		if username == "" || appId == "" {
			jsonError(w, 400, "username and appId required")
			return
		}
		// Validate username format and exists in DB
		if matched, _ := regexp.MatchString(`^[a-z][a-z0-9_]{1,31}$`, username); !matched {
			jsonError(w, 400, "Invalid username format")
			return
		}
		if usersCheck, err := dbUsersListRaw(); err == nil {
			found := false
			for _, u := range usersCheck {
				if u.Username == username {
					found = true
					break
				}
			}
			if !found {
				jsonError(w, 404, "User not found")
				return
			}
		}
		// Validate appId format — alphanumeric + dashes only
		if matched, _ := regexp.MatchString(`^[a-zA-Z0-9_-]{1,64}$`, appId); !matched {
			jsonError(w, 400, "Invalid appId format")
			return
		}
		if permission == "" {
			permission = "use"
		}
		if isAdminOnlyApp(appId) {
			jsonError(w, 400, "This app cannot be delegated to non-admin users")
			return
		}
		adminUser := session.Username
		err := dbAppAccessGrant(username, appId, permission, adminUser)
		if err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		jsonOk(w, map[string]interface{}{"ok": true})
		return
	}

	// DELETE /api/app-access — revoke access { username, appId }
	if method == "DELETE" {
		session := requireAdmin(w, r)
		if session == nil {
			return
		}
		body, _ := readBody(r)
		username := bodyStr(body, "username")
		appId := bodyStr(body, "appId")
		if username == "" || appId == "" {
			jsonError(w, 400, "username and appId required")
			return
		}
		err := dbAppAccessRevoke(username, appId)
		if err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		jsonOk(w, map[string]interface{}{"ok": true})
		return
	}

	jsonError(w, 405, "Method not allowed")
}

// GET /api/my-apps — returns list of app IDs the current user can access
func handleMyAppsRoute(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	username := session.Username
	role := session.Role

	if role == "admin" {
		// Admin has access to everything
		jsonOk(w, map[string]interface{}{"apps": "all", "role": "admin"})
		return
	}

	grants, _ := dbUserListAppAccess(username)
	appIds := []string{}
	// Always include public apps from DB
	publicRows, _ := db.Query(`SELECT id FROM app_registry WHERE public = 1`)
	if publicRows != nil {
		for publicRows.Next() {
			var id string
			publicRows.Scan(&id)
			appIds = append(appIds, id)
		}
		publicRows.Close()
	}
	for _, g := range grants {
		appIds = append(appIds, g.AppId)
	}
	grantsMap := make([]map[string]interface{}, len(grants))
	for i, g := range grants {
		grantsMap[i] = g.ToMap()
	}
	jsonOk(w, map[string]interface{}{"apps": appIds, "role": role, "grants": grantsMap})
}
