package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ═══════════════════════════════════
// Docker config (JSON file)
// ═══════════════════════════════════

const dockerConfigFile = "/var/lib/nimos/config/docker.json"

func getDockerConfigGo() map[string]interface{} {
	data, err := os.ReadFile(dockerConfigFile)
	if err != nil {
		return map[string]interface{}{"installed": false, "path": nil, "permissions": []interface{}{}, "appPermissions": map[string]interface{}{}, "installedAt": nil, "containers": []interface{}{}}
	}
	var conf map[string]interface{}
	if json.Unmarshal(data, &conf) != nil {
		return map[string]interface{}{"installed": false, "path": nil, "permissions": []interface{}{}, "appPermissions": map[string]interface{}{}}
	}
	if conf["appPermissions"] == nil {
		conf["appPermissions"] = map[string]interface{}{}
	}
	if conf["permissions"] == nil {
		conf["permissions"] = []interface{}{}
	}
	return conf
}

func saveDockerConfigGo(config map[string]interface{}) {
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(dockerConfigFile, data, 0644)
}

// getDockerPath returns the docker data path on the pool.
// Priority: docker.json path → primary pool → first pool → error
// NEVER returns /var/lib/docker — data must live on a pool.
func getDockerPath() (string, error) {
	// 1. Try docker.json config
	conf := getDockerConfigGo()
	if p, _ := conf["path"].(string); p != "" && strings.HasPrefix(p, "/nimos/pools/") {
		return p, nil
	}

	// 2. Try to find from storage pools (Beta 8.1: usa service v2)
	if storageService == nil {
		return "", fmt.Errorf("storage service not initialized")
	}
	pools, err := storageService.ListPools(context.Background())
	if err != nil {
		return "", fmt.Errorf("listing pools: %w", err)
	}
	if len(pools) == 0 {
		return "", fmt.Errorf("no storage pools available")
	}

	// Find primary pool first
	for _, p := range pools {
		if p.IsPrimary && p.MountPoint != "" {
			dockerPath := filepath.Join(p.MountPoint, "docker")
			// Save to docker.json for next time
			conf["path"] = dockerPath
			saveDockerConfigGo(conf)
			return dockerPath, nil
		}
	}

	// Use first pool with mount point
	for _, p := range pools {
		if p.MountPoint != "" {
			dockerPath := filepath.Join(p.MountPoint, "docker")
			conf["path"] = dockerPath
			saveDockerConfigGo(conf)
			return dockerPath, nil
		}
	}

	return "", fmt.Errorf("no pool with valid mount point found")
}

func isDockerInstalledGo() bool {
	_, ok := runSafe("docker", "--version")
	return ok
}

// dockerVarLibHasData · APP-063 protección
//
// Devuelve true si /var/lib/docker existe y contiene al menos una entrada
// (archivo o subdirectorio). No existir o estar vacío devuelve false.
//
// Usado por dockerInstall para detectar Docker pre-existente (instalado fuera
// de NimOS) con data que NimOS NO debe borrar sin permiso del usuario.
//
// Contexto: dockerInstall hace `rm -rf /var/lib/docker` para asegurar que el
// nuevo Docker daemon arranca limpio con data-root apuntando al pool. Si había
// data previa de otro Docker que el usuario instaló manualmente, el rm la
// borraría silenciosamente. Este helper habilita el bloqueo defensivo.
func dockerVarLibHasData() (bool, error) {
	const path = "/var/lib/docker"
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s exists but is not a directory", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, fmt.Errorf("readdir %s: %w", path, err)
	}
	return len(entries) > 0, nil
}

func getRealContainersGo() []map[string]interface{} {
	out, ok := runSafe("docker", "ps", "-a", "--format", `{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Ports}}|{{.State}}`)
	if !ok || out == "" {
		return []map[string]interface{}{}
	}
	var containers []map[string]interface{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 6)
		if len(parts) < 6 {
			continue
		}
		ports := "—"
		if parts[4] != "" {
			ports = parts[4]
		}
		containers = append(containers, map[string]interface{}{
			"id": parts[0], "name": parts[1], "image": parts[2],
			"status": parts[3], "ports": ports, "state": parts[5],
		})
	}
	if containers == nil {
		return []map[string]interface{}{}
	}
	return containers
}

func sanitizeDockerNameGo(name string) string {
	if name == "" {
		return ""
	}
	re := regexp.MustCompile(`[^a-zA-Z0-9_.\-/:]+`)
	sanitized := re.ReplaceAllString(name, "")
	if sanitized == "" || len(sanitized) > 256 || strings.Contains(sanitized, "..") {
		return ""
	}
	return sanitized
}

func hasDockerPermission(session *DBSession) bool {
	if session.Role == "admin" {
		return true
	}
	username := session.Username
	conf := getDockerConfigGo()
	perms, _ := conf["permissions"].([]interface{})
	for _, p := range perms {
		if ps, _ := p.(string); ps == username {
			return true
		}
	}
	return false
}

// ═══════════════════════════════════
// Known app metadata
// ═══════════════════════════════════

var knownDockerApps = map[string][3]string{
	"jellyfin":       {"Jellyfin", "🎞️", "#00A4DC"},
	"plex":           {"Plex", "🎬", "#E5A00D"},
	"nextcloud":      {"Nextcloud", "☁️", "#0082C9"},
	"immich":         {"Immich", "📸", "#4250AF"},
	"syncthing":      {"Syncthing", "🔄", "#0891B2"},
	"transmission":   {"Transmission", "⬇️", "#B50D0D"},
	"qbittorrent":    {"qBittorrent", "📥", "#2F67BA"},
	"homeassistant":  {"Home Assistant", "🏠", "#18BCF2"},
	"home-assistant": {"Home Assistant", "🏠", "#18BCF2"},
	"vaultwarden":    {"Vaultwarden", "🔐", "#175DDC"},
	"portainer":      {"Portainer", "📊", "#13BEF9"},
	"gitea":          {"Gitea", "🦊", "#609926"},
	"pihole":         {"Pi-hole", "🛡️", "#96060C"},
	"adguard":        {"AdGuard Home", "🛡️", "#68BC71"},
	"nginx":          {"Nginx", "🌐", "#009639"},
	"mariadb":        {"MariaDB", "🗄️", "#003545"},
	"postgres":       {"PostgreSQL", "🐘", "#336791"},
	"redis":          {"Redis", "🔴", "#DC382D"},
	"grafana":        {"Grafana", "📈", "#F46800"},
	"sonarr":         {"Sonarr", "📺", "#35C5F4"},
	"radarr":         {"Radarr", "🎥", "#FFC230"},
}

func getAppMeta(image, containerName string) (string, string, string) {
	lower := strings.ToLower(containerName)
	for key, meta := range knownDockerApps {
		if strings.Contains(lower, key) {
			return meta[0], meta[1], meta[2]
		}
	}
	lower = strings.ToLower(image)
	for key, meta := range knownDockerApps {
		if strings.Contains(lower, key) {
			return meta[0], meta[1], meta[2]
		}
	}
	return containerName, "📦", "#78706A"
}

// ═══════════════════════════════════
// Docker HTTP handlers
// ═══════════════════════════════════

