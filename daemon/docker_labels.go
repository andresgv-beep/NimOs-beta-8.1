// docker_labels.go — Sistema de labels NimOS para containers Docker.
//
// Beta 8.2 · Fase 2 (27/05/2026).
//
// Origen: bug Nextcloud (26/05/2026). Identificar containers gestionados por
// NimOS dependía de matching por nombre vs docker_apps (circular y frágil).
// Si la BD se desincroniza (ej. bug Nextcloud), un container vivo queda
// invisible para reconcile.
//
// Solución: cada container instalado por NimOS lleva labels `com.nimos.*`.
// Eso permite:
//   - docker ps --filter "label=com.nimos.managed=true" → SOLO containers NimOS
//   - Reconcile fiable (Fase 3) · comparación bidireccional con docker_apps
//   - Auditoría · quién instaló qué y cuándo
//   - Robustez · BD puede estar incompleta, los labels en Docker no mienten
//
// ─────────────────────────────────────────────────────────────────────────
// SCHEMA
// ─────────────────────────────────────────────────────────────────────────
//
//   com.nimos.schema_version   · "beta_8.2" (ver SchemaVersion abajo)
//   com.nimos.managed          · "true" · marcador universal
//   com.nimos.app_id           · "nextcloud", "jellyfin", ...
//   com.nimos.app_version      · "29.0.7" del catálogo (puede estar vacío)
//   com.nimos.installed_by     · username
//   com.nimos.installed_at     · ISO 8601 UTC
//   com.nimos.stack            · "true" si es parte de stack, "false" si single
//
// ─────────────────────────────────────────────────────────────────────────
// APLICACIÓN
// ─────────────────────────────────────────────────────────────────────────
//
// Para stacks (docker compose up -d): `docker container update --label-add`
// se ejecuta tras el up exitoso, sobre los containers del stack.
//
// Para single containers (docker run): los labels se añaden directamente
// como flags --label en el comando docker run.
//
// NOTA · `docker container update --label-add` requiere Docker 23.0+.
// En Docker viejo hay que destruir+recrear, pero NimOS ya requiere Docker
// moderno por compose v2.
//
// ─────────────────────────────────────────────────────────────────────────
// VERSIONADO
// ─────────────────────────────────────────────────────────────────────────
//
// schema_version permite evolucionar el set de labels sin romper consumers.
// Cuando se añadan/quiten labels en un futuro:
//   1. Incrementar SchemaVersion (ej. "v1.0")
//   2. El reconciler reconoce ambas versiones durante migración
//   3. Tras N días, drop soporte de version antigua

package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────
// Constantes
// ─────────────────────────────────────────────────────────────────────────

// SchemaVersion · valor del label com.nimos.schema_version.
// Cuando se cambie el set de labels, actualizar esto y documentar la
// migración en CHANGELOG.md.
const SchemaVersion = "beta_8.2"

// Nombres canónicos de los labels NimOS.
// Usar SIEMPRE estas constantes, nunca strings literales.
const (
	LabelSchemaVersion = "com.nimos.schema_version"
	LabelManaged       = "com.nimos.managed"
	LabelAppID         = "com.nimos.app_id"
	LabelAppVersion    = "com.nimos.app_version"
	LabelInstalledBy   = "com.nimos.installed_by"
	LabelInstalledAt   = "com.nimos.installed_at"
	LabelStack         = "com.nimos.stack"
)

// labelApplyTimeout · timeout para los `docker container update --label-add`.
// Operación local sobre el daemon Docker, debería ser instantánea. Margen
// generoso por si Docker está ocupado.
const labelApplyTimeout = 30 * time.Second

// ─────────────────────────────────────────────────────────────────────────
// Modelos
// ─────────────────────────────────────────────────────────────────────────

// NimOSLabels · set completo de labels NimOS para un container.
// Se construye en el handler de install y se aplica tras compose up.
type NimOSLabels struct {
	AppID        string
	AppVersion   string // puede estar vacío
	InstalledBy  string
	InstalledAt  string // ISO 8601 UTC
	IsStack      bool   // true si parte de un stack, false si single container
}

// NewNimOSLabels construye un set de labels para una nueva instalación.
// installedAt se rellena automáticamente con time.Now().UTC().
func NewNimOSLabels(appID, appVersion, installedBy string, isStack bool) NimOSLabels {
	return NimOSLabels{
		AppID:       appID,
		AppVersion:  appVersion,
		InstalledBy: installedBy,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
		IsStack:     isStack,
	}
}

// ToDockerLabelArgs devuelve los argumentos `--label key=value` para usar
// directamente en `docker run`. Cada label es un par de strings en el slice
// para facilitar el spread en exec.Command.
//
// Ejemplo:
//   labels := NewNimOSLabels("nextcloud", "29.0.7", "andres", true)
//   args := append([]string{"run", "-d", "--name", "nextcloud"},
//                  labels.ToDockerLabelArgs()...)
//   args = append(args, "nextcloud:latest")
//   exec.Command("docker", args...)
func (l NimOSLabels) ToDockerLabelArgs() []string {
	stackVal := "false"
	if l.IsStack {
		stackVal = "true"
	}
	return []string{
		"--label", LabelSchemaVersion + "=" + SchemaVersion,
		"--label", LabelManaged + "=true",
		"--label", LabelAppID + "=" + l.AppID,
		"--label", LabelAppVersion + "=" + l.AppVersion,
		"--label", LabelInstalledBy + "=" + l.InstalledBy,
		"--label", LabelInstalledAt + "=" + l.InstalledAt,
		"--label", LabelStack + "=" + stackVal,
	}
}

