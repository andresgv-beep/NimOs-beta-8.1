package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════
// BTRFS QUOTA · habilitación idempotente · NimOS Beta 8.1
// ═══════════════════════════════════════════════════════════════════════
//
// CONTEXTO (hallazgo 03/06/2026): NimOS aplicaba `btrfs qgroup limit` para
// las quotas de share SIN haber habilitado nunca `btrfs quota enable` en el
// pool. En BTRFS, qgroup limit sobre un filesystem con quota deshabilitada
// FALLA ("quotas not enabled"), y el error se tragaba (runCmd sin check).
// Resultado: las quotas de share nunca se aplicaron de verdad.
//
// Este módulo arregla la base: habilita quota en los pools de forma
// idempotente, tanto al crear un pool como en un barrido al arranque que
// repara los pools existentes. Sin esto, NINGUNA quota (share o carpeta)
// funciona.
//
// Nota sobre coste: habilitar quota en un pool con datos dispara un rescan
// asíncrono de BTRFS (cuenta lo ya escrito). No bloquea ni corrompe; sólo
// consume I/O un rato. Por eso el barrido de arranque va en background.
// ═══════════════════════════════════════════════════════════════════════

// btrfsQuotaEnabled comprueba si la quota está habilitada en el pool montado
// en mountPoint. Devuelve (true, nil) si está habilitada, (false, nil) si no.
// Un error de ejecución se devuelve como tercer caso (false, err).
func btrfsQuotaEnabled(mountPoint string) (bool, error) {
	opts := CmdOptions{Timeout: 15 * time.Second}
	res, err := runCmd("btrfs", []string{"qgroup", "show", mountPoint}, opts)
	if err == nil && res.Code == 0 {
		// Listó qgroups sin error → quota habilitada.
		return true, nil
	}
	// BTRFS responde "quotas not enabled" (o variantes) en stderr cuando no
	// está habilitada. Eso NO es un fallo de ejecución, es la respuesta "no".
	combined := strings.ToLower(res.Stderr + res.Stdout)
	if strings.Contains(combined, "quota") && strings.Contains(combined, "not enabled") {
		return false, nil
	}
	// Cualquier otra cosa es un error real (path inexistente, no es btrfs...).
	if err != nil {
		return false, fmt.Errorf("btrfs qgroup show %s: %w (stderr: %s)", mountPoint, err, res.Stderr)
	}
	return false, fmt.Errorf("btrfs qgroup show %s: code=%d stderr=%s", mountPoint, res.Code, res.Stderr)
}

// ensureBtrfsQuotaEnabled habilita la quota en el pool si no lo está ya.
// Idempotente: si ya está habilitada, no hace nada y devuelve nil.
func ensureBtrfsQuotaEnabled(mountPoint string) error {
	enabled, err := btrfsQuotaEnabled(mountPoint)
	if err != nil {
		return err
	}
	if enabled {
		return nil // ya está, nada que hacer
	}

	opts := CmdOptions{Timeout: 30 * time.Second}
	res, err := runCmd("btrfs", []string{"quota", "enable", mountPoint}, opts)
	if err != nil {
		return fmt.Errorf("btrfs quota enable %s: %w (stderr: %s)", mountPoint, err, res.Stderr)
	}
	if res.Code != 0 {
		return fmt.Errorf("btrfs quota enable %s: code=%d stderr=%s", mountPoint, res.Code, res.Stderr)
	}
	logMsg("BTRFS quota habilitada en pool montado en %s", mountPoint)
	return nil
}

// enableQuotaOnAllPools recorre los pools montados y habilita quota en cada
// uno (idempotente). Diseñado para correr al arranque del daemon y reparar
// pools existentes que nunca tuvieron quota habilitada.
//
// No es fatal: un pool que falle se registra y se continúa con el resto.
func enableQuotaOnAllPools(ctx context.Context) {
	if storageService == nil {
		logMsg("enableQuotaOnAllPools: storage service no inicializado, omitido")
		return
	}

	pools, err := storageService.ListPools(ctx)
	if err != nil {
		logMsg("enableQuotaOnAllPools: ListPools ERROR (continuando): %v", err)
		return
	}

	var enabled, skipped, failed int
	for _, p := range pools {
		if p == nil || !p.Mounted || p.MountPoint == "" {
			skipped++
			continue
		}
		already, qerr := btrfsQuotaEnabled(p.MountPoint)
		if qerr != nil {
			logMsg("enableQuotaOnAllPools: pool %q: no se pudo comprobar quota: %v", p.Name, qerr)
			failed++
			continue
		}
		if already {
			skipped++
			continue
		}
		if err := ensureBtrfsQuotaEnabled(p.MountPoint); err != nil {
			logMsg("enableQuotaOnAllPools: pool %q: habilitar quota FALLÓ: %v", p.Name, err)
			failed++
			continue
		}
		enabled++
	}
	if enabled > 0 || failed > 0 {
		logMsg("enableQuotaOnAllPools: habilitadas=%d ya_activas=%d fallidas=%d", enabled, skipped, failed)
	}
}
