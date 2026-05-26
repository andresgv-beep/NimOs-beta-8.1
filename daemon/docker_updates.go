package main

// ═══════════════════════════════════════════════════════════════════════
// docker_updates.go · Helpers para detección de actualizaciones Docker
// ───────────────────────────────────────────────────────────────────────
// Sprint Updates · 25/05/2026
//
// Funciones para:
//   - Obtener digest LOCAL de una imagen instalada (docker image inspect)
//   - Obtener digest REMOTO desde un registry (docker manifest inspect)
//   - Detectar updates comparando ambos
//
// Estrategia:
//   - Comandos Docker con timeout (5s local, 15s remoto)
//   - Si manifest inspect falla por auth/rate-limit, lo reportamos como
//     status especial · el frontend graceful (oculta botón Actualizar)
//   - NO se descargan imágenes nuevas aquí · solo metadatos
// ═══════════════════════════════════════════════════════════════════════

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	// localInspectTimeout · `docker image inspect` debería ser instantáneo
	// (es metadata local), pero damos margen por si el daemon Docker está
	// ocupado con otra operación.
	localInspectTimeout = 5 * time.Second

	// remoteInspectTimeout · `docker manifest inspect` hace HTTPS round-trip
	// al registry (Docker Hub, ghcr.io...). 15s cubre redes lentas + DNS.
	remoteInspectTimeout = 15 * time.Second
)

// getLocalImageDigest devuelve el digest sha256 de una imagen tal y como está
// instalada en este sistema. Si la imagen no está, devuelve string vacío.
//
// Ejemplo:
//   getLocalImageDigest("jellyfin/jellyfin:latest")
//   → "sha256:abc123..." (si está) o "" (si no está descargada)
//
// El comando es:
//   docker image inspect <image> --format '{{index .RepoDigests 0}}'
//
// El output viene como "image@sha256:abc..." · extraemos la parte tras el @.
func getLocalImageDigest(ctx context.Context, image string) (string, error) {
	if image == "" {
		return "", fmt.Errorf("getLocalImageDigest: image vacío")
	}

	cctx, cancel := context.WithTimeout(ctx, localInspectTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "docker", "image", "inspect", image,
		"--format", "{{if .RepoDigests}}{{index .RepoDigests 0}}{{end}}")
	out, err := cmd.Output()
	if err != nil {
		// "No such image" es un caso esperado · imagen no descargada todavía.
		// No es un error técnico · simplemente no hay digest.
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		if strings.Contains(stderr, "No such image") || strings.Contains(stderr, "Error: No such") {
			return "", nil
		}
		return "", fmt.Errorf("docker image inspect %s: %w (stderr: %s)", image, err, stderr)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		// La imagen existe pero no tiene RepoDigests · pasa con imágenes
		// construidas localmente sin push. Sin digest, no hay tracking.
		return "", nil
	}

	// Output esperado: "ghcr.io/immich/server@sha256:abc123..."
	// Queremos solo la parte sha256:abc123...
	idx := strings.Index(raw, "@")
	if idx == -1 {
		// Formato inesperado · log y devolvemos raw como fallback
		logMsg("docker: digest local sin @ separator para %s: %q", image, raw)
		return raw, nil
	}
	return raw[idx+1:], nil
}

// manifestInspectResult representa la salida JSON parcial de
// `docker manifest inspect --verbose <image>`. Solo nos interesa el digest.
//
// Hay dos formatos posibles:
//   - Single-arch manifest: { "config": { "digest": "sha256:..." }, ... }
//   - Multi-arch manifest: array de { "Descriptor": { "digest": "sha256:..." } }
//
// Para nuestro propósito (detectar updates), basta con el digest a nivel
// manifest (no a nivel layer). Usamos `docker manifest inspect` sin --verbose
// que devuelve el manifest summary directo.
type manifestInspectResult struct {
	SchemaVersion int    `json:"schemaVersion"`
	MediaType     string `json:"mediaType"`
	Config        struct {
		Digest string `json:"digest"`
	} `json:"config"`
	// Para multi-arch · el primer manifest tiene el digest representativo
	Manifests []struct {
		Digest string `json:"digest"`
		Platform struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
		} `json:"platform"`
	} `json:"manifests"`
}