func handleDockerRoutes(w http.ResponseWriter, r *http.Request) {
	urlPath := r.URL.Path
	method := r.Method

	switch {
	case urlPath == "/api/docker/status" && method == "GET":
		dockerStatus(w, r)
	case urlPath == "/api/docker/permissions" && method == "GET":
		dockerPermissionsGet(w, r)
	case urlPath == "/api/docker/permissions" && method == "PUT":
		dockerPermissionsSet(w, r)
	case urlPath == "/api/docker/app-permissions" && method == "GET":
		dockerAppPermissions(w, r)
	case urlPath == "/api/docker/containers" && method == "GET":
		dockerContainersList(w, r)
	case urlPath == "/api/docker/installed-apps" && method == "GET":
		dockerInstalledApps(w, r)
	case urlPath == "/api/docker/install" && method == "POST":
		dockerInstall(w, r)
	case urlPath == "/api/docker/uninstall" && method == "POST":
		dockerUninstall(w, r)
	case urlPath == "/api/docker/uninstall" && method == "DELETE":
		dockerUninstallConfig(w, r)
	case urlPath == "/api/docker/container" && method == "POST":
		dockerContainerCreate(w, r)
	case urlPath == "/api/docker/stack" && method == "POST":
		dockerStackDeploy(w, r)
	case urlPath == "/api/permissions/matrix" && method == "GET":
		permissionsMatrix(w, r)
	case urlPath == "/api/firewall/add-rule" && method == "POST":
		firewallAddRule(w, r)
	case urlPath == "/api/firewall/remove-rule" && method == "POST":
		firewallRemoveRule(w, r)
	case urlPath == "/api/firewall/toggle" && method == "POST":
		firewallToggle(w, r)
	case urlPath == "/api/hardware/install-driver" && method == "POST":
		hardwareInstallDriver(w, r)
	case strings.HasPrefix(urlPath, "/api/hardware/driver-log/") && method == "GET":
		hardwareDriverLog(w, r)
	default:
		// Regex routes
		if handleDockerRegexRoutes(w, r) {
			return
		}
		jsonError(w, 404, "Not found")
	}
}

func handleDockerRegexRoutes(w http.ResponseWriter, r *http.Request) bool {
	urlPath := r.URL.Path
	method := r.Method

	// PUT /api/docker/app-permissions/:appId
	reAppPerm := regexp.MustCompile(`^/api/docker/app-permissions/([a-zA-Z0-9_-]+)$`)
	if m := reAppPerm.FindStringSubmatch(urlPath); m != nil && method == "PUT" {
		dockerAppPermUpdate(w, r, m[1])
		return true
	}

	// GET /api/docker/app-access/:appId
	reAppAccess := regexp.MustCompile(`^/api/docker/app-access/([a-zA-Z0-9_-]+)$`)
	if m := reAppAccess.FindStringSubmatch(urlPath); m != nil && method == "GET" {
		dockerAppAccess(w, r, m[1])
		return true
	}

	// GET /api/docker/app-folders/:appId
	reAppFolders := regexp.MustCompile(`^/api/docker/app-folders/([a-zA-Z0-9_-]+)$`)
	if m := reAppFolders.FindStringSubmatch(urlPath); m != nil && method == "GET" {
		dockerAppFolders(w, r, m[1])
		return true
	}

	// POST /api/docker/container/:id/:action
	reAction := regexp.MustCompile(`^/api/docker/container/([a-zA-Z0-9_.-]+)/(start|stop|restart)$`)
	if m := reAction.FindStringSubmatch(urlPath); m != nil && method == "POST" {
		dockerContainerAction(w, r, m[1], m[2])
		return true
	}

	// DELETE /api/docker/container/:id
	reDelete := regexp.MustCompile(`^/api/docker/container/([a-zA-Z0-9_.-]+)$`)
	if m := reDelete.FindStringSubmatch(urlPath); m != nil && method == "DELETE" {
		dockerContainerDelete(w, r, m[1])
		return true
	}

	// GET /api/docker/container/:id/mounts
	reMounts := regexp.MustCompile(`^/api/docker/container/([a-zA-Z0-9_-]+)/mounts$`)
	if m := reMounts.FindStringSubmatch(urlPath); m != nil && method == "GET" {
		dockerContainerMounts(w, r, m[1])
		return true
	}

	// POST /api/docker/container/:id/rebuild
	reRebuild := regexp.MustCompile(`^/api/docker/container/([a-zA-Z0-9_-]+)/rebuild$`)
	if m := reRebuild.FindStringSubmatch(urlPath); m != nil && method == "POST" {
		dockerContainerRebuild(w, r, m[1])
		return true
	}

	// DELETE /api/docker/stack/:id
	reStack := regexp.MustCompile(`^/api/docker/stack/([a-zA-Z0-9_-]+)$`)
	if m := reStack.FindStringSubmatch(urlPath); m != nil && method == "DELETE" {
		dockerStackDelete(w, r, m[1])
		return true
	}

	// GET /api/docker/pull/:image
	if strings.HasPrefix(urlPath, "/api/docker/pull/") && method == "GET" {
		dockerPull(w, r)
		return true
	}

	return false
}

// ═══════════════════════════════════
// Handlers
// ═══════════════════════════════════

func dockerStatus(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	// Allow admin or users with Docker permission
	role := session.Role
	hasPerm := hasDockerPermission(session)
	if role != "admin" && !hasPerm {
		jsonError(w, 403, "No permission")
		return
	}
	conf := getDockerConfigGo()
	dockerRunning := isDockerInstalledGo()

	if dockerRunning && conf["installed"] != true {
		conf["installed"] = true
		conf["installedAt"] = time.Now().UTC().Format(time.RFC3339)
		saveDockerConfigGo(conf)
	}

	var containers []map[string]interface{}
	if dockerRunning && hasPerm {
		containers = getRealContainersGo()
	} else {
		containers = []map[string]interface{}{}
	}

	jsonOk(w, map[string]interface{}{
		"installed":     conf["installed"],
		"path":          conf["path"],
		"hasPermission": hasPerm,
		"installedAt":   conf["installedAt"],
		"containers":    containers,
		"dockerRunning": dockerRunning,
	})
}

func dockerPermissionsGet(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	conf := getDockerConfigGo()
	usersRaw, _ := dbUsersListRaw()
	perms, _ := conf["permissions"].([]interface{})

	var userList []map[string]interface{}
	for _, u := range usersRaw {
		hasAccess := u.Role == "admin"
		if !hasAccess {
			for _, p := range perms {
				if ps, _ := p.(string); ps == u.Username {
					hasAccess = true
					break
				}
			}
		}
		userList = append(userList, map[string]interface{}{
			"username": u.Username, "role": u.Role, "hasAccess": hasAccess,
		})
	}
	jsonOk(w, map[string]interface{}{"users": userList, "permissions": perms})
}

func dockerPermissionsSet(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	body, _ := readBody(r)
	permsRaw, ok := body["permissions"].([]interface{})
	if !ok {
		jsonError(w, 400, "Invalid permissions format")
		return
	}
	conf := getDockerConfigGo()
	conf["permissions"] = permsRaw
	saveDockerConfigGo(conf)
	jsonOk(w, map[string]interface{}{"ok": true, "permissions": permsRaw})
}

func dockerAppPermissions(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	conf := getDockerConfigGo()
	usersRaw2, _ := dbUsersListRaw()
	sharesRaw, _ := dbSharesListRaw()

	var installedApps []map[string]interface{}
	containers := getRealContainersGo()
	for _, c := range containers {
		installedApps = append(installedApps, map[string]interface{}{"id": c["name"], "name": c["name"], "type": "container", "image": c["image"]})
	}

	// Check stacks
	dockerPath, _ := conf["path"].(string)
	if dockerPath == "" {
		if dp, err := getDockerPath(); err == nil {
			dockerPath = dp
		}
	}
	stacksPath := filepath.Join(dockerPath, "stacks")
	if entries, err := os.ReadDir(stacksPath); err == nil {
		for _, e := range entries {
			if _, err := os.Stat(filepath.Join(stacksPath, e.Name(), "docker-compose.yml")); err == nil {
				installedApps = append(installedApps, map[string]interface{}{"id": e.Name(), "name": e.Name(), "type": "stack"})
			}
		}
	}

	var userList []map[string]interface{}
	for _, u := range usersRaw2 {
		userList = append(userList, map[string]interface{}{"username": u.Username, "role": u.Role})
	}

	var shareList []map[string]interface{}
	for _, s := range sharesRaw {
		shareList = append(shareList, map[string]interface{}{"name": s.Name, "displayName": s.DisplayName, "permissions": s.Permissions})
	}

	jsonOk(w, map[string]interface{}{
		"users":             userList,
		"apps":              installedApps,
		"shares":            shareList,
		"appPermissions":    conf["appPermissions"],
		"dockerPermissions": conf["permissions"],
	})
}

