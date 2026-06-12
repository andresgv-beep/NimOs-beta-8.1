package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// Storage Health Monitor — bucle proactivo único (Ola 1 del cierre de storage)
//
// Reemplaza el viejo checkStorageHealthGo (que SOLO miraba UsagePercent) por un
// único bucle que cada ciclo evalúa, por pool:
//
//   · FIX1 + P6 — Salud del pool vía buildPoolHealth (degraded/critical con el
//     margen por profile YA calculado por ComputePoolHealth) → notifica SOLO en
//     transiciones de estado, replicando el patrón probado del SMART monitor.
//   · P1        — Espacio sin asignar (unallocated) por device → alerta ENOSPC
//     de metadata, precursor del read-only silencioso de BTRFS.
//
// Principio rector (igual que el SMART monitor): notificar en TRANSICIONES de
// estado, no en cada lectura. Esto da dedupe natural y captura recoveries.
//
// NO cambia la detección (CollectDiagnostics/ComputePoolHealth ya detectan bien)
// — solo conecta la detección al canal de notificaciones existente.
// ─────────────────────────────────────────────────────────────────────────────

// Umbral de unallocated por device por debajo del cual se considera riesgo de
// ENOSPC de metadata. BTRFS necesita poder asignar nuevos chunks de metadata; si
// el unallocated cae cerca de cero, el pool puede pasar a read-only de golpe.
// 1 GiB da margen para avisar antes del bloqueo.
const unallocatedCriticalBytes int64 = 1 << 30 // 1 GiB

// Estado previo notificado por pool (healthy/at_risk/unstable/degraded/critical).
// Permite notificar solo en transiciones, como smartHistory en el SMART monitor.
var (
	poolHealthMu      sync.Mutex
	poolHealthHistory = map[string]string{}
	// unallocatedHistory: estado previo de espacio sin asignar por pool
	// ("ok" | "critical"). Evita spamear la alerta ENOSPC cada ciclo.
	unallocatedHistory = map[string]string{}
)

// runStorageHealthCheck ejecuta UNA pasada del bucle proactivo. Llamada cada
// ciclo desde startStorageMonitoring. También recalcula storageAlertsGo (las
// alertas de capacidad en memoria que consume el HTTP) para no perder esa vía.
func runStorageHealthCheck() {
	if storageService == nil {
		return
	}
	pools, err := storageService.ListPools(context.Background())
	if err != nil {
		return
	}

	// Mantener storageAlertsGo (capacidad %) — vía legacy que el HTTP consume.
	checkStorageHealthGo()

	for _, p := range pools {
		if p.MountPoint == "" {
			continue
		}
		// Solo evaluamos pools realmente montados; un pool desmontado lo
		// gestiona el reconciler de mount, no este bucle de salud.
		if !isPoolMounted(p.MountPoint) {
			continue
		}

		checkPoolHealthTransition(p)
		checkPoolUnallocated(p)
	}
}

// checkPoolHealthTransition computa la salud del pool con el motor existente y
// notifica solo si el status cambió respecto al último ciclo. (FIX1 + P6)
func checkPoolHealthTransition(p *Pool) {
	configDisks := make([]string, 0, len(p.Devices))
	for _, d := range p.Devices {
		name := strings.TrimPrefix(d.CurrentPath, "/dev/")
		if name != "" {
			configDisks = append(configDisks, name)
		}
	}
	if len(configDisks) == 0 {
		return
	}

	health := buildPoolHealth(DiagnosticInput{
		PoolType:    "btrfs",
		VdevType:    btrfsVdevTypeForProfile(string(p.Profile)),
		ConfigDisks: configDisks,
		MountPoint:  p.MountPoint,
	})
	current := health.Status

	poolHealthMu.Lock()
	prevStatus, existed := poolHealthHistory[p.Name]
	poolHealthHistory[p.Name] = current
	poolHealthMu.Unlock()

	if shouldNotifyHealth(prevStatus, existed, current) {
		notifyPoolHealth(p, prevStatus, current, health)
	}
}

// shouldNotifyHealth decide si una transición de estado debe notificar.
// Política (igual que el SMART monitor): notificar solo en transiciones.
//   - primera observación: notificar solo si NO nace healthy (evita ruido al boot)
//   - observaciones siguientes: notificar solo si el estado cambió
func shouldNotifyHealth(prev string, existed bool, current string) bool {
	if !existed {
		return current != "healthy"
	}
	return current != prev
}

