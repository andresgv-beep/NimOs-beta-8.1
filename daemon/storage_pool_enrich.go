// storage_pool_enrich.go — Enriquecimiento de Pool con datos runtime.
//
// Los campos Usage, Health, IsPrimary y Mounted NO se almacenan en SQLite:
// son derivados del estado físico del sistema en runtime.
//
// Esta capa los calcula y los adjunta al struct Pool para que la API HTTP
// (storage_http_v2.go) devuelva pools "completos" a la UI sin que el frontend
// tenga que pegar varios endpoints.
//
// Diseño:
//   · enrichPool() toma un Pool ya cargado del repo y le añade campos runtime
//   · Reusa helpers existentes (runSafe, buildPoolHealth, btrfsVdevTypeForProfile)
//   · No reinventa la lógica que ya estaba en getBtrfsPoolInfo
//
// Principios aplicados:
//   · Principio 1 — SQLite=entidades, JSON=payload. Estos campos son derivados.
//   · Principio 2 — runSafe con timeout donde aplica
//   · Principio 13 — Health rico, no booleano

package main

import (
	"context"
	"strings"
)

// enrichPool añade los campos derivados (Usage, Health, IsPrimary, Mounted)
// a un Pool ya hidratado con Devices.
//
// primaryPool es el nombre del pool primario actual (leído de
// storage_metadata.primary_pool en SQLite vía getPrimaryPoolName).
func enrichPool(p *Pool, primaryPool string) {
	if p == nil {
		return
	}

	p.IsPrimary = p.Name == primaryPool

	// Detect mount status + usage
	p.Mounted = isPoolMounted(p.MountPoint)
	if p.Mounted {
		p.Usage = computePoolUsage(p.MountPoint)
	}

	// Compute health using the existing diagnostic engine + enrich each
	// device with SmartStatus (runtime, from smartctl cache).
	configDisks := make([]string, 0, len(p.Devices))
	for i, d := range p.Devices {
		// getDiskStatusForBtrfs y getSmartDetailsForDisk esperan nombres
		// cortos como "sda" (sin /dev/), pero el servicio Beta 8 usa rutas
		// completas. Convertimos aquí para compatibilidad con esas funciones.
		name := d.CurrentPath
		if strings.HasPrefix(name, "/dev/") {
			name = strings.TrimPrefix(name, "/dev/")
		}
		if name == "" {
			continue
		}
		configDisks = append(configDisks, name)

		// Enriquecer Device con SmartStatus (runtime, cache de smartctl).
		// getSmartDetailsForDisk no llama smartctl: lee de smartHistory.
		smartStatus, _ := getSmartDetailsForDisk(name)
		if smartStatus != "" {
			p.Devices[i].SmartStatus = smartStatus
		}
	}

	vdevType := btrfsVdevTypeForProfile(string(p.Profile))
	health := buildPoolHealth(DiagnosticInput{
		PoolType:    "btrfs",
		VdevType:    vdevType,
		ConfigDisks: configDisks,
		MountPoint:  p.MountPoint,
	})
	p.Health = &health
}

// btrfsVdevTypeForProfile mapea el Profile BTRFS al vdevType que entiende
// buildPoolHealth (alineado con la convención de storage_health.go).
func btrfsVdevTypeForProfile(profile string) string {
	switch profile {
	case "raid1", "raid1c3":
		return "mirror"
	case "raid10":
		return "raid10"
	case "single":
		return "single"
	}
	return "single"
}

// isPoolMounted comprueba si mountPoint tiene un filesystem montado
// distinto del root (cubre el caso edge de mountPoint=/ que sería un bug).
func isPoolMounted(mountPoint string) bool {
	if mountPoint == "" || mountPoint == "/" {
		return false
	}
	mountSrc, _ := runSafe("findmnt", "-n", "-o", "SOURCE", mountPoint)
	if strings.TrimSpace(mountSrc) == "" {
		return false
	}
	rootSrc, _ := runSafe("findmnt", "-n", "-o", "SOURCE", "/")
	return strings.TrimSpace(mountSrc) != strings.TrimSpace(rootSrc)
}

// computePoolUsage calcula la capacidad usable real de un pool BTRFS
// usando `btrfs filesystem usage -b` (correcto para RAID asimétrico) con
// fallback a `df -B1` si btrfs no responde.
//
// IMPORTANTE: el cálculo en getBtrfsPoolInfo usa el mismo método.
// Aquí mantenemos la fórmula correcta: total = used + available (capacidad
// usable real, NO el tamaño bruto de los discos).
//
// Bug fix histórico (2026-05): el cálculo ingenuo "Free (estimated)" sobrestima
// en RAID1 con discos asimétricos. "Free (statfs, df)" da el valor real.
func computePoolUsage(mountPoint string) *PoolUsage {
	if mountPoint == "" {
		return nil
	}

	var used, available int64

	if bfsOut, ok := runSafe("btrfs", "filesystem", "usage", "-b", mountPoint); ok {
		for _, line := range strings.Split(bfsOut, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Used:") {
				used = parseInt64(strings.TrimSpace(strings.TrimPrefix(line, "Used:")))
			} else if strings.HasPrefix(line, "Free (statfs, df):") {
				val := strings.TrimSpace(strings.TrimPrefix(line, "Free (statfs, df):"))
				if idx := strings.Index(val, "("); idx > 0 {
					val = strings.TrimSpace(val[:idx])
				}
				available = parseInt64(val)
			}
		}
	}

	// Fallback a df si btrfs no responde
	if used == 0 && available == 0 {
		if dfOut, ok := runSafe("df", "-B1", "--output=size,used,avail", mountPoint); ok {
			lines := strings.Split(strings.TrimSpace(dfOut), "\n")
			if len(lines) >= 2 {
				parts := strings.Fields(lines[1])
				if len(parts) >= 3 {
					total := parseInt64(parts[0])
					used = parseInt64(parts[1])
					available = parseInt64(parts[2])
					_ = total // total se recalcula abajo como used+available
				}
			}
		}
	}

	if used == 0 && available == 0 {
		return nil
	}

	total := used + available
	usagePct := 0
	if total > 0 {
		usagePct = int(float64(used) / float64(total) * 100)
	}

	return &PoolUsage{
		TotalBytes:     total,
		UsedBytes:      used,
		AvailableBytes: available,
		UsagePercent:   usagePct,
	}
}

// getPrimaryPoolName devuelve el nombre del pool primario configurado.
// Beta 8.1: lee storage_metadata directamente (sin pasar por el adapter
// de Beta 7, que fue eliminado). El valor se almacena como UUID y se
// resuelve a nombre vía repo.GetPool.
func getPrimaryPoolName() string {
	if storageService == nil {
		return ""
	}
	ctx := context.Background()
	var primaryID string
	err := storageService.repo.db.QueryRowContext(ctx,
		`SELECT value FROM storage_metadata WHERE key = 'primary_pool'`).Scan(&primaryID)
	if err != nil || primaryID == "" {
		return ""
	}
	pool, err := storageService.repo.GetPool(ctx, primaryID)
	if err != nil || pool == nil {
		return ""
	}
	return pool.Name
}