func dockerAppPermUpdate(w http.ResponseWriter, r *http.Request, appId string) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	body, _ := readBody(r)
	allowedUsers, ok := body["users"].([]interface{})
	if !ok {
		jsonError(w, 400, "Invalid format")
		return
	}
	conf := getDockerConfigGo()
	appPerms, _ := conf["appPermissions"].(map[string]interface{})
	if appPerms == nil {
		appPerms = map[string]interface{}{}
	}
	appPerms[appId] = allowedUsers
	conf["appPermissions"] = appPerms
	saveDockerConfigGo(conf)
	jsonOk(w, map[string]interface{}{"ok": true, "appId": appId, "users": allowedUsers})
}

func dockerAppAccess(w http.ResponseWriter, r *http.Request, appId string) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	if session.Role == "admin" {
		jsonOk(w, map[string]interface{}{"hasAccess": true, "appId": appId})
		return
	}
	conf := getDockerConfigGo()
	appPerms, _ := conf["appPermissions"].(map[string]interface{})
	users, _ := appPerms[appId].([]interface{})
	username := session.Username
	hasAccess := false
	for _, u := range users {
		if us, _ := u.(string); us == username {
			hasAccess = true
			break
		}
	}
	jsonOk(w, map[string]interface{}{"hasAccess": hasAccess, "appId": appId})
}

func dockerAppFolders(w http.ResponseWriter, r *http.Request, appId string) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	sharesRaw, _ := dbSharesListRaw()
	var folders []map[string]interface{}
	for _, s := range sharesRaw {
		for _, ap := range s.AppPermissions {
			if ap.AppId == appId {
				folders = append(folders, map[string]interface{}{"name": s.Name, "displayName": s.DisplayName, "path": s.Path})
				break
			}
		}
	}
	if folders == nil {
		folders = []map[string]interface{}{}
	}
	jsonOk(w, map[string]interface{}{"appId": appId, "folders": folders})
}

func dockerContainersList(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	if !isDockerInstalledGo() {
		jsonOk(w, map[string]interface{}{"installed": false, "containers": []interface{}{}})
		return
	}
	if !hasDockerPermission(session) {
		jsonError(w, 403, "No permission to manage Docker")
		return
	}
	jsonOk(w, map[string]interface{}{"installed": true, "containers": getRealContainersGo()})
}

// dockerInstalledApps · GET /api/docker/installed-apps
//
// APP-010 · DEPRECATED desde Beta 8.1.x.
//
// Este endpoint queda mantenido para compat con clientes pre-Beta-8 que
// consumían el formato legacy {apps: [{id, name, port, status, isStack,
// external, category}]}. El frontend nuevo debe leer /api/services y
// filtrar por type="docker-app" o consumir /api/services?app=docker.
//
// Headers de deprecación según RFC 8594:
//   Deprecation: true
//   Sunset: ... (fecha estimada de retirada, una vez completado el port frontend)
//   Link: </api/services>; rel="successor-version"
//
// APP-017 · refactorizado para usar matchContainerForAppID (single source
// of truth con getDockerAppStatuses).
func dockerInstalledApps(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	// APP-010 · marcar deprecación en cada respuesta.
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Link", `</api/services>; rel="successor-version"`)

	if !isDockerInstalledGo() {
		jsonOk(w, map[string]interface{}{"apps": []interface{}{}})
		return
	}

	registeredApps, err := appsRepo.ListDockerApps(r.Context())
	if err != nil {
		logMsg("docker: ListDockerApps failed: %v", err)
		registeredApps = nil
	}

	// docker ps (running only · este endpoint legacy mantiene la semántica
	// histórica de "solo containers running con ports expuestos").
	out, _ := runSafe("docker", "ps", "--format", "{{.Names}}|{{.Image}}|{{.Status}}|{{.Ports}}")
	containers := map[string]dockerContainer{}
	if out != "" {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			parts := strings.SplitN(line, "|", 4)
			if len(parts) < 4 {
				continue
			}
			containers[parts[0]] = dockerContainer{
				Name:   parts[0],
				Image:  parts[1],
				Status: parts[2],
				Ports:  parts[3],
			}
		}
	}

	// Helper local · extrae el primer port host del raw "0.0.0.0:8096->8096/tcp"
	extractFirstHostPort := func(rawPorts string) interface{} {
		if rawPorts == "" {
			return nil
		}
		re := regexp.MustCompile(`0\.0\.0\.0:(\d+)`)
		if m := re.FindStringSubmatch(rawPorts); m != nil {
			return parseIntDefault(m[1], 0)
		}
		return nil
	}

	var apps []interface{}
	matchedContainers := map[string]bool{}

	for _, reg := range registeredApps {
		// APP-017 · matching delegado al helper compartido.
		containerName, container := matchContainerForAppID(reg.ID, containers)

		containerStatus := "unknown"
		if container != nil {
			matchedContainers[containerName] = true
			if strings.Contains(container.Status, "Up") {
				containerStatus = "running"
			} else {
				containerStatus = "stopped"
			}
		}

		apps = append(apps, map[string]interface{}{
			"id": reg.ID, "name": reg.Name, "icon": reg.Icon,
			"color": reg.Color, "port": reg.Port, "image": reg.Image,
			"status": containerStatus, "category": "installed",
			"isStack":  reg.Type == "stack",
			"external": reg.OpenMode == "external",
		})
	}

	// Containers running no registrados + con port expuesto · "orphans con UI".
	// APP-017 · usa isPossibleStackSubContainer para filtrar subcontainers.
	for name, c := range containers {
		if matchedContainers[name] {
			continue
		}
		port := extractFirstHostPort(c.Ports)
		if port == nil {
			continue
		}
		if isPossibleStackSubContainer(name, matchedContainers) {
			continue
		}

		dispName, icon, color := getAppMeta(c.Image, name)
		apps = append(apps, map[string]interface{}{
			"id": name, "name": dispName, "icon": icon, "color": color,
			"port": port, "image": c.Image, "status": "running", "category": "installed",
		})
	}

	if apps == nil {
		apps = []interface{}{}
	}
	jsonOk(w, map[string]interface{}{"apps": apps})
}

// dockerInstall · POST /api/docker/install
//
// Wrapper sync/async sobre runDockerInstallWork.
//
// Modo sync (default · sin query param):
//   - Bloquea hasta completar (~30s-5min según si Docker ya está instalado).
//   - Responde 200 OK con resultado, o jsonError con el código apropiado.
//
// Modo async (con ?async=true):
//   - Crea una operation en operationsRepo (type="docker.install").
//   - Lanza goroutine que ejecuta el trabajo y reporta progreso.
//   - Responde 202 Accepted con {operationId, pollUrl} para polling.
//   - El cliente debe GET /api/operations/{id} para ver estado.
func dockerInstall(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	body, _ := readBody(r)

	if isAsyncRequested(r) {
		// Async path · APP-014
		op, err := operationsRepo.Create(r.Context(), "docker.install", session.Username)
		if err != nil {
			jsonError(w, 500, "Failed to create operation: "+err.Error())
			return
		}
		runWorkerAsync(op.ID, func(ctx context.Context) (map[string]interface{}, error) {
			return runDockerInstallWork(ctx, body, op.ID)
		})
		writeAsyncAccepted(w, op)
		return
	}

	// Sync path (legacy · compat 100%)
	result, err := runDockerInstallWork(r.Context(), body, "")
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	jsonOk(w, result)
}

