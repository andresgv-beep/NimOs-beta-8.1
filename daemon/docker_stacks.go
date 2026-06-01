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
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
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

	// Fase 2 (Beta 8.2) · inyectar labels com.nimos.* en cada servicio del
	// compose ANTES de escribirlo a disco. Los labels de un container Docker
	// son inmutables tras `docker create`, por lo que la única vía robusta
	// es modificar el YAML antes del `compose up -d`.
	//
	// Permite identificación robusta de containers gestionados por NimOS
	// (Fase 3 reconciler) sin depender de matching por nombre.
	//
	// Si la inyección falla (compose mal formado, etc.) el deploy continúa
	// con el compose original · NO bloqueante para no romper installs por
	// problemas en el catálogo.
	stackLabels := NewNimOSLabels(id, bodyStr(body, "appVersion"), session.Username, true)
	composeWithLabels, lerr := injectNimOSLabelsIntoCompose(compose, stackLabels)
	if lerr != nil {
		logMsg("docker: stack %s · inyección de labels falló (%v) · usando compose original", id, lerr)
		composeWithLabels = compose
	} else {
		logMsg("docker: stack %s · labels com.nimos.* inyectados en compose", id)
	}
	os.WriteFile(composePath, []byte(composeWithLabels), 0644)

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

	// APP-067 · Default seguro para variables ${VAR} no resueltas.
	//
	// Bug navidrome (descubierto 28/05, bloqueante 31/05): el compose usa
	// ${MUSIC_PATH}:/music:ro pero MUSIC_PATH no es canónica (CONFIG_PATH,
	// HOST_IP, TZ) ni viene en body.env. Queda sin definir → docker-compose
	// la expande a "" → spec ":/music:ro" → "empty section between colons"
	// → deploy FALLA con 500.
	//
	// Sin este fix, CUALQUIER app cuyo compose use una variable de path que
	// NimOS no conozca (MUSIC_PATH, PHOTOS_PATH, MEDIA_PATH, ...) rompería el
	// deploy con un error críptico.
	//
	// Solución: escanear el compose por ${VAR} (y ${VAR:-default}), y para
	// cada una que NO esté ya en autoEnv NI tenga default en el propio
	// compose, asignar un default seguro bajo CONFIG_PATH:
	//   MUSIC_PATH → {containerPath}/music
	//
	// La app SIEMPRE arranca con una carpeta vacía bajo su config. El usuario
	// puede luego apuntar la variable a su biblioteca real (editar .env y
	// recrear, o reinstalar con el valor en body.env · que tiene prioridad).
	//
	// NO toca variables que YA tienen default en el compose (${VAR:-/x}) ·
	// docker-compose las resuelve solo. NO toca las canónicas ni las de
	// body.env (ya están en autoEnv).
	autoEnv = fillUnresolvedPathVars(compose, autoEnv, containerPath)

	var lines []string
	for k, v := range autoEnv {
		lines = append(lines, fmt.Sprintf("%s=%v", k, v))
	}
	os.WriteFile(envFilePath, []byte(strings.Join(lines, "\n")+"\n"), 0644)

	// APP-068 · Pull de imágenes ANTES del up · necesario para poder
	// inspeccionar el UID de cada imagen y aplicar permisos correctos a los
	// volúmenes de apps con UID propio (Grafana=472, Postgres=999, ...).
	pullCmd := exec.Command("docker", "compose", "-f", composePath, "pull")
	pullCmd.Dir = stackPath
	if out, err := pullCmd.CombinedOutput(); err != nil {
		// El pull puede fallar por imágenes que no soportan pull (build local)
		// o problemas de red · no abortamos, el up reintentará el pull.
		logMsg("docker: compose pull para %s devolvió: %s (continuando)", id, string(out))
	}

	// APP-068 · Aplicar ACLs por UID de imagen a los volúmenes ANTES del up.
	// Apps con UID propio (no PUID/PGID) necesitan poder escribir en su
	// volumen al arrancar. Genérico · lee el UID de cada imagen, sin hardcode.
	applyUIDPermissions(compose, autoEnv)

	// Deploy
	cmd := exec.Command("docker", "compose", "-f", composePath, "up", "-d")
	cmd.Dir = stackPath
	if out, err := cmd.CombinedOutput(); err != nil {
		jsonError(w, 500, fmt.Sprintf("Failed to deploy stack: %s", string(out)))
		return
	}

	// Fix permissions on container config dir after deploy
	// Set group to nimos-share-docker-apps so FileManager can browse.
	// NOTA: esto añade el grupo, pero NO pisa las ACLs de UID puestas arriba
	// (setfacl -m añade, no reemplaza · ambas conviven).
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
	// BUG-FIX (Nextcloud install · 26/05/2026): usar context.Background() en lugar
	// de r.Context() porque el frontend puede dar timeout durante descargas pesadas
	// (Nextcloud ~1GB, Immich varios GB · 5-15min). Si el HTTP context se cancela,
	// `compose up` (subprocess externo) continúa hasta terminar PERO el INSERT a
	// docker_apps usaría un contexto cancelado y abortaría silenciosamente · resultado:
	// container vivo pero NimOS sin registro = invisible en AppStore/NimShield.
	//
	// Síntoma del bug original: Nextcloud apareció en docker ps + en docker_app_images
	// (poblada por goroutine con context.Background()) pero NO en docker_apps.
	if err := appsRepo.CreateOrUpdateDockerApp(context.Background(), app); err != nil {
		logMsg("docker: stack install register failed for %s: %v", id, err)
	}

	// Sprint Updates · poblar docker_app_images con los servicios del stack.
	// No bloqueante: si falla, se logea pero el deploy se considera OK.
	// Update-check posterior puede refrescar lo que falte.
	go populateAppImagesAfterDeploy(context.Background(), id, composePath, stackPath)

	// APP-034 · invalidación inmediata de cache de NimHealth (sync, ~150ms en Pi).
	// Sin esto, la app no aparece en /api/services hasta el siguiente tick (≤30s).
	// También usamos context.Background() · misma razón.
	ForceDockerCacheRefresh(context.Background())

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
	// commitContext() · debe persistir aunque cliente se desconecte (el cleanup
	// en goroutine ya está lanzado, la BD debe quedar consistente).
	if err := appsRepo.MarkDockerAppDeleting(commitContext(), safeId); err != nil {
		logMsg("docker: stack uninstall mark-deleting failed for %s: %v", safeId, err)
	}

	conf := getDockerConfigGo()
	dockerPath, _ := conf["path"].(string)
	if dockerPath == "" {
		if dp, err := getDockerPath(); err == nil {
			dockerPath = dp
		} else {
			// Sin path · borramos la row directamente, no hay nada de stack que limpiar.
			// commitContext() · borrado final debe persistir.
			if delErr := appsRepo.DeleteDockerApp(commitContext(), safeId); delErr != nil {
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
		// En modo wipe · capturar las imágenes del stack ANTES del down
		// (necesita los containers vivos para listarlas). Se borran después.
		// Usa el label com.nimos.app_id · inmune a variables sin definir en
		// el compose (bug navidrome/MUSIC_PATH).
		var stackImages []string
		if wipeCapture {
			stackImages = getStackImages(context.Background(), idCapture)
		}

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

			// Borrar las imágenes del stack (capturadas antes del down).
			// docker rmi SIN -f · seguro entre apps: si otra app usa una imagen
			// compartida, Docker la protege y no se borra.
			if len(stackImages) > 0 {
				n := removeAppImages(context.Background(), stackImages)
				logMsg("docker: stack %s · %d/%d imágenes borradas (wipe)", idCapture, n, len(stackImages))
			}
		} else {
			logMsg("docker: stack %s uninstalled in SOFT mode · data preserved at %s/containers/%s", idCapture, dockerPathCapture, idCapture)
		}

		// DELETE final libera la row de BD (en ambos modos)
		if err := appsRepo.DeleteDockerApp(context.Background(), idCapture); err != nil {
			logMsg("docker: stack uninstall final DB delete failed for %s: %v", idCapture, err)
		}
		// Sprint Updates · limpiar también las imágenes tracked.
		// Tanto modo soft como wipe: la app deja de aparecer instalada · sus
		// imágenes no necesitan tracking. Si reinstala, populateAppImagesAfterDeploy
		// las recreará con los digests actuales.
		if appImagesRepo != nil {
			if err := appImagesRepo.DeleteByApp(context.Background(), idCapture); err != nil {
				logMsg("docker: app images cleanup failed for %s: %v", idCapture, err)
			}
		}
		// APP-034 · refresh cache tras cleanup completo.
		ForceDockerCacheRefresh(context.Background())
	}()

	jsonOk(w, map[string]interface{}{"ok": true, "wipe": wipe})
}

