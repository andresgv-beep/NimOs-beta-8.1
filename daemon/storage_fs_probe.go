package main

// storage_fs_probe.go — Probe genérico para filesystems no-BTRFS.
//
// Beta 8.2 (DEUDA-ARQUI-OBSERVABLE-ENTITY): detecta ext4, ntfs, fat32,
// xfs, exfat usando blkid + lsblk + /proc/self/mounts.
//
// BTRFS sigue siendo manejado por storage_btrfs_probe.go (que tiene su
// propia lógica multi-device, profile, balance, etc.). Este archivo se
// centra en filesystems de un solo dispositivo.
//
// Diseño:
//   · probeForeignFilesystems() devuelve []ObservedFilesystem
//   · Usa blkid -o export para parsear UUID/TYPE/LABEL
//   · Cruza con /proc/self/mounts para mount point
//   · Cruza con statfs(2) para capacidad si está montado
//   · Excluye particiones del sistema (root, /boot, /home si está separado)

import (
	"os"
	"strings"
	"syscall"
	"time"
)

// Tipos de filesystem que NimOS Beta 8.2 entiende como pools managed.
// blkid puede reportar otros (ntfs-3g, vfat...). Normalizamos a estos.
var supportedForeignFS = map[string]FSType{
	"ext4":   FSTypeExt4,
	"ext3":   FSTypeExt4, // ext3 se trata como ext4 (ext4 lo monta nativamente)
	"ntfs":   FSTypeNTFS,
	"ntfs3":  FSTypeNTFS, // kernel >= 5.15
	"vfat":   FSTypeFAT32,
	"fat32":  FSTypeFAT32,
	"fat":    FSTypeFAT32,
	"msdos":  FSTypeFAT32,
	"xfs":    FSTypeXFS,
	"exfat":  FSTypeExFAT,
}

// systemMountPoints son rutas que NUNCA se reportan como pools observables.
// Aunque tengan ext4 / etc., son partes del sistema operativo.
var systemMountPoints = map[string]bool{
	"/":            true,
	"/boot":        true,
	"/boot/efi":    true,
	"/home":        true, // si está en partición separada, también es sistema
	"/var":         true,
	"/var/lib":     true,
	"/tmp":         true,
	"/usr":         true,
	"/opt":         true,
	"/proc":        true,
	"/sys":         true,
	"/run":         true,
	"/dev":         true,
}

// probeForeignFilesystems escanea el sistema buscando filesystems no-BTRFS
// (ext4, ntfs, fat32, xfs, exfat) en discos físicos.
//
// Devuelve la lista de filesystems detectados (vacía si no hay) y un bool
// que indica si el probe pudo ejecutarse (true incluso si la lista es vacía).
//
// El probe es robusto:
//   · Excluye particiones del sistema (root, boot, etc.)
//   · No falla si blkid devuelve algunas líneas sin parsear
//   · Tolera la ausencia de comandos opcionales (lsblk siempre presente)
func probeForeignFilesystems() ([]ObservedFilesystem, bool) {
	// 1. blkid escupe todas las particiones con metadata
	//    Output formato 'export': TYPE=ext4\nUUID=...\nLABEL=...\nDEVNAME=/dev/sdb1\n
	out, ok := runSafe("blkid", "-o", "full")
	if !ok {
		return nil, false
	}

	// 2. Construir mount map (path → mountpoint) leyendo /proc/self/mounts
	mountMap := buildMountMap()

	// 3. Parsear cada línea de blkid
	results := []ObservedFilesystem{}
	now := time.Now().UTC()

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fs := parseBlkidLine(line)
		if fs == nil {
			continue
		}

		// Filtrar tipos no soportados (btrfs, swap, zfs_member, LVM2_member, ...)
		fsTypeNorm, ok := supportedForeignFS[strings.ToLower(fs.fsType)]
		if !ok {
			continue
		}

		// Filtrar particiones del sistema
		mp := mountMap[fs.devicePath]
		if mp != "" && systemMountPoints[mp] {
			continue
		}

		// Construir ObservedFilesystem
		obsFS := ObservedFilesystem{
			UUID:   fs.uuid,
			Label:  fs.label,
			FSType: string(fsTypeNorm),
			Device: ObservedDevice{
				Path: fs.devicePath,
			},
			IsMounted:        mp != "",
			MountPoint:       mp,
			CanMount:         true,
			CanWrite:         fsCanWrite(fsTypeNorm),
			CanImportManaged: true,
			HasUnixPerms:     fsTypeNorm.SupportsUnixPerms(),
			LastSeen:         now,
		}

		// Si está montado, obtener capacidad real via statfs
		if obsFS.IsMounted {
			if stat, err := statfsBytes(obsFS.MountPoint); err == nil {
				obsFS.SizeBytes = stat.total
				obsFS.UsedBytes = stat.used
				obsFS.FreeBytes = stat.free
			}
		}

		// Hidratar device info (size, model, serial) si tenemos lsblk a mano
		hydrateDeviceInfo(&obsFS.Device)

		results = append(results, obsFS)
	}

	return results, true
}