// runDockerInstallWork · trabajo real de instalación de Docker engine en el pool.
//
// Función pura sin acceso a HTTP. Reusada por el wrapper sync y por el
// trabajo async (runWorkerAsync). Si opID != "" reporta progreso a
// operationsRepo en pasos clave.
//
// Returns:
//   - map: payload de éxito (mismo contrato que el endpoint sync previo)
//   - error: si es *httpStatusError, el wrapper sync usa Code; si no, 500
//
// Pasos (con % de progreso reportado en modo async):
//
//	 0% · validaciones (paso 0)
//	10% · ubicar pool destino (paso 1-2)
//	20% · crear directorios + daemon.json (pasos 3-4)
//	30% · instalar Docker engine (paso 5 · ~60% del tiempo en el peor caso)
//	80% · arrancar Docker + permisos (pasos 6-8)
//	90% · crear share docker-apps (paso 9)
//	95% · guardar config + registrar (pasos 10-11)
//
// El caller ya verificó admin · este worker no re-autoriza.
func runDockerInstallWork(ctx context.Context, body map[string]interface{}, opID string) (map[string]interface{}, error) {
	updateOpProgressSafe(ctx, opID, 0, "Checking environment")

	// ── 0. APP-063 · proteger data Docker pre-existente ──
	// Si NimOS no había instalado Docker antes pero /var/lib/docker tiene
	// data, probablemente es de un Docker instalado manualmente por el user.
	// El paso 6 más abajo hace `rm -rf /var/lib/docker` para limpiar al
	// reapuntar data-root al pool · sin este check, borraría data ajena.
	prevConf := getDockerConfigGo()
	prevInstalled, _ := prevConf["installed"].(bool)
	if !prevInstalled {
		hasData, checkErr := dockerVarLibHasData()
		if checkErr != nil {
			return nil, asHTTPError(500, "Failed to check /var/lib/docker: %v", checkErr)
		}
		if hasData {
			logMsg("docker: install aborted · /var/lib/docker has pre-existing data, NimOS hadn't installed Docker previously")
			return nil, asHTTPError(409,
				"/var/lib/docker contains existing data not managed by NimOS. "+
					"To prevent accidental data loss, NimOS won't overwrite it automatically. "+
					"Either move the data elsewhere or remove the directory manually, "+
					"then retry installation.")
		}
	}

	updateOpProgressSafe(ctx, opID, 10, "Locating storage pool")

	// ── 1. Find the target pool (Beta 8.1: usa service v2) ──
	if storageService == nil {
		return nil, asHTTPError(500, "Storage service not initialized")
	}
	pools, err := storageService.ListPools(ctx)
	if err != nil {
		return nil, asHTTPError(500, "listing pools: %v", err)
	}
	if len(pools) == 0 {
		return nil, asHTTPError(400, "No storage pools available. Create a pool in Storage Manager first.")
	}

	poolName := bodyStr(body, "pool")
	var targetPool *Pool
	for _, p := range pools {
		if poolName != "" && p.Name == poolName {
			targetPool = p
			break
		}
		if p.IsPrimary {
			targetPool = p
		}
	}
	if targetPool == nil {
		targetPool = pools[0]
	}

	mountPoint := targetPool.MountPoint
	if mountPoint == "" {
		return nil, asHTTPError(400, "Pool has no mount point configured")
	}

	// ── 2. Verify pool is REALLY mounted ──
	mountSrc, _ := runSafe("findmnt", "-n", "-o", "SOURCE", mountPoint)
	rootSrc, _ := runSafe("findmnt", "-n", "-o", "SOURCE", "/")
	if strings.TrimSpace(mountSrc) == "" || strings.TrimSpace(mountSrc) == strings.TrimSpace(rootSrc) {
		return nil, asHTTPError(400, "Storage pool is not mounted. Check Storage Manager.")
	}

	dockerPath := filepath.Join(mountPoint, "docker")
	dockerDataPath := filepath.Join(dockerPath, "data")

	updateOpProgressSafe(ctx, opID, 20, "Preparing directories")

	// ── 3. Create ALL directories on the pool FIRST ──
	for _, dir := range []string{"data", "containers", "stacks", "volumes"} {
		if err := os.MkdirAll(filepath.Join(dockerPath, dir), 0755); err != nil {
			return nil, asHTTPError(500, "Failed to create directory: %v", err)
		}
	}
	log.Printf("Docker directories created at %s", dockerPath)

	// ── 4. Create daemon.json BEFORE Docker ever starts ──
	os.MkdirAll("/etc/docker", 0755)
	daemonConf := map[string]interface{}{"data-root": dockerDataPath}
	daemonData, _ := json.MarshalIndent(daemonConf, "", "  ")
	if err := os.WriteFile("/etc/docker/daemon.json", daemonData, 0644); err != nil {
		return nil, asHTTPError(500, "Failed to write daemon.json: %v", err)
	}
	log.Printf("Docker daemon.json → data-root=%s", dockerDataPath)

	// ── 5. Install Docker if not present ──
	dockerAvailable := isDockerInstalledGo()
	if !dockerAvailable {
		updateOpProgressSafe(ctx, opID, 30, "Installing Docker engine (this can take several minutes)")
		log.Println("Docker not found, installing...")
		runSafe("systemctl", "stop", "docker.socket", "docker", "containerd")

		installCtx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
		defer cancel()
		// SECURITY: Download Docker install script to file first, then execute
		// (avoids pipe-to-shell which can't be verified)
		scriptPath := "/tmp/docker-install.sh"
		if _, ok := runSafe("curl", "-fsSL", "https://get.docker.com", "-o", scriptPath); !ok {
			return nil, asHTTPError(500, "Failed to download Docker install script")
		}
		// Verify script was downloaded and is non-empty
		if info, err := os.Stat(scriptPath); err != nil || info.Size() < 1000 {
			os.Remove(scriptPath)
			return nil, asHTTPError(500, "Docker install script is invalid or empty")
		}
		defer os.Remove(scriptPath)
		cmd := exec.CommandContext(installCtx, "bash", scriptPath)
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		installOut, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("Docker install failed: %v\nOutput: %s", err, string(installOut))
			return nil, asHTTPError(500, "Docker installation failed. Check system logs.")
		}
		log.Println("Docker engine installed")
		runSafe("usermod", "-aG", "docker", "nimos")
		runSafe("usermod", "-aG", "docker", "nimos")
		dockerAvailable = true
		// Stop whatever the installer started — we restart with our config
		runSafe("systemctl", "stop", "docker.socket", "docker", "containerd")
	} else {
		// Docker exists — stop to reconfigure
		runSafe("systemctl", "stop", "docker.socket", "docker", "containerd")
	}

	updateOpProgressSafe(ctx, opID, 80, "Starting Docker service")

	// ── 6. Kill /var/lib/docker — pool only ──
	// Seguro: si NimOS no había instalado Docker antes, el check APP-063 en
	// paso 0 ya verificó que /var/lib/docker está vacío o no existe. Si NimOS
	// SÍ había instalado Docker antes, data-root ya estaba en el pool desde
	// el primer install · /var/lib/docker no debería tener data nuestra.
	runSafe("rm", "-rf", "/var/lib/docker")

	// ── 7. Start Docker with correct config ──
	if dockerAvailable {
		runSafe("systemctl", "enable", "docker.service", "docker.socket")
		runSafe("systemctl", "start", "docker")
		time.Sleep(2 * time.Second)

		// Verify
		rootDir, _ := runSafe("docker", "info", "--format", "{{.DockerRootDir}}")
		rootDir = strings.TrimSpace(rootDir)
		if rootDir != "" && rootDir != dockerDataPath {
			log.Printf("WARNING: Docker Root Dir=%s expected=%s", rootDir, dockerDataPath)
		} else {
			log.Printf("Docker Root Dir confirmed: %s", dockerDataPath)
		}

		// ── 8. Permissions for FileManager ──
		runSafe("chmod", "755", dockerPath)
		runSafe("chmod", "755", filepath.Join(dockerPath, "containers"))
		runSafe("chmod", "755", filepath.Join(dockerPath, "stacks"))
		runSafe("chmod", "755", filepath.Join(dockerPath, "volumes"))

		updateOpProgressSafe(ctx, opID, 90, "Creating docker-apps share")

		// ── 9. Create docker-apps share ──
		dockerSharePath := filepath.Join(dockerPath, "containers")
		existingShare, _ := dbSharesGetRaw("docker-apps")
		if existingShare == nil {
			pName := ""
			if targetPool != nil {
				pName = targetPool.Name
			}
			shareGroup := "nimos-share-docker-apps"
			runSafe("groupadd", "-f", shareGroup)
			runSafe("chown", "root:"+shareGroup, dockerSharePath)
			runSafe("chmod", "2775", dockerSharePath)
			runSafe("setfacl", "-d", "-m", "g:"+shareGroup+":rwx", dockerSharePath)
			runSafe("usermod", "-aG", shareGroup, "nimos")
			runSafe("usermod", "-aG", shareGroup, "nimos")
			dbSharesCreate("docker-apps", "Docker Apps", "Application data for Docker containers", dockerSharePath, pName, pName, "system")
			if usersForDocker, err := dbUsersListRaw(); err == nil {
				for _, u := range usersForDocker {
					if u.Role == "admin" && u.Username != "" {
						dbShareSetPermission("docker-apps", u.Username, "rw")
						runSafe("usermod", "-aG", "docker", u.Username)
						runSafe("usermod", "-aG", shareGroup, u.Username)
					}
				}
			}
			log.Println("Docker share 'docker-apps' created at", dockerSharePath)
		}
	}

	updateOpProgressSafe(ctx, opID, 95, "Registering Docker engine")

	// ── 10. Save config ──
	conf := getDockerConfigGo()
	conf["installed"] = true
	conf["dockerAvailable"] = dockerAvailable
	conf["path"] = dockerPath
	if perms, ok := body["permissions"].([]interface{}); ok {
		conf["permissions"] = perms
	}
	conf["installedAt"] = time.Now().UTC().Format(time.RFC3339)
	saveDockerConfigGo(conf)

	// ── 11. APP-013 · registro vía único punto canónico ──
	// Antes de Beta 8.1.1 había un dbServiceRegister hardcodeado aquí con
	// Status="running"/Health="healthy" sin verificar nada, paralelo al
	// detector. Si los IDs divergían (findPoolFromPath vs targetPool.Name),
	// quedaban dos rows.
	//
	// Flujo actual:
	//   1. saveDockerConfigGo deja installed=true en docker.json
	//   2. runAutoRegister invoca detectDockerEngine que inserta la instance
	//      con Status="unknown"
	//   3. reconcileServices verifica systemctl is-active docker y corrige
	//      el status real inmediatamente · sin esperar al tick (≤30s)
	//   4. ForceDockerCacheRefresh prepara la cache para que /api/services
	//      pueda servir Docker engine + children sin lag perceptible
	runAutoRegister(ctx)
	reconcileServices()
	ForceDockerCacheRefresh(ctx)

	return map[string]interface{}{"ok": true, "path": dockerPath, "dockerAvailable": dockerAvailable}, nil
}