// dockerPull · GET /api/docker/pull/{image}
//
// Wrapper sync/async sobre runDockerPullWork.

// composeVarPattern captura referencias a variables en un compose:
//   ${VAR}        → grupo 1 = "VAR", grupo 2 = ""        (sin default)
//   ${VAR:-foo}   → grupo 1 = "VAR", grupo 2 = ":-foo"   (con default · NO tocar)
//   ${VAR-foo}    → grupo 1 = "VAR", grupo 2 = "-foo"    (con default · NO tocar)
//   $VAR          → grupo 1 = "VAR", grupo 2 = ""        (forma sin llaves)
var composeVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:?-[^}]*)?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// fillUnresolvedPathVars escanea el texto del compose buscando variables
// ${VAR} que NO estén ya definidas en autoEnv y que NO tengan un default
// inline en el propio compose (${VAR:-x}). Para cada una, asigna un default
// seguro bajo containerPath: {containerPath}/{var_en_minúsculas}.
//
// Esto evita que un compose con una variable de path desconocida (MUSIC_PATH,
// PHOTOS_PATH, etc.) rompa el deploy con "empty section between colons".
//
// Reglas:
//   - Si la var YA está en autoEnv (canónica o body.env) → no se toca.
//   - Si la var tiene default inline ${VAR:-x} → no se toca (compose lo resuelve).
//   - El nombre del directorio default es el nombre de la var en minúsculas,
//     quitando sufijos comunes "_PATH"/"_DIR" para que quede limpio:
//       MUSIC_PATH  → {containerPath}/music
//       PHOTOS_DIR  → {containerPath}/photos
//       DATA        → {containerPath}/data
//
// Devuelve autoEnv modificado (mismo mapa).
func fillUnresolvedPathVars(compose string, autoEnv map[string]interface{}, containerPath string) map[string]interface{} {
	matches := composeVarPattern.FindAllStringSubmatch(compose, -1)
	for _, m := range matches {
		// m[1] = nombre con llaves · m[2] = default inline (si hay) · m[3] = nombre sin llaves
		varName := m[1]
		hasInlineDefault := m[2] != ""
		if varName == "" {
			varName = m[3] // forma $VAR sin llaves
		}
		if varName == "" {
			continue
		}
		// Ya definida → no tocar
		if _, exists := autoEnv[varName]; exists {
			continue
		}
		// Tiene default inline en el compose → docker-compose lo resuelve solo
		if hasInlineDefault {
			continue
		}
		// Variable sin resolver · asignar default seguro bajo containerPath
		dirName := defaultDirNameForVar(varName)
		defaultPath := filepath.Join(containerPath, dirName)
		// Crear el directorio · si no existe, Docker lo crearía como root con
		// permisos restrictivos. Lo creamos nosotros con permisos abiertos
		// para que la app (PUID/PGID) pueda escribir y el usuario navegar.
		os.MkdirAll(defaultPath, 0775)
		autoEnv[varName] = defaultPath
		logMsg("docker: stack · variable %q sin definir, default seguro → %s "+
			"(el usuario puede cambiarla luego)", varName, defaultPath)
	}
	return autoEnv
}

