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
// **CRÍTICO**: los labels de un container Docker son INMUTABLES tras el
// `docker create`. NO se pueden añadir post-creación con `docker container
// update` (esa API NO acepta --label-add · solo cambia recursos como CPU/RAM).
// Solo `docker service update` (Swarm) acepta --label-add, y NimOS no usa
// Swarm.
//
// Por tanto, los labels deben aplicarse AL CREAR el container:
//
//   Single containers (docker_containers.go) · args --label en `docker run`
//   Stacks (docker_stacks.go) · injection en el YAML antes de `compose up -d`
//
// La función injectNimOSLabelsIntoCompose modifica el YAML del compose
// recibido del catálogo, añadiendo el bloque `labels:` a cada servicio,
// y devuelve el YAML modificado para escribir a disco.
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

	"gopkg.in/yaml.v3"
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

// dockerCmdTimeout · timeout para comandos docker locales (ps, inspect).
// Estos solo consultan el daemon Docker local, deberían ser instantáneos.
const dockerCmdTimeout = 30 * time.Second

// ─────────────────────────────────────────────────────────────────────────
// Modelos
// ─────────────────────────────────────────────────────────────────────────

// NimOSLabels · set completo de labels NimOS para un container.
// Se construye en el handler de install y se aplica al CREAR el container
// (no es posible aplicarlos después en Docker).
type NimOSLabels struct {
	AppID       string
	AppVersion  string // puede estar vacío
	InstalledBy string
	InstalledAt string // ISO 8601 UTC
	IsStack     bool   // true si parte de un stack, false si single container
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
// directamente en `docker run`. Usado por docker_containers.go.
//
// Ejemplo:
//   labels := NewNimOSLabels("jellyfin", "10.11", "andres", false)
//   args := append([]string{"run", "-d", "--name", "jellyfin"},
//                  labels.ToDockerLabelArgs()...)
//   args = append(args, "jellyfin:latest")
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

// ToMap devuelve el set de labels como map[string]string. Usado por
// injectNimOSLabelsIntoCompose para añadirlos al YAML del stack.
func (l NimOSLabels) ToMap() map[string]string {
	stackVal := "false"
	if l.IsStack {
		stackVal = "true"
	}
	return map[string]string{
		LabelSchemaVersion: SchemaVersion,
		LabelManaged:       "true",
		LabelAppID:         l.AppID,
		LabelAppVersion:    l.AppVersion,
		LabelInstalledBy:   l.InstalledBy,
		LabelInstalledAt:   l.InstalledAt,
		LabelStack:         stackVal,
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Inyección en YAML de compose
// ─────────────────────────────────────────────────────────────────────────

// injectNimOSLabelsIntoCompose parsea el YAML de un docker-compose recibido
// del catálogo y añade los labels com.nimos.* a cada servicio bajo la clave
// `services:`. Devuelve el YAML modificado.
//
// Si el compose ya tiene labels en algún servicio, los labels NimOS se
// MERGEAN (no se sobreescriben los labels del usuario; los NimOS se añaden).
// Si hay colisión en un nombre exacto de label (ej. el catálogo ya define
// "com.nimos.app_id"), el NimOS gana porque es metadato de gestión.
//
// La función NO modifica:
//   - El resto del YAML (volumes, networks, secrets, ...)
//   - El orden de los servicios
//   - Otros campos de los servicios (image, ports, environment, ...)
//
// Si el YAML no parsea o no tiene clave `services:`, devuelve error.
func injectNimOSLabelsIntoCompose(composeYAML string, labels NimOSLabels) (string, error) {
	if strings.TrimSpace(composeYAML) == "" {
		return "", fmt.Errorf("injectNimOSLabelsIntoCompose: compose vacío")
	}

	// Parseamos como Node para preservar estructura, comentarios y orden.
	// Esto es más robusto que map[string]interface{} para escritura.
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &root); err != nil {
		return "", fmt.Errorf("yaml unmarshal: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return "", fmt.Errorf("yaml root no es DocumentNode válido")
	}

	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return "", fmt.Errorf("yaml root document no es MappingNode (compose mal formado)")
	}

	// Buscar la clave 'services:' en el mapping raíz.
	servicesNode := findMappingValue(doc, "services")
	if servicesNode == nil {
		return "", fmt.Errorf("compose sin clave 'services:' (no es un compose válido)")
	}
	if servicesNode.Kind != yaml.MappingNode {
		return "", fmt.Errorf("'services:' no es un mapping (compose mal formado)")
	}

	// Recorrer cada servicio bajo services:
	labelMap := labels.ToMap()
	for i := 0; i < len(servicesNode.Content); i += 2 {
		serviceKey := servicesNode.Content[i]
		serviceVal := servicesNode.Content[i+1]

		if serviceVal.Kind != yaml.MappingNode {
			// Servicio mal formado (ej. solo nombre sin definición) · saltamos
			logMsg("docker_labels: servicio %q no es MappingNode, omitido", serviceKey.Value)
			continue
		}

		// Aplicar labels a este servicio (merge si ya existen, add si no).
		mergeLabelsIntoServiceNode(serviceVal, labelMap)
	}

	// Re-serializar a YAML.
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return "", fmt.Errorf("yaml encode: %w", err)
	}
	enc.Close()

	return buf.String(), nil
}

// findMappingValue busca una clave en un MappingNode y devuelve su nodo valor.
// Devuelve nil si no se encuentra. Las claves en YAML mapping son pares
// (key, value) consecutivos en Content[].
func findMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// mergeLabelsIntoServiceNode añade los labels NimOS al servicio.
// Si el servicio ya tiene una clave `labels:` la mergea (no la reemplaza).
// Si no la tiene, crea la clave entera.
//
// Acepta tanto formato map (labels: { key: value }) como secuencia
// (labels: [ "key=value" ]). En ambos casos preserva el formato original
// del usuario · si era map sigue siendo map, si era secuencia sigue siendo
// secuencia.
func mergeLabelsIntoServiceNode(service *yaml.Node, labelMap map[string]string) {
	existing := findMappingValue(service, "labels")

	if existing == nil {
		// No hay labels · creamos el bloque entero en formato map
		labelsNode := buildLabelsMappingNode(labelMap)
		service.Content = append(service.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "labels", Tag: "!!str"},
			labelsNode,
		)
		return
	}

	// Existen labels · mergeamos preservando el formato original
	switch existing.Kind {
	case yaml.MappingNode:
		// Formato map · añadimos los nuestros (sobrescribimos si colisión)
		for k, v := range labelMap {
			setMappingValue(existing, k, v)
		}
	case yaml.SequenceNode:
		// Formato lista ("key=value") · añadimos al final, deduplicando
		for k, v := range labelMap {
			removeSequenceItemWithPrefix(existing, k+"=")
			existing.Content = append(existing.Content, &yaml.Node{
				Kind:  yaml.ScalarNode,
				Tag:   "!!str",
				Value: k + "=" + v,
			})
		}
	default:
		// Tipo inesperado · loggeamos y omitimos (mejor no romper el deploy)
		logMsg("docker_labels: servicio con labels de tipo inesperado (kind=%d), omitido", existing.Kind)
	}
}

