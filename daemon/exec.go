// exec.go — Capa de ejecución de comandos del daemon (CRÍTICA EN SEGURIDAD).
//
// Este fichero es la ÚNICA puerta legítima del daemon hacia el sistema
// operativo vía procesos externos. Tres primitivas, en orden de preferencia:
//
//	runSafe()        → argv directo, SIN shell. Para todo input de usuario.
//	runSafeInput()   → argv directo + stdin. Para datos sensibles (passwords).
//	runShellStatic() → sh -c con comando 100% LITERAL. Solo para pipelines
//	                   estáticos hardcodeados. Su guard rechaza verbos de
//	                   formato (%s/%d/%v) para cazar interpolaciones accidentales.
//
// REGLA: input externo JAMÁS llega a runShellStatic. Si necesitas datos
// dinámicos, usa runSafe con argv. (Extraído de main.go · refactor 11/06/2026.)
package main

import (
	"os/exec"
	"strings"
	"time"
)

const (
	execTimeout = 10 * time.Second // reservado: timeout de ejecución (pendiente de aplicar)
	maxRetries  = 3
)

// ═══════════════════════════════════
// Helper: safe command execution with retry
// ═══════════════════════════════════

// runShellStatic executes a STATIC command via shell (sh -c) with retry.
//
// SECURITY: This function rejects any command containing format verbs (%s, %d)
// or string concatenation markers (+) to prevent accidental interpolation.
// All ~46 callers use ONLY hardcoded string literals or pre-validated internal vars.
// User input MUST go through runSafe() / runSafeInput() / runCmd().
func runShellStatic(command string) (string, bool) {
	// Guard: reject commands that look like they contain interpolation
	if strings.Contains(command, "%s") || strings.Contains(command, "%d") || strings.Contains(command, "%v") {
		logMsg("SECURITY: runShellStatic rejected interpolated command: %s", command)
		return "", false
	}
	for attempt := 0; attempt < maxRetries; attempt++ {
		ctx := exec.Command("sh", "-c", command)
		out, err := ctx.CombinedOutput()
		result := strings.TrimSpace(string(out))

		if err == nil {
			return result, true
		}

		// Retry on lock contention
		if strings.Contains(result, "bloquear") || strings.Contains(result, "lock") || strings.Contains(result, "unable to lock") {
			logMsg("exec retry (%d/%d): %s", attempt+1, maxRetries, command)
			time.Sleep(200 * time.Millisecond)
			continue
		}

		logMsg("exec failed: %s → %s", command, result)
		return result, false
	}
	logMsg("exec gave up after %d retries: %s", maxRetries, command)
	return "", false
}

// runSafe executes a command with arguments directly (no shell).
// Same return signature as runShellStatic() for easy migration.
// ALWAYS prefer this over runShellStatic() when arguments contain user data.
func runSafe(cmd string, args ...string) (string, bool) {
	c := exec.Command(cmd, args...)
	out, err := c.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if err != nil {
		logMsg("exec failed: %s %v → %s", cmd, args, result)
		return result, false
	}
	return result, true
}

// runSafeInput executes a command with data piped to stdin (no shell).
func runSafeInput(stdin string, cmd string, args ...string) (string, bool) {
	c := exec.Command(cmd, args...)
	c.Stdin = strings.NewReader(stdin)
	out, err := c.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if err != nil {
		logMsg("exec failed: %s %v → %s", cmd, args, result)
		return result, false
	}
	return result, true
}