// RemoteCheckOutcome encapsula el resultado de comprobar un registry remoto.
// Incluye el status para que el caller (endpoint update-check) decida si
// guardarlo en BD como 'ok', 'unauthorized', 'rate_limited', etc.
type RemoteCheckOutcome struct {
	Digest string // sha256:... si OK, vacío si falló
	Status string // 'ok' | 'rate_limited' | 'unauthorized' | 'unsupported' | 'error'
	Err    error  // detalles del fallo · útil para logs
}

// runtimeArch devuelve la arquitectura Docker (no Go) que corresponde al
// runtime actual · 'amd64' en x86_64, 'arm64' en Raspberry Pi 4/5, etc.
// Se mapea desde runtime.GOARCH a la nomenclatura Docker.
func runtimeArch() string {
	switch runtime.GOARCH {
	case "amd64", "x86_64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	case "arm":
		return "arm"
	case "386":
		return "386"
	default:
		return runtime.GOARCH
	}
}

// getRemoteImageDigest consulta el registry remoto para obtener el digest
// actual del tag. NO descarga la imagen · solo metadatos.
//
// Ejemplos:
//   getRemoteImageDigest("jellyfin/jellyfin:latest")  → "sha256:..."
//   getRemoteImageDigest("private/repo:v1")           → "", status='unauthorized'
//   getRemoteImageDigest("typo/imagen:latest")        → "", status='unsupported'
//
// El comando es:
//   docker manifest inspect <image>
//
// IMPORTANTE · multi-arch: muchas imágenes (Jellyfin, Immich, Postgres) son
// multi-arch · el manifest inspect devuelve un INDEX con manifests por
// arquitectura. Necesitamos el digest de la arquitectura del runtime actual
// (en Pi 4 = arm64), NO el primer manifest que devuelve (que suele ser amd64).
//
// Sin este filtro, en Pi 4 comparamos:
//   local (arm64)  = sha256:abc...
//   remote (amd64) = sha256:xyz...
// Y aunque la imagen sea la misma, los digests NUNCA coinciden · falsamente
// detectamos update permanente.
//
// Necesita que el daemon Docker esté corriendo y que `experimental` esté
// habilitado en config (en Docker moderno está por defecto).
func getRemoteImageDigest(ctx context.Context, image string) RemoteCheckOutcome {
	if image == "" {
		return RemoteCheckOutcome{Status: "error", Err: fmt.Errorf("image vacío")}
	}

	cctx, cancel := context.WithTimeout(ctx, remoteInspectTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "docker", "manifest", "inspect", image)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}

		// Clasificar el error para que el frontend reaccione adecuadamente.
		// Ver lista de status en apps_schema.sql.
		switch {
		case strings.Contains(stderr, "toomanyrequests") || strings.Contains(stderr, "rate limit"):
			return RemoteCheckOutcome{Status: "rate_limited", Err: fmt.Errorf("registry rate limit: %s", stderr)}
		case strings.Contains(stderr, "unauthorized") || strings.Contains(stderr, "denied") || strings.Contains(stderr, "authentication required"):
			return RemoteCheckOutcome{Status: "unauthorized", Err: fmt.Errorf("registry requires auth: %s", stderr)}
		case strings.Contains(stderr, "manifest unknown") || strings.Contains(stderr, "not found"):
			return RemoteCheckOutcome{Status: "unsupported", Err: fmt.Errorf("image/tag not in registry: %s", stderr)}
		case cctx.Err() == context.DeadlineExceeded:
			return RemoteCheckOutcome{Status: "error", Err: fmt.Errorf("registry timeout (%s)", remoteInspectTimeout)}
		default:
			return RemoteCheckOutcome{Status: "error", Err: fmt.Errorf("docker manifest inspect %s: %w (stderr: %s)", image, err, stderr)}
		}
	}

	// Parsear output JSON
	var result manifestInspectResult
	if err := json.Unmarshal(out, &result); err != nil {
		return RemoteCheckOutcome{Status: "error", Err: fmt.Errorf("parse manifest JSON: %w", err)}
	}

	myArch := runtimeArch()

	// Caso 1 · single-arch manifest · digest en config.digest.
	// Este caso es raro hoy día · casi todas las imágenes públicas son multi-arch.
	if result.Config.Digest != "" && len(result.Manifests) == 0 {
		return RemoteCheckOutcome{Digest: result.Config.Digest, Status: "ok"}
	}

	// Caso 2 · multi-arch · buscar el manifest que coincide con la arch
	// del runtime actual. Esto es CRÍTICO en Pi (arm64) porque la imagen
	// instalada localmente es la del manifest arm64, no amd64.
	for _, m := range result.Manifests {
		if m.Platform.Architecture == myArch && m.Platform.OS == "linux" {
			return RemoteCheckOutcome{Digest: m.Digest, Status: "ok"}
		}
	}

	// Caso 3 · fallback · ninguna arch coincide pero hay manifests.
	// Esto pasa si la imagen no tiene build para nuestra arquitectura
	// (típicamente apps que solo publican amd64 y nosotros estamos en arm64,
	// el usuario habría tenido un fallo de instalación previo, pero por si
	// acaso devolvemos el primero como mejor esfuerzo).
	if len(result.Manifests) > 0 {
		logMsg("docker: image %s sin manifest para arch %s (manifests disponibles: %d) · usando el primero como fallback", image, myArch, len(result.Manifests))
		return RemoteCheckOutcome{Digest: result.Manifests[0].Digest, Status: "ok"}
	}

	return RemoteCheckOutcome{Status: "unsupported", Err: fmt.Errorf("manifest sin digest reconocible")}
}

