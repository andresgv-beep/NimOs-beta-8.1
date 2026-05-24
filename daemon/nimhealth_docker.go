// nimhealth_docker.go — Lógica de parsing Docker para NimHealth.
//
// Aislado de los handlers HTTP y del observer para que el archivo de
// docker parsing tenga responsabilidad única: cruzar docker_apps
// (config persistente) con docker ps -a (runtime) y devolver una lista
// de DockerAppStatus enriquecida.
//
// El observer (nimhealth_observer.go) llama a getDockerAppStatuses
// UNA vez por tick (~30s) y guarda el resultado en dockerCache.
// El handler HTTP NUNCA llama estas funciones directamente · lee cache.
//
// REGLA: cero docker stats, cero docker inspect periódico. Solo
// docker ps -a. Si se necesita inspect, es on-demand desde un endpoint
// /detail (futuro, fuera de scope actual).

package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// dockerContainer · línea parseada de docker ps -a
type dockerContainer struct {
	Name   string
	Image  string
	Status string // raw: "Up 3 hours", "Exited (0) 2h ago", ...
	Ports  string // raw: "0.0.0.0:8096->8096/tcp, ..."
}

// ═══════════════════════════════════════════════════════════════════════
// Aggregate health · Docker engine como agregado de sus children
//
// Vocabulario oficial HealthStatus (7 constantes en nimos_health.go).
// NimHealth usa 6 de esas 7 (NO usa HealthIncomplete · ver §4.7 doc).
//
// Reglas:
//
//	no children                                     → HealthHealthy (engine OK vacío)
//	all containers stopped (engine OK)              → HealthHealthy (no es failure)
//	any child status=error  OR  health=failed       → HealthDegraded
//	any child stopped + otros running               → HealthDegraded (mix)
//	all running+healthy                              → HealthHealthy
// ═══════════════════════════════════════════════════════════════════════

func ComputeDockerAggregateHealth(children []DockerAppStatus) HealthStatus {
	if len(children) == 0 {
		return HealthHealthy
	}
	allStopped := true
	hasError := false
	for _, c := range children {
		if c.Status != "stopped" {
			allStopped = false
		}
		if c.Status == "error" || c.Health == string(HealthFailed) {
			hasError = true
		}
	}
	if hasError {
		return HealthDegraded
	}
	if allStopped {
		// Engine arriba, containers todos parados · no es failure
		return HealthHealthy
	}
	// Hay al menos uno running · si alguno está stopped es mix → degraded
	for _, c := range children {
		if c.Status == "stopped" {
			return HealthDegraded
		}
	}
	return HealthHealthy
}

// ═══════════════════════════════════════════════════════════════════════
// getDockerAppStatuses · construye DockerAppStatus para cada app
// registrada cruzando docker_apps (SQLite) con docker ps -a.
//
// Devuelve la lista y el conteo de containers huérfanos.
// ═══════════════════════════════════════════════════════════════════════