// ToLabelAddArgs devuelve los argumentos `--label-add key=value` para usar
// con `docker container update`. Es la sintaxis diferente que usa el
// comando update vs run (la coherencia de Docker CLI deja que desear).
func (l NimOSLabels) ToLabelAddArgs() []string {
	stackVal := "false"
	if l.IsStack {
		stackVal = "true"
	}
	return []string{
		"--label-add", LabelSchemaVersion + "=" + SchemaVersion,
		"--label-add", LabelManaged + "=true",
		"--label-add", LabelAppID + "=" + l.AppID,
		"--label-add", LabelAppVersion + "=" + l.AppVersion,
		"--label-add", LabelInstalledBy + "=" + l.InstalledBy,
		"--label-add", LabelInstalledAt + "=" + l.InstalledAt,
		"--label-add", LabelStack + "=" + stackVal,
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Aplicación de labels
// ─────────────────────────────────────────────────────────────────────────

// applyLabelsToContainer aplica los labels NimOS a un container existente
// vía `docker container update --label-add`. Usado tras compose up exitoso.
//
// Si el container no existe (raro, indicaría bug en flujo de install),
// devuelve error. Si Docker daemon no responde, devuelve error con detalles.
//
// IMPORTANTE: `docker container update --label-add` requiere Docker 23.0+.
// NimOS ya requiere Docker moderno (compose v2 + manifest inspect), no es
// regresión.
func applyLabelsToContainer(ctx context.Context, containerName string, labels NimOSLabels) error {
	if containerName == "" {
		return fmt.Errorf("applyLabelsToContainer: containerName vacío")
	}

	cctx, cancel := context.WithTimeout(ctx, labelApplyTimeout)
	defer cancel()

	args := append([]string{"container", "update"}, labels.ToLabelAddArgs()...)
	args = append(args, containerName)

	cmd := exec.CommandContext(cctx, "docker", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker container update %s: %w (output: %s)",
			containerName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// applyLabelsToStack aplica los labels NimOS a TODOS los containers de un
// stack tras `compose up -d` exitoso. Lista los containers vía `docker compose
// ps -q` y aplica labels uno por uno.
//
// Si un container individual falla, se loguea pero NO aborta · queremos que
// la mayoría queden etiquetados aunque uno tenga problema (sería visible en
// el reconciler Fase 3).
//
// Devuelve el número de containers etiquetados con éxito.
func applyLabelsToStack(ctx context.Context, composePath, stackDir string, labels NimOSLabels) (int, error) {
	if composePath == "" {
		return 0, fmt.Errorf("applyLabelsToStack: composePath vacío")
	}

	cctx, cancel := context.WithTimeout(ctx, labelApplyTimeout)
	defer cancel()

	// `docker compose ps -q` lista solo los IDs de containers del stack
	psCmd := exec.CommandContext(cctx, "docker", "compose", "-f", composePath, "ps", "-q")
	if stackDir != "" {
		psCmd.Dir = stackDir
	}
	out, err := psCmd.Output()
	if err != nil {
		return 0, fmt.Errorf("docker compose ps -q: %w", err)
	}

	containerIDs := strings.Fields(strings.TrimSpace(string(out)))
	if len(containerIDs) == 0 {
		return 0, fmt.Errorf("no containers en stack %s tras compose up", composePath)
	}

	success := 0
	for _, id := range containerIDs {
		if err := applyLabelsToContainer(ctx, id, labels); err != nil {
			logMsg("docker_labels: failed to label container %s of stack %s: %v",
				id, composePath, err)
			continue
		}
		success++
	}
	return success, nil
}

// ─────────────────────────────────────────────────────────────────────────
// Consulta de containers gestionados
// ─────────────────────────────────────────────────────────────────────────

// NimOSContainer · representación mínima de un container gestionado.
// Resultado de listNimOSContainers · usado por reconciler (Fase 3).
type NimOSContainer struct {
	ID          string // ID del container Docker
	Name        string // Nombre (sin slash prefix)
	AppID       string // Valor del label com.nimos.app_id
	IsStack     bool   // Valor del label com.nimos.stack
	SchemaVer   string // Valor del label com.nimos.schema_version
}

// listNimOSContainers devuelve los containers que tienen label
// com.nimos.managed=true, vivos o parados. Es la fuente de verdad para
// el reconciler Docker (Fase 3).
//
// El reconciler compara esta lista con `docker_apps` y detecta:
//   - Containers vivos sin row (huérfanos a importar)
//   - Rows sin container correspondiente (apps perdidas)
func listNimOSContainers(ctx context.Context) ([]NimOSContainer, error) {
	cctx, cancel := context.WithTimeout(ctx, labelApplyTimeout)
	defer cancel()

	// docker ps -a · incluir parados también (el reconciler decide qué hacer)
	// --filter label=com.nimos.managed=true · solo containers NimOS
	// --format · campos separados por tabs para parsing trivial
	cmd := exec.CommandContext(cctx, "docker", "ps", "-a",
		"--filter", "label="+LabelManaged+"=true",
		"--format", "{{.ID}}\t{{.Names}}\t{{.Label \""+LabelAppID+"\"}}\t{{.Label \""+LabelStack+"\"}}\t{{.Label \""+LabelSchemaVersion+"\"}}")

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps --filter label: %w", err)
	}

	var result []NimOSContainer
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 5 {
			logMsg("docker_labels: malformed ps output line: %q (skipping)", line)
			continue
		}
		result = append(result, NimOSContainer{
			ID:        fields[0],
			Name:      fields[1],
			AppID:     fields[2],
			IsStack:   fields[3] == "true",
			SchemaVer: fields[4],
		})
	}
	return result, nil
}