// blkidLine es lo que extraemos de cada línea de `blkid -o full`.
// Ejemplo de línea: /dev/sdb1: UUID="abc-123" TYPE="ext4" LABEL="data"
type blkidLine struct {
	devicePath string
	uuid       string
	label      string
	fsType     string
}

func parseBlkidLine(line string) *blkidLine {
	// Format: /dev/X: KEY="value" KEY="value" ...
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return nil
	}
	devicePath := strings.TrimSpace(line[:idx])
	if !strings.HasPrefix(devicePath, "/dev/") {
		return nil
	}

	fs := &blkidLine{devicePath: devicePath}
	rest := line[idx+1:]

	// Parsear KEY="value" pairs
	for _, kv := range splitKVPairs(rest) {
		eq := strings.Index(kv, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(kv[:eq])
		val := strings.TrimSpace(kv[eq+1:])
		val = strings.Trim(val, `"`)

		switch key {
		case "UUID":
			fs.uuid = val
		case "TYPE":
			fs.fsType = val
		case "LABEL":
			fs.label = val
		}
	}

	if fs.uuid == "" || fs.fsType == "" {
		return nil
	}
	return fs
}

// splitKVPairs separa una línea como `UUID="abc" TYPE="ext4"` en pairs
// respetando comillas (los valores pueden contener espacios).
func splitKVPairs(s string) []string {
	pairs := []string{}
	var current strings.Builder
	inQuotes := false

	for _, r := range s {
		switch r {
		case '"':
			inQuotes = !inQuotes
			current.WriteRune(r)
		case ' ', '\t':
			if inQuotes {
				current.WriteRune(r)
			} else {
				if current.Len() > 0 {
					pairs = append(pairs, current.String())
					current.Reset()
				}
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		pairs = append(pairs, current.String())
	}
	return pairs
}

// buildMountMap lee /proc/self/mounts y devuelve un mapa device→mountpoint.
// Solo incluye filesystems "reales" (no proc, sysfs, tmpfs, etc.).
func buildMountMap() map[string]string {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return map[string]string{}
	}
	result := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		source := fields[0]
		target := fields[1]
		fsType := fields[2]

		// Filtrar pseudo-filesystems
		switch fsType {
		case "proc", "sysfs", "tmpfs", "devtmpfs", "devpts", "cgroup",
			"cgroup2", "pstore", "bpf", "tracefs", "debugfs", "fusectl",
			"configfs", "securityfs", "mqueue", "hugetlbfs", "autofs",
			"binfmt_misc", "rpc_pipefs", "nsfs":
			continue
		}
		// Solo entradas con device path /dev/...
		if !strings.HasPrefix(source, "/dev/") {
			continue
		}
		result[source] = target
	}
	return result
}

// statfsResult tiene el resultado de un syscall statfs en bytes.
type statfsResult struct {
	total int64
	used  int64
	free  int64
}

// statfsBytes envuelve syscall.Statfs y devuelve bytes en lugar de bloques.
func statfsBytes(path string) (statfsResult, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return statfsResult{}, err
	}
	// Bsize puede ser int64 o uint32 según OS, hacemos cast explícito
	bsize := int64(stat.Bsize)
	total := int64(stat.Blocks) * bsize
	free := int64(stat.Bavail) * bsize
	return statfsResult{
		total: total,
		free:  free,
		used:  total - free,
	}, nil
}

// fsCanWrite devuelve true si NimOS puede escribir en este FS por defecto.
// NTFS requiere ntfs-3g, que si no está instalado solo permite read-only.
// El daemon detecta esto al hacer mount real, aquí devolvemos true como
// hint optimista (la operación real validará).
func fsCanWrite(t FSType) bool {
	// Todos los FS soportados son writable en principio. Si el sistema no
	// tiene ntfs-3g, NimOS lo detectará al intentar mount y reportará error.
	return true
}

// hydrateDeviceInfo rellena Model, Serial, SizeBytes del device usando lsblk.
// Best-effort: si lsblk falla, deja los campos vacíos sin error.
func hydrateDeviceInfo(d *ObservedDevice) {
	if d.Path == "" {
		return
	}
	out, ok := runSafe("lsblk", "-n", "-b", "-o", "SIZE,MODEL,SERIAL", d.Path)
	if !ok {
		return
	}
	// Output suele ser una línea: "1000204886016 SAMSUNG_SSD ABCDEF"
	line := strings.TrimSpace(out)
	if line == "" {
		return
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return
	}
	// Size (primer campo)
	if n := parseInt64(fields[0]); n > 0 {
		d.SizeBytes = n
	}
	// Model (resto excepto último) y Serial (último)
	if len(fields) >= 2 {
		d.Model = strings.Join(fields[1:len(fields)-1], " ")
		d.Serial = fields[len(fields)-1]
		if d.Model == "" {
			// Solo SIZE + 1 campo más → trata ese campo como Model
			d.Model = fields[1]
		}
	}
}