func dockerUninstall(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	runShellStatic("docker stop $(docker ps -aq) 2>/dev/null || true")
	runShellStatic("docker rm $(docker ps -aq) 2>/dev/null || true")
	runSafe("systemctl", "stop", "docker")
	runSafe("systemctl", "disable", "docker")
	runShellStatic("apt-get purge -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin 2>/dev/null || true")
	runSafe("rm", "-f", "/etc/docker/daemon.json")

	// Deregister from service registry
	conf := getDockerConfigGo()
	if p, _ := conf["path"].(string); strings.HasPrefix(p, nimosPoolsDir+"/") {
		parts := strings.Split(strings.TrimPrefix(p, nimosPoolsDir+"/"), "/")
		if len(parts) > 0 {
			dbServiceDelete("docker@" + parts[0])
		}
	}

	conf["installed"] = false
	conf["dockerAvailable"] = false
	conf["path"] = nil
	conf["permissions"] = []interface{}{}
	conf["installedAt"] = nil
	saveDockerConfigGo(conf)
	jsonOk(w, map[string]interface{}{"ok": true})
}

func dockerUninstallConfig(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	conf := getDockerConfigGo()
	conf["installed"] = false
	conf["path"] = nil
	conf["permissions"] = []interface{}{}
	conf["installedAt"] = nil
	saveDockerConfigGo(conf)
	jsonOk(w, map[string]interface{}{"ok": true})
}

func dockerContainerCreate(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	if !hasDockerPermission(session) {
		jsonError(w, 403, "No permission to manage Docker")
		return
	}
	if !isDockerInstalledGo() {
		jsonError(w, 400, "Docker not installed")
		return
	}

	body, _ := readBody(r)
	rawId := bodyStr(body, "id")
	rawName := bodyStr(body, "name")
	rawImage := bodyStr(body, "image")
	id := sanitizeDockerNameGo(rawId)
	name := sanitizeDockerNameGo(rawName)
	image := sanitizeDockerNameGo(rawImage)
	if id == "" || name == "" || image == "" {
		jsonError(w, 400, "Missing container info")
		return
	}
	// Reject if sanitization changed the input — means malicious chars were present
	if id != rawId || name != rawName || image != rawImage {
		jsonError(w, 400, "Container name, id, or image contains invalid characters")
		return
	}

	conf := getDockerConfigGo()
	dockerPath, _ := conf["path"].(string)
	if dockerPath == "" {
		dp, err := getDockerPath()
		if err != nil {
			jsonError(w, 400, "Docker not configured — install Docker from App Store first")
			return
		}
		dockerPath = dp
	}

	// Build docker args as a slice — no shell interpolation
	dockerArgs := []string{"run", "-d", "--name", id, "--restart", "unless-stopped"}

	// Ports — validate strictly
	if ports, ok := body["ports"].(map[string]interface{}); ok {
		portRegex := regexp.MustCompile(`^\d{1,5}$`)
		for host, container := range ports {
			containerStr := fmt.Sprintf("%v", container)
			if !portRegex.MatchString(host) || !portRegex.MatchString(containerStr) {
				jsonError(w, 400, "Invalid port mapping (must be numeric)")
				return
			}
			dockerArgs = append(dockerArgs, "-p", host+":"+containerStr)
		}
	}

	// Config volume
	containerDataPath := filepath.Join(dockerPath, "containers", id)
	os.MkdirAll(containerDataPath, 0775)
	// Set group ownership for FileManager access
	runSafe("chown", "root:nimos-share-docker-apps", containerDataPath)
	runSafe("chmod", "2775", containerDataPath)
	dockerArgs = append(dockerArgs, "-v", containerDataPath+":/config")

	// Shared folder mounts
	sharesForMount, _ := dbSharesListRaw()
	var mountedShares []string
	for _, s := range sharesForMount {
		for _, ap := range s.AppPermissions {
			if ap.AppId == id {
				if s.Path != "" {
					dockerArgs = append(dockerArgs, "-v", s.Path+":/media/"+s.Name+":ro")
					mountedShares = append(mountedShares, s.Name)
				}
				break
			}
		}
	}

	// Env vars — SECURITY: passed as separate args, no shell escaping needed
	if env, ok := body["env"].(map[string]interface{}); ok {
		for key, val := range env {
			valStr := fmt.Sprintf("%v", val)
			if matched, _ := regexp.MatchString(`^[a-zA-Z_][a-zA-Z0-9_]*$`, key); matched {
				dockerArgs = append(dockerArgs, "-e", key+"="+valStr)
			}
		}
	}

	dockerArgs = append(dockerArgs, image)

	out, ok := runSafe("docker", dockerArgs...)
	if !ok {
		jsonError(w, 500, "Failed to create container")
		return
	}

	// Register app · CreateOrUpdate es UPSERT idempotente, no necesita pre-filtrar
	//
	// APP-033 · construir array completo de PortBinding desde body.ports.
	// El primer port se duplica en Port (legacy) para clientes viejos que
	// leen ese campo. El array completo va en PortsJSON · canonical multi-port.
	var portBindings []PortBinding
	appPort := 0
	if ports, ok := body["ports"].(map[string]interface{}); ok {
		for host, container := range ports {
			hostInt := parseIntDefault(host, 0)
			containerInt := parseIntDefault(fmt.Sprintf("%v", container), 0)
			if hostInt == 0 || containerInt == 0 {
				continue
			}
			portBindings = append(portBindings, PortBinding{
				Host:     hostInt,
				Declared: containerInt,
				Protocol: "tcp", // body no trae proto · asumimos tcp (99% apps web)
			})
			if appPort == 0 {
				appPort = hostInt
			}
		}
	}
	portsJSON := "[]"
	if len(portBindings) > 0 {
		if data, jerr := json.Marshal(portBindings); jerr == nil {
			portsJSON = string(data)
		}
	}

	app := &DBDockerApp{
		ID:          id,
		Name:        name,
		Icon:        bodyStr(body, "icon"),
		Image:       image,
		Color:       bodyStr(body, "color"),
		Type:        "container",
		Port:        appPort,
		PortsJSON:   portsJSON,
		InstalledBy: session.Username,
	}
	if err := appsRepo.CreateOrUpdateDockerApp(r.Context(), app); err != nil {
		logMsg("docker: install register failed for %s: %v", id, err)
	}

	// APP-034 · invalidación inmediata de cache de NimHealth (sync, ~150ms en Pi).
	ForceDockerCacheRefresh(r.Context())

	jsonOk(w, map[string]interface{}{
		"ok": true, "containerId": strings.TrimSpace(out),
		"container":     map[string]interface{}{"id": id, "name": name, "image": image, "status": "running"},
		"mountedShares": mountedShares,
	})
}

