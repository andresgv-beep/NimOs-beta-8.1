// docker_stacks.go — Stacks docker-compose (Beta 8.1)
//
// Stacks = aplicaciones multi-container con docker-compose.yml.
// Usado principalmente por el AppStore para apps del catálogo (Jellyfin,
// Immich, VS Code Server, etc).
//
// Endpoints:
//   POST   /api/docker/stack        · deploy nuevo stack (compose + .env)
//   DELETE /api/docker/stack/<id>   · elimina stack completo
//
// Variables canónicas inyectadas automáticamente en .env de cada stack:
//   CONFIG_PATH · {dockerPath}/containers/{stackId}
//   HOST_IP     · IP local del NAS (getStackHostIP en docker_async.go)
//   TZ          · timezone del host (getStackTimezone en docker_async.go)
//
// Tras escribir .env se expanden referencias ${VAR} recursivamente con
// expandStackEnvRefs (máximo 4 pasadas anti-loop). Esto permite al catálogo
// definir vars compuestas como PROJECTS_PATH=${CONFIG_PATH}/projects.

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
	//   TZ          · timezone del host (e.g. "Europe/Madrid"). Apps con
	//                 cron interno o logs timestamp lo necesitan. Si no
	//                 se puede determinar, queda "UTC".
	//
	// Estas vars se inyectan SIEMPRE antes de escribir .env. Si el body del
	// frontend manda también values en body.env, esos prevalecen (override).
	//
	// Tras el merge, expandimos referencias ${OTRA_VAR} dentro de los values
	// recursivamente · permite que el catálogo defina vars compuestas como
	// `PROJECTS_PATH = ${CONFIG_PATH}/projects` y que se resuelvan al path
	// completo antes de que docker-compose lo lea. Sin esta expansión, las
	// vars del .env no se interpolan entre sí (limitación de docker-compose).
	autoEnv := map[string]interface{}{
		"CONFIG_PATH": containerPath,
		"HOST_IP":     getStackHostIP(),
		"TZ":          getStackTimezone(),
	}
	// Merge body.env encima · permitir override desde el catálogo si hace falta
	if env, ok := body["env"].(map[string]interface{}); ok {
		for k, v := range env {
			autoEnv[k] = v
		}
	}
	// Expandir referencias ${KEY} dentro de values · max 4 pasadas para evitar
	// loops infinitos en caso de referencia circular (raro, pero defensivo).
	autoEnv = expandStackEnvRefs(autoEnv, 4)

	// APP-066 · Resolver placeholders {RANDOM} con persistencia idempotente.
	//
	// Algunos catálogos declaran credenciales internas del stack como literal
	// "{RANDOM}" (ejemplo: Immich · DB_PASSWORD entre immich-server y su
	// Postgres interno). El user nunca ve esos valores · son comunicación
	// máquina-a-máquina dentro de la red Docker del stack.
	//
	// Sin resolución, el valor llega literal a docker-compose y Postgres se
	// inicializa con la cadena "{RANDOM}" como password · funcional pero
	// inseguro (todos los Immich del mundo tendrían misma pass).
	//
	// La función es IDEMPOTENTE por construcción · lee el .env previo si
	// existe y reusa valores ya generados. Esto significa:
	//   · Primera instalación · genera 24 chars aleatorios
	//   · Reinstalación con .env previo · mantiene valor previo (no rompe
	//     Postgres data dir que tiene el hash del valor anterior)
	//
	// El user nunca tiene que tocar esto · totalmente transparente.
	envFilePath := filepath.Join(stackPath, ".env")
	autoEnv = resolveRandomPlaceholders(autoEnv, envFilePath)

	var lines []string
	for k, v := range autoEnv {
		lines = append(lines, fmt.Sprintf("%s=%v", k, v))
	}
	os.WriteFile(envFilePath, []byte(strings.Join(lines, "\n")+"\n"), 0644)

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
// dockerStackDelete · DELETE /api/docker/stack/<id>[?wipe=true]
//
// Dos modos de operación según query param `wipe`:
//
//   wipe=false (default · recomendado para el user) · "DESINSTALACIÓN SUAVE"
//     · docker compose down --remove-orphans · containers fuera
//     · NO se borran volúmenes Docker (-v) · datos en containers/{id} intactos
//     · NO se borra stackPath ni containers/{id} · compose YAML, .env y datos
//       de la app se conservan
//     · Resultado: si reinstalas la app más tarde, todo vuelve donde estaba.
//       Postgres encuentra su data dir, Immich su BD, Jellyfin su biblioteca.
//
//   wipe=true · "DESINSTALACIÓN COMPLETA · DESTRUCTIVA"
//     · docker compose down -v --remove-orphans · containers + volúmenes Docker
//     · Borra stackPath (docker-compose.yml + .env)
//     · Borra containers/{id} (uploads, postgres data, configs de la app)
//     · NO se puede deshacer.
//
// En ambos casos: la row en docker_apps se elimina · la app deja de aparecer
// como instalada en el AppStore.
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

	// Detectar modo · default es suave (preservar datos)
	wipe := r.URL.Query().Get("wipe") == "true"

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
	wipeCapture := wipe
	dockerPathCapture := dockerPath
	go func() {
		if _, err := os.Stat(composePath); err == nil {
			// Argumentos de compose down · siempre --remove-orphans, solo añade -v
			// en modo wipe (destruir volúmenes Docker)
			downArgs := []string{"compose", "-f", composePath, "down", "--remove-orphans"}
			if wipeCapture {
				downArgs = append(downArgs[:len(downArgs)-1], "-v", "--remove-orphans")
			}
			cmd := exec.Command("docker", downArgs...)
			cmd.Dir = stackPath
			cmd.Run()
		}

		// En modo wipe · borrar stack path (compose YAML + .env) y datos
		if wipeCapture {
			os.RemoveAll(stackPath)
			os.RemoveAll(filepath.Join(dockerPathCapture, "containers", idCapture))
			logMsg("docker: stack %s uninstalled in WIPE mode · all data removed", idCapture)
		} else {
			logMsg("docker: stack %s uninstalled in SOFT mode · data preserved at %s/containers/%s", idCapture, dockerPathCapture, idCapture)
		}

		// DELETE final libera la row de BD (en ambos modos)
		if err := appsRepo.DeleteDockerApp(context.Background(), idCapture); err != nil {
			logMsg("docker: stack uninstall final DB delete failed for %s: %v", idCapture, err)
		}
		// APP-034 · refresh cache tras cleanup completo.
		ForceDockerCacheRefresh(context.Background())
	}()

	jsonOk(w, map[string]interface{}{"ok": true, "wipe": wipe})
}

// dockerPull · GET /api/docker/pull/{image}
//
// Wrapper sync/async sobre runDockerPullWork.