// defaultDirNameForVar deriva un nombre de directorio limpio del nombre de una
// variable: minúsculas y sin sufijos _PATH/_DIR/_LOCATION.
//   MUSIC_PATH → "music"  ·  PHOTOS_DIR → "photos"  ·  MEDIA → "media"
func defaultDirNameForVar(varName string) string {
	n := strings.ToLower(varName)
	for _, suffix := range []string{"_path", "_dir", "_location", "_folder"} {
		n = strings.TrimSuffix(n, suffix)
	}
	if n == "" {
		n = strings.ToLower(varName)
	}
	return n
}

// ─────────────────────────────────────────────────────────────────────────
// APP-068 · Permisos de volúmenes para apps con UID propio
// ─────────────────────────────────────────────────────────────────────────
//
// Problema: NimOS crea los volúmenes como root:nimos-share-docker-apps. Las
// apps linuxserver.io (PUID/PGID) funcionan, pero las que corren con un UID
// fijo propio (Grafana=472, PostgreSQL=999, ...) NO pueden escribir en su
// volumen → "permission denied" → la app falla al arrancar.
//
// Solución: detectar el UID de la imagen de cada servicio y añadir una ACL
// (setfacl) para ese UID sobre SUS volúmenes. Genérico · lee el UID de la
// imagen, no de una lista hardcodeada de apps. No cambia el dueño/grupo (el
// modelo de permisos por share de NimOS queda intacto · la ACL solo añade
// al proceso interno del container, no a ningún usuario humano).
//
// Debe ejecutarse ANTES del `compose up` · grafana/postgres escriben al
// arrancar, necesitan los permisos ya puestos.

// composeForPerms es una vista mínima del compose para extraer servicios,
// sus imágenes y sus volúmenes (bind mounts).
type composeForPerms struct {
	Services map[string]struct {
		Image   string   `yaml:"image"`
		Volumes []string `yaml:"volumes"`
	} `yaml:"services"`
}