func getDockerAppStatuses(dockerServiceID string) ([]DockerAppStatus, int) {
	ctx := context.Background()

	// 1. Apps registradas en la DB
	registered, err := appsRepo.ListDockerApps(ctx)
	if err != nil {
		logMsg("apps: getDockerAppStatuses list failed: %v", err)
		return []DockerAppStatus{}, 0
	}

	// 2. docker ps -a (ALL containers, not just running)
	out, _ := runSafe("docker", "ps", "-a", "--format", "{{.Names}}|{{.Image}}|{{.Status}}|{{.Ports}}")
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

	// 3. Cross: para cada app registrada, encontrar su container
	var statuses []DockerAppStatus
	matchedContainers := map[string]bool{}

	for _, reg := range registered {
		var found *dockerContainer
		var containerName string
		// Prueba exact match y prefijos de stack
		for _, suffix := range []string{"", "_server", "-server", "_app", "-app"} {
			candidate := reg.ID + suffix
			if c, ok := containers[candidate]; ok {
				found = &c
				containerName = candidate
				matchedContainers[candidate] = true
				break
			}
		}
		// Fallback: prefix match (immich_server → immich)
		if found == nil {
			for name, c := range containers {
				if strings.HasPrefix(name, reg.ID+"_") || strings.HasPrefix(name, reg.ID+"-") {
					found = &c
					containerName = name
					matchedContainers[name] = true
					break
				}
			}
		}

		// Construir status base
		// Default: stopped/healthy · stopped no es failure por sí mismo,
		// el campo status ya transmite la inactividad.
		status := DockerAppStatus{
			ServiceBase: ServiceBase{
				ID:     reg.ID,
				Type:   "docker-app",
				Parent: dockerServiceID,
				Name:   reg.Name,
				Status: "stopped",
				Health: string(HealthHealthy),
			},
			Image:         reg.Image,
			Icon:          reg.Icon,
			ContainerName: containerName,
			OpenMode:      reg.OpenMode,
		}

		if found != nil {
			status.Status = NormalizeDockerStatus(found.Status)
			if status.Status == "running" {
				status.Health = string(HealthHealthy)
			} else if status.Status == "error" {
				status.Health = string(HealthFailed)
			}
			status.Uptime = extractUptime(found.Status)
			status.Ports = parsePorts(found.Ports, reg)
		} else {
			// Registrada pero sin container
			status.Status = "stopped"
			status.Health = string(HealthHealthy)
			if reg.Port > 0 {
				status.Ports = []PortBinding{{Declared: reg.Port, Host: reg.Port}}
			}
		}

		statuses = append(statuses, status)
	}

	// 4. Contar orphans (en docker ps pero no en docker_apps)
	orphanCount := 0
	stackSubs := []string{"_redis", "_postgres", "_ml", "_machine", "_db", "_cache"}
	for name := range containers {
		if matchedContainers[name] {
			continue
		}
		isStackSub := false
		for _, sub := range stackSubs {
			if strings.Contains(name, sub) {
				isStackSub = true
				break
			}
		}
		if !isStackSub {
			for matched := range matchedContainers {
				prefix := strings.SplitN(matched, "_", 2)[0]
				if strings.HasPrefix(name, prefix+"_") || strings.HasPrefix(name, prefix+"-") {
					isStackSub = true
					break
				}
			}
		}
		if !isStackSub {
			orphanCount++
		}
	}

	if statuses == nil {
		statuses = []DockerAppStatus{}
	}
	return statuses, orphanCount
}

// extractUptime · de docker ps STATUS, e.g. "Up 3 hours" → "3h"
func extractUptime(rawStatus string) string {
	lower := strings.ToLower(rawStatus)
	if !strings.Contains(lower, "up") {
		return ""
	}
	upRegex := regexp.MustCompile(`(?i)up\s+([^,(]+)`)
	matches := upRegex.FindStringSubmatch(rawStatus)
	if len(matches) < 2 {
		return ""
	}
	dur := strings.TrimSpace(matches[1])
	dur = strings.ReplaceAll(dur, " hours", "h")
	dur = strings.ReplaceAll(dur, " hour", "h")
	dur = strings.ReplaceAll(dur, " minutes", "m")
	dur = strings.ReplaceAll(dur, " minute", "m")
	dur = strings.ReplaceAll(dur, " seconds", "s")
	dur = strings.ReplaceAll(dur, " second", "s")
	dur = strings.ReplaceAll(dur, " days", "d")
	dur = strings.ReplaceAll(dur, " day", "d")
	dur = strings.ReplaceAll(dur, " weeks", "w")
	dur = strings.ReplaceAll(dur, " week", "w")
	dur = strings.ReplaceAll(dur, "About a ", "1")
	dur = strings.ReplaceAll(dur, "About an ", "1")
	return strings.TrimSpace(dur)
}

// parsePorts · extrae bindings de docker ps PORTS, mergeando con config.
func parsePorts(rawPorts string, config *DBDockerApp) []PortBinding {
	if rawPorts == "" {
		if config != nil && config.Port > 0 {
			return []PortBinding{{Declared: config.Port, Host: config.Port}}
		}
		return []PortBinding{}
	}

	var bindings []PortBinding
	portRegex := regexp.MustCompile(`(\d+):(\d+)/`)
	matches := portRegex.FindAllStringSubmatch(rawPorts, -1)
	seen := map[string]bool{}
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		key := m[1] + ":" + m[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		var host, declared int
		fmt.Sscanf(m[1], "%d", &host)
		fmt.Sscanf(m[2], "%d", &declared)
		bindings = append(bindings, PortBinding{Host: host, Declared: declared})
	}
	if bindings == nil {
		bindings = []PortBinding{}
	}
	return bindings
}