// notifyPoolHealth emite la notificación adecuada según la transición. El mensaje
// reusa health.Reason.Message, que YA incluye el margen por profile (p.ej.
// "Sin redundancia — 1 de 2 discos" para raid1, "puede perder N discos más"
// para raid1c3/raid10).
func notifyPoolHealth(p *Pool, prev, current string, health PoolHealth) {
	reason := strings.TrimSpace(health.Reason.Message)

	switch current {
	case "critical":
		msg := reason
		if msg == "" {
			msg = "Estado crítico — datos en riesgo. Revisa la sección Salud."
		}
		addNotification("error", "system",
			fmt.Sprintf("Pool %s en estado crítico", p.Name), msg)
		logMsg("HEALTH CRITICAL: pool %s %s→critical (%s)", p.Name, prev, reason)

	case "degraded":
		msg := reason
		if msg == "" {
			msg = "Pool degradado. Revisa la sección Salud."
		}
		addNotification("error", "system",
			fmt.Sprintf("Pool %s degradado", p.Name), msg)
		logMsg("HEALTH DEGRADED: pool %s %s→degraded (%s)", p.Name, prev, reason)

	case "unstable", "at_risk":
		msg := reason
		if msg == "" {
			msg = "El pool muestra señales de inestabilidad. Revisa la sección Salud."
		}
		addNotification("warning", "system",
			fmt.Sprintf("Pool %s requiere atención", p.Name), msg)
		logMsg("HEALTH %s: pool %s %s→%s (%s)", strings.ToUpper(current), p.Name, prev, current, reason)

	case "healthy":
		// Recovery: solo notificar si veníamos de un estado malo.
		if prev != "" && prev != "healthy" {
			addNotification("success", "system",
				fmt.Sprintf("Pool %s recuperado", p.Name),
				fmt.Sprintf("El pool %s ha vuelto a estado saludable.", p.Name))
			logMsg("HEALTH OK: pool %s recovered from %s", p.Name, prev)
		}
	}
}

// checkPoolUnallocated lee el espacio sin asignar por device y alerta cuando
// cae por debajo del umbral crítico — precursor del ENOSPC de metadata. (P1)
func checkPoolUnallocated(p *Pool) {
	minUnalloc, ok := readMinUnallocated(p.MountPoint)
	if !ok {
		return // no se pudo leer; no inventamos estado
	}

	state := "ok"
	if minUnalloc < unallocatedCriticalBytes {
		state = "critical"
	}

	poolHealthMu.Lock()
	prev, existed := unallocatedHistory[p.Name]
	unallocatedHistory[p.Name] = state
	poolHealthMu.Unlock()

	// Notificar solo al entrar en estado crítico (transición ok→critical o
	// primera observación ya crítica) y al recuperar (critical→ok).
	if state == "critical" && (!existed || prev != "critical") {
		addNotification("error", "system",
			fmt.Sprintf("Pool %s: espacio sin asignar crítico", p.Name),
			fmt.Sprintf("%s: solo quedan %s sin asignar. Riesgo de bloqueo en solo-lectura (ENOSPC de metadata). Ejecuta un balance (btrfs balance -dusage=N) para recuperar chunks.",
				p.Name, humanBytes(minUnalloc)))
		logMsg("HEALTH ENOSPC: pool %s unallocated crítico (%d bytes)", p.Name, minUnalloc)
	} else if state == "ok" && existed && prev == "critical" {
		addNotification("success", "system",
			fmt.Sprintf("Pool %s: espacio sin asignar recuperado", p.Name),
			fmt.Sprintf("%s vuelve a tener margen de espacio sin asignar (%s).", p.Name, humanBytes(minUnalloc)))
		logMsg("HEALTH ENOSPC OK: pool %s unallocated recuperado (%d bytes)", p.Name, minUnalloc)
	}
}

// readMinUnallocated devuelve el MENOR "Unallocated" entre los devices del pool,
// parseando `btrfs filesystem usage -b <mp>`. El menor es el que primero
// provocará ENOSPC de metadata. Devuelve (bytes, true) si pudo leer al menos un
// device; (0, false) si el comando falló o no había líneas Unallocated.
func readMinUnallocated(mountPoint string) (int64, bool) {
	out, ok := runSafe("btrfs", "filesystem", "usage", "-b", mountPoint)
	if !ok {
		return 0, false
	}
	return parseMinUnallocated(out)
}

// parseMinUnallocated extrae el menor valor de las líneas "Unallocated:" del
// output de `btrfs filesystem usage -b`. Separado para poder testearlo sin
// ejecutar btrfs.
//
// En ese output, la sección final lista por device:
//
//	/dev/sda, ID: 1
//	   Device size:            120034123776
//	   Device slack:                      0
//	   Data,single:              5368709120
//	   Metadata,single:          1073741824
//	   System,single:              33554432
//	   Unallocated:            113558118400
//
// Hay una línea "Unallocated:" por device. Tomamos el mínimo.
func parseMinUnallocated(usageOutput string) (int64, bool) {
	var min int64 = -1
	for _, line := range strings.Split(usageOutput, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Unallocated:") {
			continue
		}
		v := parseTrailingInt(line)
		if min < 0 || v < min {
			min = v
		}
	}
	if min < 0 {
		return 0, false
	}
	return min, true
}

// humanBytes formatea bytes de forma legible para los mensajes de notificación.
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