func dockerStackDeploy(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	if !hasDockerPermission(session) {
		jsonError(w, 403, "No permission to manage Docker")
		return
	}
	if !isDockerInstalledGo() {
		jsonError(w, 400, "Docker not installed")
		return
	}

	body, _ := readBody(r)
	id := sanitizeDockerNameGo(bodyStr(body, "id"))
	compose := bodyStr(body, "compose")
	if id == "" || compose == "" {
		jsonError(w, 400, "Missing stack info")
		return
	}

	conf := getDockerConfigGo()
	dockerPath, _ := conf["path"].(string)
	if dockerPath == "" {
		dp, err := getDockerPath()
		if err != nil {
			jsonError(w, 400, "Docker not configured — install Docker from App Store first")
			return
		}
		dockerPath = dp
	}
	stackPath := filepath.Join(dockerPath, "stacks", id)
	os.MkdirAll(stackPath, 0755)

	// Create container config directory (used by CONFIG_PATH in compose)
	containerPath := filepath.Join(dockerPath, "containers", id)
	os.MkdirAll(containerPath, 0775)
	// Set permissions so admin can read/write configs
	runSafe("chmod", "-R", "775", containerPath)

	// Write compose file
	composePath := filepath.Join(stackPath, "docker-compose.yml")
	os.WriteFile(composePath, []byte(compose), 0644)

	// APP-064 · Inyección automática de variables canónicas en .env del stack.
	//
	// Los composes del catálogo NimOS Appstore usan placeholders estándar
	// que el backend conoce pero el frontend no:
	//
	//   CONFIG_PATH · ruta del directorio de configuración del container
	//                 (montado como volumen para persistencia de configs).
	//                 = {dockerPath}/containers/{stackId}
	//
	//   HOST_IP     · IP local del NAS · usada por apps que generan URLs
	//                 absolutas (e.g. Jellyfin PublishedServerUrl).
	//
	// Estas dos vars se inyectan SIEMPRE antes de escribir .env, de modo que
	// docker-compose las pueda resolver al hacer 'up'. Si el frontend manda
	// también values en body.env, esos prevalecen (override · por si una app
	// específica necesita un valor custom).
	//
	// Decisión: el backend es la fuente canónica de estos paths/IPs · evita
	// que el frontend tenga que conocer la estructura interna del filesystem
	// del pool o adivinar la IP del host.
	autoEnv := map[string]interface{}{
		"CONFIG_PATH": containerPath,
		"HOST_IP":     getStackHostIP(),
	}
	// Merge body.env encima · permitir override desde el catálogo si hace falta
	if env, ok := body["env"].(map[string]interface{}); ok {
		for k, v := range env {
			autoEnv[k] = v
		}
	}
	var lines []string
	for k, v := range autoEnv {
		lines = append(lines, fmt.Sprintf("%s=%v", k, v))
	}
	os.WriteFile(filepath.Join(stackPath, ".env"), []byte(strings.Join(lines, "\n")+"\n"), 0644)

	// Deploy
	cmd := exec.Command("docker", "compose", "-f", composePath, "up", "-d")
	cmd.Dir = stackPath
	if out, err := cmd.CombinedOutput(); err != nil {
		jsonError(w, 500, fmt.Sprintf("Failed to deploy stack: %s", string(out)))
		return
	}

	// Fix permissions on container config dir after deploy
	// Set group to nimos-share-docker-apps so FileManager can browse
	runSafe("chown", "-R", "root:nimos-share-docker-apps", containerPath)
	runSafe("chmod", "-R", "2775", containerPath)
	runSafe("setfacl", "-R", "-d", "-m", "g:nimos-share-docker-apps:rwx", containerPath)
	runSafe("chmod", "-R", "775", stackPath)

	// Register stack
	stackPort := 0
	if p, ok := body["port"].(float64); ok {
		stackPort = int(p)
	}
	openMode := "internal"
	if ext, ok := body["external"].(bool); ok && ext {
		openMode = "external"
	}
	app := &DBDockerApp{
		ID:          id,
		Name:        bodyStr(body, "name"),
		Icon:        bodyStr(body, "icon"),
		Image:       "stack",
		Color:       bodyStr(body, "color"),
		Type:        "stack",
		OpenMode:    openMode,
		Port:        stackPort,
		InstalledBy: session.Username,
	}
	if err := appsRepo.CreateOrUpdateDockerApp(r.Context(), app); err != nil {
		logMsg("docker: stack install register failed for %s: %v", id, err)
	}

	// APP-034 · invalidación inmediata de cache de NimHealth (sync, ~150ms en Pi).
	// Sin esto, la app no aparece en /api/services hasta el siguiente tick (≤30s).
	ForceDockerCacheRefresh(r.Context())

	jsonOk(w, map[string]interface{}{"ok": true, "stack": id, "path": stackPath})
}

func dockerContainerAction(w http.ResponseWriter, r *http.Request, id, action string) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	if !hasDockerPermission(session) {
		jsonError(w, 403, "No permission to manage Docker")
		return
	}
	safeId := sanitizeDockerNameGo(id)
	if safeId == "" {
		jsonError(w, 400, "Invalid container ID")
		return
	}
	if _, ok := runSafe("docker", action, safeId); !ok {
		jsonError(w, 500, fmt.Sprintf("Failed to %s container", action))
		return
	}
	// APP-034 · refresh async para no añadir latencia a la respuesta del action.
	// El user clicó start/stop/restart, la operación ya completó · la cache se
	// pondrá al día antes de la próxima vista de NimHealth.
	go ForceDockerCacheRefresh(context.Background())

	jsonOk(w, map[string]interface{}{"ok": true, "action": action, "containerId": safeId})
}

func dockerContainerDelete(w http.ResponseWriter, r *http.Request, id string) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	if !hasDockerPermission(session) {
		jsonError(w, 403, "No permission to manage Docker")
		return
	}
	safeId := sanitizeDockerNameGo(id)
	if safeId == "" {
		jsonError(w, 400, "Invalid container ID")
		return
	}

	// APP-031 · race-free uninstall:
	//   1. MarkDockerAppDeleting · síncrono. Observer ya no la lista activa,
	//      container no se cuenta como orphan durante el cleanup.
	//   2. Goroutine: docker stop/rm + DeleteDockerApp final.
	//
	// Sustituye al flujo legacy (DELETE row síncrono + container stop async)
	// que generaba flicker en orphanCount durante la ventana stop/rm.
	if err := appsRepo.MarkDockerAppDeleting(r.Context(), safeId); err != nil {
		logMsg("docker: uninstall mark-deleting failed for %s: %v", safeId, err)
		// Continuamos: el cleanup de Docker es lo importante. El observer puede
		// generar un orphan transitorio pero no es peor que el flujo legacy.
	}

	// Capturar id para la goroutine (Context del request muere al return).
	idCapture := safeId
	go func() {
		runSafe("docker", "stop", idCapture)
		if _, ok := runSafe("docker", "rm", idCapture); !ok {
			runSafe("docker", "rm", "-f", idCapture)
		}
		// DELETE final libera la row. Usamos Background ctx porque el request
		// ya terminó y la operación debe completarse independientemente.
		if err := appsRepo.DeleteDockerApp(context.Background(), idCapture); err != nil {
			logMsg("docker: uninstall final DB delete failed for %s: %v", idCapture, err)
		}
		// APP-034 · refresh cache tras cleanup completo.
		ForceDockerCacheRefresh(context.Background())
	}()

	jsonOk(w, map[string]interface{}{"ok": true, "containerId": safeId})
}