// buildLabelsMappingNode construye un MappingNode con los labels dados.
// Salida YAML típica:
//
//   labels:
//     com.nimos.app_id: "nextcloud"
//     com.nimos.managed: "true"
//     ...
//
// Orden alfabético para output reproducible (facilita tests deterministas).
func buildLabelsMappingNode(labels map[string]string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

	// Sort claves manualmente (evita import "sort" extra)
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}

	for _, k := range keys {
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: labels[k], Style: yaml.DoubleQuotedStyle},
		)
	}
	return node
}

// setMappingValue añade o sobrescribe un par clave/valor en un MappingNode.
func setMappingValue(mapping *yaml.Node, key, value string) {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1].Kind = yaml.ScalarNode
			mapping.Content[i+1].Tag = "!!str"
			mapping.Content[i+1].Value = value
			mapping.Content[i+1].Style = yaml.DoubleQuotedStyle
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: yaml.DoubleQuotedStyle},
	)
}

// removeSequenceItemWithPrefix elimina del SequenceNode los items cuyo Value
// empieza con el prefix dado. Usado para deduplicar al mergear labels en
// formato lista ("key=value").
func removeSequenceItemWithPrefix(seq *yaml.Node, prefix string) {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return
	}
	filtered := seq.Content[:0]
	for _, item := range seq.Content {
		if !strings.HasPrefix(item.Value, prefix) {
			filtered = append(filtered, item)
		}
	}
	seq.Content = filtered
}

// ─────────────────────────────────────────────────────────────────────────
// Consulta de containers gestionados
// ─────────────────────────────────────────────────────────────────────────

// NimOSContainer · representación mínima de un container gestionado.
// Resultado de listNimOSContainers · usado por reconciler (Fase 3).
type NimOSContainer struct {
	ID        string // ID del container Docker
	Name      string // Nombre (sin slash prefix)
	AppID     string // Valor del label com.nimos.app_id
	IsStack   bool   // Valor del label com.nimos.stack
	SchemaVer string // Valor del label com.nimos.schema_version
}

// listNimOSContainers devuelve los containers que tienen label
// com.nimos.managed=true, vivos o parados. Es la fuente de verdad para
// el reconciler Docker (Fase 3).
//
// El reconciler compara esta lista con `docker_apps` y detecta:
//   - Containers vivos sin row (huérfanos a importar)
//   - Rows sin container correspondiente (apps perdidas)
func listNimOSContainers(ctx context.Context) ([]NimOSContainer, error) {
	cctx, cancel := context.WithTimeout(ctx, dockerCmdTimeout)
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
