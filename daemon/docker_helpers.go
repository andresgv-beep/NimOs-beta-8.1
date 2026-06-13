// docker_helpers.go — Helpers puros del módulo Docker (Beta 8.1)
//
// Funciones de soporte sin estado, llamadas por todos los handlers del
// módulo (containers, stacks, install, permissions, pull, status). Aquí
// vive además hasDockerPermission, que aunque pertenece conceptualmente
// al dominio de permisos, se usa como auth check en TODOS los handlers
// y se beneficia de estar en el archivo de helpers.
//
// Separado del resto durante el sprint post-cierre (mayo 2026) para cumplir
// DISCIPLINE §1 · "un archivo no cabe en una pantalla mental, hace demasiado".
//
// El docker.go original tenía 1833 LOC y 39 funciones mezclando 4-5 dominios.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// dockerConfigFile · path del archivo de configuración del módulo Docker.
// Almacena pool elegido, lista de containers, permissions, app_permissions.
// Definida aquí (junto a los helpers que la leen/escriben) en lugar de en
// docker.go (que ya no existe tras el sprint post-cierre).
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
	// 1. Try docker.json config — PERO solo si el pool está REALMENTE montado.
	//    Confiar en el path guardado sin verificar el montaje es lo que hacía
	//    que, tras un rename a medias o un pool desmontado, Docker escribiera
	//    en un directorio vacío sobre el disco de sistema en vez de en el pool.
	//    (Regla 16: la config no es autoridad; el estado de montaje sí.)
	conf := getDockerConfigGo()
	if p, _ := conf["path"].(string); p != "" && strings.HasPrefix(p, "/nimos/pools/") {
		poolMount := filepath.Dir(p) // /nimos/pools/data9/docker → /nimos/pools/data9
		if isPoolMounted(poolMount) {
			return p, nil
		}
		logMsg("getDockerPath: docker.json apunta a %s pero %s NO está montado; buscando un pool montado", p, poolMount)
		// No retornamos: caemos a la selección por pools montados de abajo.
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

	// Find primary pool first — debe estar REALMENTE montado.
	for _, p := range pools {
		if p.IsPrimary && p.MountPoint != "" && isPoolMounted(p.MountPoint) {
			dockerPath := filepath.Join(p.MountPoint, "docker")
			conf["path"] = dockerPath
			saveDockerConfigGo(conf)
			return dockerPath, nil
		}
	}

	// Use first pool with a mount point that is ACTUALLY mounted.
	for _, p := range pools {
		if p.MountPoint != "" && isPoolMounted(p.MountPoint) {
			dockerPath := filepath.Join(p.MountPoint, "docker")
			conf["path"] = dockerPath
			saveDockerConfigGo(conf)
			return dockerPath, nil
		}
	}

	return "", fmt.Errorf("no hay ningún pool montado donde ubicar el data-root de Docker")
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