func dockerContainerMounts(w http.ResponseWriter, r *http.Request, id string) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	if !hasDockerPermission(session) {
		jsonError(w, 403, "No permission")
		return
	}
	safeId := sanitizeDockerNameGo(id)
	if safeId == "" {
		jsonError(w, 400, "Invalid container ID")
		return
	}
	out, ok := runSafe("docker", "inspect", safeId, "--format", `{{range .Mounts}}{{.Source}}|{{.Destination}}|{{.Mode}}{{println}}{{end}}`)
	if !ok {
		jsonError(w, 500, "Failed to get mounts")
		return
	}
	var mounts []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) >= 2 {
			mode := "rw"
			if len(parts) >= 3 {
				mode = parts[2]
			}
			mounts = append(mounts, map[string]interface{}{"source": parts[0], "destination": parts[1], "mode": mode})
		}
	}
	if mounts == nil {
		mounts = []map[string]interface{}{}
	}
	jsonOk(w, map[string]interface{}{"containerId": safeId, "mounts": mounts})
}

// dockerContainerRebuild · reconstruye una app instalada preservando su config.
//
// Política Beta 8.1 (post-APP-001):
//
//	type='stack'     → docker compose -f {stack}/docker-compose.yml up -d --force-recreate
//	                   El compose file ES la fuente de verdad: preserva volumes, env,
//	                   networks, ports, labels, restart policy — todo lo declarado.
//	type='container' → 501 Not Implemented. Rebuild de container suelto requiere
//	                   reconstruir flags desde `docker inspect` (ticket APP-001-B).
//
// CONTEXTO HISTÓRICO (Beta 7 y anteriores):
// La implementación previa hacía `docker stop && rm && run -d --name X image` lo cual
// PERDÍA volumes, env vars, port mappings y network attachments. Una app con datos
// (Jellyfin biblioteca, Immich DB, Vaultwarden vault) quedaba inutilizable tras un
// click en "Rebuild". Esta versión bloquea el path peligroso devolviendo 501 hasta
// que la implementación correcta esté lista.
func dockerContainerRebuild(w http.ResponseWriter, r *http.Request, id string) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	if !hasDockerPermission(session) {
		jsonError(w, 403, "No permission")
		return
	}
	safeId := sanitizeDockerNameGo(id)
	if safeId == "" {
		jsonError(w, 400, "Invalid container ID")
		return
	}

	// Lookup app type en docker_apps. Sin registro no podemos garantizar rebuild seguro.
	app, err := appsRepo.GetDockerApp(r.Context(), safeId)
	if err != nil {
		jsonError(w, 500, fmt.Sprintf("Failed to lookup app: %v", err))
		return
	}
	if app == nil {
		jsonError(w, 404, "App not found in registry · rebuild requires a registered app")
		return
	}

	switch app.Type {
	case "stack":
		dockerPath, err := getDockerPath()
		if err != nil {
			jsonError(w, 400, "Docker path not configured")
			return
		}
		stackDir := filepath.Join(dockerPath, "stacks", safeId)
		composePath := filepath.Join(stackDir, "docker-compose.yml")
		if _, err := os.Stat(composePath); err != nil {
			jsonError(w, 404, fmt.Sprintf("Compose file not found at %s", composePath))
			return
		}
		// `docker compose up -d --force-recreate` reusa el compose existente y
		// recrea containers preservando TODO lo declarado (volumes, env, ports,
		// networks, labels). Único cambio: containers nuevos con IDs nuevos.
		cmd := exec.Command("docker", "compose", "-f", composePath, "up", "-d", "--force-recreate")
		cmd.Dir = stackDir
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			logMsg("docker: stack rebuild %s failed: %v · output: %s", safeId, runErr, string(out))
			jsonError(w, 500, fmt.Sprintf("Rebuild failed: %s", string(out)))
			return
		}
		logMsg("docker: stack rebuild %s ok", safeId)
		jsonOk(w, map[string]interface{}{
			"ok":          true,
			"containerId": safeId,
			"type":        "stack",
			"method":      "compose_force_recreate",
		})
		return

	case "container", "":
		// Rebuild de container suelto (no stack) está deshabilitado hasta tener
		// implementación correcta basada en `docker inspect` + reconstrucción de
		// flags. Devolver 501 explícito previene que la UI lo invoque silenciosamente.
		//
		// Workaround para el usuario: desinstalar y reinstalar la app desde AppStore.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{` +
			`"ok":false,` +
			`"error":"container_rebuild_not_implemented",` +
			`"message":"Rebuild for standalone containers is temporarily disabled. ` +
			`The previous implementation lost volumes, environment variables, port mappings and ` +
			`network attachments. To rebuild this app, uninstall and reinstall it.",` +
			`"ticket":"APP-001-B"` +
			`}`))
		logMsg("docker: rebuild %s rejected (type=container, APP-001-B pending)", safeId)
		return

	default:
		jsonError(w, 500, fmt.Sprintf("Unknown app type %q for rebuild", app.Type))
		return
	}
}

func dockerStackDelete(w http.ResponseWriter, r *http.Request, id string) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	if !hasDockerPermission(session) {
		jsonError(w, 403, "No permission")
		return
	}
	safeId := sanitizeDockerNameGo(id)
	if safeId == "" {
		jsonError(w, 400, "Invalid stack ID")
		return
	}

	// APP-031 · race-free uninstall (mismo flujo que dockerContainerDelete).
	if err := appsRepo.MarkDockerAppDeleting(r.Context(), safeId); err != nil {
		logMsg("docker: stack uninstall mark-deleting failed for %s: %v", safeId, err)
	}

	conf := getDockerConfigGo()
	dockerPath, _ := conf["path"].(string)
	if dockerPath == "" {
		if dp, err := getDockerPath(); err == nil {
			dockerPath = dp
		} else {
			// Sin path · borramos la row directamente, no hay nada de stack que limpiar.
			if delErr := appsRepo.DeleteDockerApp(r.Context(), safeId); delErr != nil {
				logMsg("docker: stack uninstall row delete failed for %s: %v", safeId, delErr)
			}
			jsonOk(w, map[string]interface{}{"ok": true})
			return
		}
	}
	stackPath := filepath.Join(dockerPath, "stacks", safeId)
	composePath := filepath.Join(stackPath, "docker-compose.yml")

	// Cleanup en background + DELETE final tras compose down.
	idCapture := safeId
	go func() {
		if _, err := os.Stat(composePath); err == nil {
			cmd := exec.Command("docker", "compose", "-f", composePath, "down", "-v", "--remove-orphans")
			cmd.Dir = stackPath
			cmd.Run()
		}
		os.RemoveAll(stackPath)
		os.RemoveAll(filepath.Join(dockerPath, "containers", idCapture))

		// DELETE final libera la row.
		if err := appsRepo.DeleteDockerApp(context.Background(), idCapture); err != nil {
			logMsg("docker: stack uninstall final DB delete failed for %s: %v", idCapture, err)
		}
		// APP-034 · refresh cache tras cleanup completo.
		ForceDockerCacheRefresh(context.Background())
	}()

	jsonOk(w, map[string]interface{}{"ok": true})
}