// refreshRemoteDigestsForApp consulta el registry para todas las imágenes de
// una app y actualiza la BD con los digests obtenidos. Se llama desde el
// endpoint update-check cuando el TTL del cache ha expirado.
//
// Cada servicio se consulta en paralelo (limit razonable · típicamente 1-5
// imágenes por app, no necesita pool global). Si una falla, se registra su
// status en BD pero las otras siguen.
//
// Devuelve el número de servicios actualizados con éxito.
func refreshRemoteDigestsForApp(ctx context.Context, repo *AppImagesRepo, appID string) (int, error) {
	images, err := repo.GetByApp(ctx, appID)
	if err != nil {
		return 0, fmt.Errorf("GetByApp %s: %w", appID, err)
	}
	if len(images) == 0 {
		return 0, nil
	}

	// Canal para recoger resultados
	type result struct {
		serviceName string
		outcome     RemoteCheckOutcome
	}
	results := make(chan result, len(images))

	for _, img := range images {
		go func(img AppImage) {
			outcome := getRemoteImageDigest(ctx, img.Image)
			results <- result{serviceName: img.ServiceName, outcome: outcome}
		}(img)
	}

	updated := 0
	for i := 0; i < len(images); i++ {
		r := <-results
		if r.outcome.Err != nil {
			logMsg("docker: remote check failed for %s/%s: %v (status=%s)", appID, r.serviceName, r.outcome.Err, r.outcome.Status)
		}
		// Aun si falló, persistimos el status (sirve para que el frontend
		// sepa que esa imagen no es comprobable y oculte el botón).
		if err := repo.UpdateRemoteDigest(ctx, appID, r.serviceName, r.outcome.Digest, r.outcome.Status); err != nil {
			logMsg("docker: UpdateRemoteDigest %s/%s: %v", appID, r.serviceName, err)
			continue
		}
		if r.outcome.Status == "ok" {
			updated++
		}
	}

	return updated, nil
}