// applyUIDPermissions parsea el compose, y para cada servicio cuya imagen
// declare un UID propio no-root, aplica una ACL a los volúmenes de ese
// servicio que residan bajo el pool.
//
//   compose      · texto del compose (con variables aún sin expandir)
//   envVars      · el .env ya resuelto (para expandir ${VAR} en los volúmenes)
//   pulledImages · si las imágenes ya están (para poder inspeccionar su UID)
//
// No falla la instalación si algo va mal · solo loguea. Mejor instalar con
// un posible permiso de menos (que el workaround manual arregla) que abortar.
func applyUIDPermissions(compose string, envVars map[string]interface{}) {
	var parsed composeForPerms
	if err := yaml.Unmarshal([]byte(compose), &parsed); err != nil {
		logMsg("docker: applyUIDPermissions · no se pudo parsear compose: %v (se omite)", err)
		return
	}

	for svcName, svc := range parsed.Services {
		if svc.Image == "" {
			continue
		}
		// UID que usa la imagen · docker inspect lee Config.User de la imagen.
		uid := imageUID(svc.Image)
		if uid == "" || uid == "0" || uid == "root" {
			// Sin UID propio (corre como root) o linuxserver (PUID/PGID) ·
			// no necesita ACL especial.
			continue
		}

		// Para cada volumen tipo "host:container[:opts]", si el lado host
		// (tras expandir variables) está bajo el pool, aplicar ACL.
		for _, vol := range svc.Volumes {
			hostPath := resolveVolumeHostPath(vol, envVars)
			if hostPath == "" {
				continue
			}
			if !strings.HasPrefix(hostPath, nimosPoolsRoot()) {
				continue // volumen fuera del pool (ej. /etc/localtime) · no tocar
			}
			// Crear el dir si no existe (Docker lo crearía como root si no)
			os.MkdirAll(hostPath, 0775)
			// ACL para el UID del servicio · no cambia dueño/grupo.
			runSafe("setfacl", "-R", "-m", "u:"+uid+":rwx", hostPath)
			runSafe("setfacl", "-R", "-d", "-m", "u:"+uid+":rwx", hostPath)
			logMsg("docker: applyUIDPermissions · ACL u:%s:rwx en %s (servicio %s)",
				uid, hostPath, svcName)
		}
	}
}

// imageUID devuelve el UID (o nombre de usuario) que la imagen declara en
// Config.User. Vacío si corre como root o no se puede determinar.
func imageUID(image string) string {
	out, ok := runSafe("docker", "inspect", image, "--format", "{{.Config.User}}")
	if !ok {
		return ""
	}
	user := strings.TrimSpace(out)
	// Formatos posibles: "472", "472:472", "grafana", "postgres", "".
	// Nos quedamos con la parte antes de ":" (el usuario/uid).
	if idx := strings.IndexByte(user, ':'); idx >= 0 {
		user = user[:idx]
	}
	return user
}

// resolveVolumeHostPath extrae el lado host de un volumen "host:container[:opts]"
// y expande las variables ${VAR} usando envVars. Devuelve "" si es un volumen
// con nombre (no bind mount) o no se puede resolver.
func resolveVolumeHostPath(vol string, envVars map[string]interface{}) string {
	parts := strings.SplitN(vol, ":", 2)
	if len(parts) < 2 {
		return "" // volumen con nombre (ej. "model-cache:/cache") · no bind mount
	}
	host := strings.TrimSpace(parts[0])
	// Expandir ${VAR} y $VAR con envVars
	host = expandComposeVars(host, envVars)
	// Si tras expandir sigue teniendo $ o está vacío, no es resoluble
	if host == "" || strings.Contains(host, "$") {
		return ""
	}
	// Solo rutas absolutas (bind mounts), no volúmenes con nombre
	if !strings.HasPrefix(host, "/") {
		return ""
	}
	return host
}

// expandComposeVars sustituye ${VAR} y $VAR en s usando envVars.
func expandComposeVars(s string, envVars map[string]interface{}) string {
	return composeVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		m := composeVarPattern.FindStringSubmatch(match)
		name := m[1]
		if name == "" {
			name = m[3]
		}
		if val, ok := envVars[name]; ok {
			return fmt.Sprintf("%v", val)
		}
		return match // no resuelta · dejar tal cual (se filtrará luego)
	})
}

// nimosPoolsRoot devuelve el prefijo de los pools para validar que un volumen
// está dentro del área gestionada por NimOS.
func nimosPoolsRoot() string {
	return "/nimos/pools/"
}