// dockerPull · GET /api/docker/pull/{image}
//
// Wrapper sync/async sobre runDockerPullWork.
// APP-053 · soporta ?async=true para devolver 202 + operationId.
func dockerPull(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	if !hasDockerPermission(session) {
		jsonError(w, 403, "No permission")
		return
	}
	rawImage := strings.TrimPrefix(r.URL.Path, "/api/docker/pull/")
	decoded, _ := url.PathUnescape(rawImage)
	image := sanitizeDockerNameGo(decoded)
	if image == "" || image != decoded {
		jsonError(w, 400, "Invalid image name")
		return
	}

	if isAsyncRequested(r) {
		// Async path · APP-053
		op, err := operationsRepo.Create(r.Context(), "docker.pull", session.Username)
		if err != nil {
			jsonError(w, 500, "Failed to create operation: "+err.Error())
			return
		}
		runWorkerAsync(op.ID, func(ctx context.Context) (map[string]interface{}, error) {
			return runDockerPullWork(ctx, image, op.ID)
		})
		writeAsyncAccepted(w, op)
		return
	}

	// Sync path (legacy)
	result, err := runDockerPullWork(r.Context(), image, "")
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	jsonOk(w, result)
}

// runDockerPullWork · trabajo real de `docker pull <image>`.
//
// Función pura sin acceso a HTTP. Si opID != "" reporta progreso a
// operationsRepo. Como docker pull es una sola operación de duración variable
// (10s-2min) y no expone progreso real, solo reporta start (5%) y end (100%).
//
// Returns:
//   - map: {"ok": true, "image": image}
//   - error: *httpStatusError(500) si docker pull falla
func runDockerPullWork(ctx context.Context, image, opID string) (map[string]interface{}, error) {
	updateOpProgressSafe(ctx, opID, 5, "Pulling image "+image)

	if _, ok := runSafe("docker", "pull", image); !ok {
		return nil, asHTTPError(500, "Failed to pull image")
	}

	return map[string]interface{}{"ok": true, "image": image}, nil
}

func permissionsMatrix(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	usersRaw3, _ := dbUsersListRaw()
	sharesRaw, _ := dbSharesListRaw()
	conf := getDockerConfigGo()
	perms, _ := conf["permissions"].([]interface{})

	var userList []map[string]interface{}
	for _, u := range usersRaw3 {
		hasDock := u.Role == "admin"
		for _, p := range perms {
			if ps, _ := p.(string); ps == u.Username {
				hasDock = true
			}
		}
		userList = append(userList, map[string]interface{}{"username": u.Username, "role": u.Role, "dockerAccess": hasDock})
	}

	var shareList []map[string]interface{}
	for _, s := range sharesRaw {
		appPerms := make([]map[string]interface{}, 0, len(s.AppPermissions))
		for _, ap := range s.AppPermissions {
			appPerms = append(appPerms, map[string]interface{}{"appId": ap.AppId, "uid": ap.Uid, "permission": ap.Permission})
		}
		shareList = append(shareList, map[string]interface{}{
			"name": s.Name, "displayName": s.DisplayName,
			"userPermissions": s.Permissions, "appPermissions": appPerms,
		})
	}

	jsonOk(w, map[string]interface{}{"users": userList, "shares": shareList, "dockerAdmins": perms})
}

// ═══════════════════════════════════
// Firewall
// ═══════════════════════════════════

func firewallAddRule(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	body, _ := readBody(r)
	port := fmt.Sprintf("%v", body["port"])
	protocol := bodyStr(body, "protocol")
	action := bodyStr(body, "action")
	source := bodyStr(body, "source")

	if port == "" || protocol == "" || action == "" {
		jsonError(w, 400, "port, protocol, and action required")
		return
	}

	// Strict validation — prevent command injection
	// Port: digits only, or digits:digits for ranges
	if matched, _ := regexp.MatchString(`^\d{1,5}(:\d{1,5})?$`, port); !matched {
		jsonError(w, 400, "Invalid port format (use number or range like 8000:8100)")
		return
	}
	// Protocol: whitelist only
	if protocol != "tcp" && protocol != "udp" && protocol != "both" {
		jsonError(w, 400, "Invalid protocol (must be tcp, udp, or both)")
		return
	}
	// Action: whitelist only
	if action != "allow" && action != "deny" && action != "reject" && action != "limit" {
		jsonError(w, 400, "Invalid action (must be allow, deny, reject, or limit)")
		return
	}
	// Source: must be a valid IP or CIDR, or empty/any
	if source != "" && source != "any" && source != "Any" {
		if matched, _ := regexp.MatchString(`^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(/\d{1,2})?$`, source); !matched {
			jsonError(w, 400, "Invalid source (must be IP address or CIDR like 192.168.1.0/24)")
			return
		}
	}

	_, hasUfw := runSafe("which", "ufw")
	if hasUfw {
		// Build ufw args safely — no shell interpolation
		portProto := port
		if protocol != "both" {
			portProto = port + "/" + protocol
		}
		args := []string{action, portProto}
		if source != "" && source != "any" && source != "Any" {
			args = append(args, "from", source)
		}
		result, _ := runSafe("ufw", args...)
		jsonOk(w, map[string]interface{}{"ok": true, "command": "ufw " + strings.Join(args, " "), "result": result})
	} else {
		jsonError(w, 400, "ufw not installed")
	}
}

func firewallRemoveRule(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	body, _ := readBody(r)
	ruleNum := fmt.Sprintf("%v", body["ruleNum"])
	if ruleNum == "" {
		jsonError(w, 400, "ruleNum required")
		return
	}
	// Strict validation — ruleNum must be a positive integer
	if matched, _ := regexp.MatchString(`^\d{1,5}$`, ruleNum); !matched {
		jsonError(w, 400, "Invalid rule number (must be a positive integer)")
		return
	}
	result, _ := runSafeInput("y\n", "ufw", "delete", ruleNum)
	jsonOk(w, map[string]interface{}{"ok": true, "result": result})
}

func firewallToggle(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	body, _ := readBody(r)
	enable, _ := body["enable"].(bool)
	if enable {
		result, _ := runSafeInput("y\n", "ufw", "enable")
		jsonOk(w, map[string]interface{}{"ok": true, "result": result})
	} else {
		result, _ := runSafe("ufw", "disable")
		jsonOk(w, map[string]interface{}{"ok": true, "result": result})
	}
}

// ═══════════════════════════════════
// Hardware driver install
// ═══════════════════════════════════

func hardwareInstallDriver(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	body, _ := readBody(r)
	pkg := bodyStr(body, "package")
	action := bodyStr(body, "action")
	if pkg == "" || action == "" {
		jsonError(w, 400, "package and action required")
		return
	}
	if matched, _ := regexp.MatchString(`^(nvidia-driver-\d+|nvidia-driver-\d+-server|nvidia-driver-\d+-open|xserver-xorg-video-\w+|mesa-\w+|linux-modules-nvidia-\S+)$`, pkg); !matched {
		jsonError(w, 400, "Invalid driver package name")
		return
	}
	if action != "install" && action != "remove" {
		jsonError(w, 400, "action must be install or remove")
		return
	}

	logFile := fmt.Sprintf("/tmp/nimos-driver-%d.log", time.Now().UnixMilli())
	go func() {
		// SECURITY: exec.Command directly, no shell interpolation
		c := exec.Command("apt-get", action, "-y", pkg)
		c.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		out, _ := c.CombinedOutput()
		os.WriteFile(logFile, out, 0644)
	}()
	jsonOk(w, map[string]interface{}{"ok": true, "message": fmt.Sprintf("%s %s started", action, pkg), "logFile": logFile})
}

func hardwareDriverLog(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	logFile := "/tmp/" + filepath.Base(r.URL.Path)
	if !strings.HasPrefix(logFile, "/tmp/nimos-driver-") {
		jsonError(w, 400, "Invalid log file")
		return
	}
	content, err := os.ReadFile(logFile)
	if err != nil {
		jsonOk(w, map[string]interface{}{"content": "Waiting...", "done": false, "success": false})
		return
	}
	s := string(content)
	done := strings.Contains(s, "SUCCESS:") || strings.Contains(s, "ERROR:")
	success := strings.Contains(s, "SUCCESS:")
	jsonOk(w, map[string]interface{}{"content": s, "done": done, "success": success})
}
