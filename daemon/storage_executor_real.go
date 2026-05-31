// storage_executor_real.go — Implementación real de BtrfsExecutor.
//
// Ejecuta comandos `btrfs`, `mkfs.btrfs`, `mount`, `umount`, `wipefs`
// directamente. NO escribe a SQLite (eso es responsabilidad del Service).
// NO toca el filesystem JSON viejo (eso muere en Fase 5).
//
// La lógica BTRFS que tiene Beta 7 en storage_btrfs_pool.go etc. se va
// a REIMPLEMENTAR aquí — con menos dependencias y más limpio — porque
// las funciones viejas mezclan BTRFS con JSON, mount entries, etc.
//
// Para los tests unitarios se usa MockBtrfsExecutor. Para tests de
// integración (Bloque 5) y producción real, se usa esta implementación.

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RealBtrfsExecutor implementa BtrfsExecutor invocando comandos reales.
type RealBtrfsExecutor struct {
	// Timeout para comandos largos (mkfs, balance). Default 30 min.
	CmdTimeout time.Duration
}

// NewRealBtrfsExecutor crea el executor con valores razonables.
func NewRealBtrfsExecutor() *RealBtrfsExecutor {
	return &RealBtrfsExecutor{
		CmdTimeout: 30 * time.Minute,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helper común: ejecutar comando con timeout y log
// ─────────────────────────────────────────────────────────────────────────────

// runCommand ejecuta un comando y devuelve stdout o error. Logea el
// comando antes de ejecutar y el resultado después.
func (e *RealBtrfsExecutor) runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, e.CmdTimeout)
	defer cancel()

	logMsg("BtrfsExecutor: %s %s", name, strings.Join(args, " "))

	cmd := exec.CommandContext(cmdCtx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s %s: %v: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CreateFilesystem
// ─────────────────────────────────────────────────────────────────────────────

func (e *RealBtrfsExecutor) CreateFilesystem(ctx context.Context, req CreateFilesystemRequest) (*FilesystemInfo, error) {
	if len(req.ByIDPaths) == 0 {
		return nil, fmt.Errorf("CreateFilesystem: no devices provided")
	}
	if !req.Profile.IsValid() {
		return nil, fmt.Errorf("CreateFilesystem: invalid profile %q", req.Profile)
	}
	if len(req.ByIDPaths) < req.Profile.MinDisks() {
		return nil, fmt.Errorf("CreateFilesystem: profile %s requires at least %d disks, got %d",
			req.Profile, req.Profile.MinDisks(), len(req.ByIDPaths))
	}

	// Wipe defensivo si se pide
	if req.WipeFirst {
		for _, p := range req.ByIDPaths {
			if err := e.WipeDevice(ctx, p); err != nil {
				return nil, fmt.Errorf("CreateFilesystem: wipe %s: %w", p, err)
			}
		}
	}

	// Construir args de mkfs.btrfs
	// HARD-5 fix: -f (force) solo si WipeFirst=true. Sin -f, mkfs falla
	// limpio si encuentra un filesystem existente, lo cual actúa como
	// last line of defense del kernel contra races entre preflight check
	// y el mkfs real.
	args := []string{"-L", req.Label}
	if req.WipeFirst {
		args = append([]string{"-f"}, args...)
	}
	switch req.Profile {
	case ProfileSingle:
		// single: si hay >1 disco, metadata raid1 igual (BTRFS default sano)
		if len(req.ByIDPaths) > 1 {
			args = append(args, "-d", "single", "-m", "raid1")
		}
	case ProfileRaid1:
		args = append(args, "-d", "raid1", "-m", "raid1")
	case ProfileRaid1c3:
		args = append(args, "-d", "raid1c3", "-m", "raid1c3")
	case ProfileRaid10:
		args = append(args, "-d", "raid10", "-m", "raid10")
	}
	args = append(args, req.ByIDPaths...)

	// btrfs device scan --forget para limpiar referencias previas del kernel
	_, _ = e.runCommand(ctx, "btrfs", "device", "scan", "--forget")

	// mkfs.btrfs con retry simple (kernel a veces ve los devices ocupados)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		_, err := e.runCommand(ctx, "mkfs.btrfs", args...)
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		if !strings.Contains(err.Error(), "Device or resource busy") {
			break
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("mkfs.btrfs failed: %w", lastErr)
	}

	// Obtener UUID del filesystem creado (sobre el primer device)
	uuidOut, err := e.runCommand(ctx, "blkid", "-s", "UUID", "-o", "value", req.ByIDPaths[0])
	if err != nil {
		return nil, fmt.Errorf("CreateFilesystem: cannot read UUID: %w", err)
	}
	uuid := strings.TrimSpace(uuidOut)
	if uuid == "" {
		return nil, fmt.Errorf("CreateFilesystem: blkid returned empty UUID")
	}

	devices := make([]FilesystemDevice, len(req.ByIDPaths))
	for i, p := range req.ByIDPaths {
		devices[i] = FilesystemDevice{
			ByIDPath: p,
			DeviceID: i + 1,
		}
	}

	return &FilesystemInfo{
		BtrfsUUID: uuid,
		Devices:   devices,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// MountFilesystem / UnmountFilesystem
// ─────────────────────────────────────────────────────────────────────────────

func (e *RealBtrfsExecutor) MountFilesystem(ctx context.Context, byIDPath, mountPoint string) error {
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		return fmt.Errorf("MountFilesystem: cannot create %s: %w", mountPoint, err)
	}

	_, err := e.runCommand(ctx, "mount", "-t", "btrfs", byIDPath, mountPoint)
	if err != nil {
		return fmt.Errorf("MountFilesystem: %w", err)
	}
	return nil
}

func (e *RealBtrfsExecutor) UnmountFilesystem(ctx context.Context, mountPoint string) error {
	// Si no está montado, no es error
	_, err := os.Stat(mountPoint)
	if os.IsNotExist(err) {
		return nil
	}

	out, err := e.runCommand(ctx, "umount", mountPoint)
	if err != nil {
		// Si dice "not mounted" no es error
		if strings.Contains(out, "not mounted") || strings.Contains(err.Error(), "not mounted") {
			return nil
		}
		return fmt.Errorf("UnmountFilesystem %s: %w", mountPoint, err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// DestroyFilesystem
// ─────────────────────────────────────────────────────────────────────────────

func (e *RealBtrfsExecutor) DestroyFilesystem(ctx context.Context, req DestroyFilesystemRequest) error {
	// 1. Unmount
	if req.Force {
		_, _ = e.runCommand(ctx, "umount", "-l", req.MountPoint)
	} else {
		if err := e.UnmountFilesystem(ctx, req.MountPoint); err != nil {
			return fmt.Errorf("DestroyFilesystem: unmount: %w", err)
		}
	}

	// 2. Wipe each device
	for _, p := range req.ByIDPaths {
		if err := e.WipeDevice(ctx, p); err != nil {
			// No abortar — intentar limpiar todos los devices
			logMsg("DestroyFilesystem: wipe %s failed: %v", p, err)
		}
	}

	// 3. Remove mount point if empty and under /nimos/pools
	if strings.HasPrefix(req.MountPoint, "/nimos/pools/") {
		_ = os.Remove(req.MountPoint)
	}

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// AddDevice / RemoveDevice / ReplaceDevice
// ─────────────────────────────────────────────────────────────────────────────

func (e *RealBtrfsExecutor) AddDevice(ctx context.Context, mountPoint, byIDPath string) error {
	_, err := e.runCommand(ctx, "btrfs", "device", "add", byIDPath, mountPoint)
	if err != nil {
		return fmt.Errorf("AddDevice: %w", err)
	}
	return nil
}

func (e *RealBtrfsExecutor) RemoveDevice(ctx context.Context, mountPoint, byIDPath string) error {
	_, err := e.runCommand(ctx, "btrfs", "device", "remove", byIDPath, mountPoint)
	if err != nil {
		return fmt.Errorf("RemoveDevice: %w", err)
	}
	return nil
}

func (e *RealBtrfsExecutor) ReplaceDevice(ctx context.Context, mountPoint, oldByIDPath, newByIDPath string) error {
	// btrfs replace start <old> <new> <mountpoint>
	// El nuevo device se sincroniza desde los demás miembros del pool.
	// Tras esto el old queda fuera del pool y se le hace wipefs SEGURO.
	_, err := e.runCommand(ctx, "btrfs", "replace", "start", "-f", oldByIDPath, newByIDPath, mountPoint)
	if err != nil {
		return fmt.Errorf("ReplaceDevice: replace start: %w", err)
	}

	// Wipefs SEGURO del old (NO a ciegas — usa nuestro WipeDevice con guards)
	// see docs/storage_invariants.md#4
	if err := e.WipeDevice(ctx, oldByIDPath); err != nil {
		// Esto no debe abortar el replace (el filesystem ya tiene el new),
		// pero loggeamos.
		logMsg("ReplaceDevice: warning, wipe of old device %s failed: %v", oldByIDPath, err)
	}
	return nil
}

// ConvertProfile cambia el profile de un pool ejecutando btrfs balance
// con filtros de profile. El comando bloquea hasta que termina (operación
// pesada que puede tardar minutos/horas).
func (e *RealBtrfsExecutor) ConvertProfile(ctx context.Context, mountPoint string, newProfile Profile) error {
	if !newProfile.IsValid() {
		return fmt.Errorf("ConvertProfile: invalid profile %q", newProfile)
	}

	profileStr := string(newProfile)
	// btrfs balance start -dconvert=raid1 -mconvert=raid1 <mountpoint>
	// Convertimos data Y metadata para mantener consistencia.
	args := []string{
		"balance", "start",
		"-dconvert=" + profileStr,
		"-mconvert=" + profileStr,
		"--full-balance",
		mountPoint,
	}

	_, err := e.runCommand(ctx, "btrfs", args...)
	if err != nil {
		return fmt.Errorf("ConvertProfile: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// WipeDevice — con guards defensivos
// ─────────────────────────────────────────────────────────────────────────────

// WipeDevice borra firmas del device. Implementa los guards documentados
// en storage_invariants.md#4.2:
//   1. Verifica que el device no es el boot disk
//   2. Verifica que no está montado
// Solo entonces hace wipefs.
func (e *RealBtrfsExecutor) WipeDevice(ctx context.Context, byIDPath string) error {
	if byIDPath == "" {
		return fmt.Errorf("WipeDevice: empty path")
	}

	// Resolver el real path para los checks
	realPath, err := filepath.EvalSymlinks(byIDPath)
	if err != nil {
		// Si no se puede resolver el symlink, no podemos verificar nada,
		// rechazar por seguridad.
		return fmt.Errorf("WipeDevice: cannot resolve %s: %w", byIDPath, err)
	}

	// Guard 1: ¿es el boot disk?
	if isBootDisk(realPath) {
		return fmt.Errorf("WipeDevice: refusing to wipe boot disk %s (resolved from %s)",
			realPath, byIDPath)
	}

	// Guard 2: ¿está montado?
	if isDeviceMounted(realPath) {
		return fmt.Errorf("WipeDevice: refusing to wipe %s, currently mounted", realPath)
	}

	// Solo entonces, wipefs
	_, err = e.runCommand(ctx, "wipefs", "-a", byIDPath)
	if err != nil {
		return fmt.Errorf("WipeDevice: %w", err)
	}
	return nil
}

// isBootDisk devuelve true si el device es el disco de boot del sistema.
// Lo determina viendo qué device contiene la partición montada en "/".
//
// Soporta tanto particiones tradicionales (sda1 → sda) como NVMe
// (nvme0n1p2 → nvme0n1).
func isBootDisk(realPath string) bool {
	// Leer /proc/mounts y encontrar lo que está montado en "/"
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		// Por seguridad, si no podemos leer mounts, asumir que sí
		return true
	}

	rootDevice := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "/" {
			rootDevice = fields[0]
			break
		}
	}
	if rootDevice == "" {
		// No encontramos root mount, asumir peor caso
		return true
	}

	// Resolver symlinks del root device
	rootReal, err := filepath.EvalSymlinks(rootDevice)
	if err != nil {
		return true
	}

	// Si el realPath es la partición del root, es boot.
	if rootReal == realPath {
		return true
	}
	// Si el realPath es el disco padre de la partición root, también es boot.
	parent := parentDeviceOf(rootReal)
	return parent == realPath
}

// parentDeviceOf devuelve el disco padre de una partición.
//
// Convenciones del kernel Linux:
//   - sd*, vd*, hd*: la partición es <disk><N>  (sda1 → sda)
//   - nvme*:         la partición es <disk>p<N> (nvme0n1p2 → nvme0n1)
//   - mmc*:          la partición es <disk>p<N> (mmcblk0p1 → mmcblk0)
//
// Si el path no parece una partición, devuelve el path original.
func parentDeviceOf(devicePath string) string {
	base := filepath.Base(devicePath)
	dir := filepath.Dir(devicePath)

	// NVMe / MMC: <disk>p<N> donde <disk> también contiene dígitos
	// (nvme0n1, mmcblk0). El separador "p" es el indicador.
	if strings.HasPrefix(base, "nvme") || strings.HasPrefix(base, "mmcblk") {
		// Buscar el último "p" seguido SOLO de dígitos al final
		idx := strings.LastIndex(base, "p")
		if idx > 0 {
			suffix := base[idx+1:]
			if suffix != "" && allDigits(suffix) {
				return filepath.Join(dir, base[:idx])
			}
		}
		// No tiene "pN" al final: es el disco entero, no una partición
		return devicePath
	}

	// sd*, vd*, hd*: stripping de dígitos finales
	trimmed := strings.TrimRight(base, "0123456789")
	if trimmed == base {
		// No tenía dígitos al final → ya es el disco, no una partición
		return devicePath
	}
	return filepath.Join(dir, trimmed)
}

// allDigits devuelve true si s no está vacía y solo contiene dígitos.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isDeviceMounted devuelve true si el device está montado en alguna parte.
func isDeviceMounted(realPath string) bool {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		// Por seguridad, si no podemos saber, asumir que sí
		return true
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 1 {
			mounted, err := filepath.EvalSymlinks(fields[0])
			if err == nil && mounted == realPath {
				return true
			}
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// GetFilesystemInfo
// ─────────────────────────────────────────────────────────────────────────────

func (e *RealBtrfsExecutor) GetFilesystemInfo(ctx context.Context, mountPoint string) (*FilesystemInfo, error) {
	// Beta 8 nota: implementación completa pendiente de Bloque 3 cuando
	// se conecte al scan real. Aquí dejamos el stub básico para que el
	// service pueda llamarlo sin crashear.
	// see docs/nimos_beta8_storage_plan.md fase 3 día 6
	return &FilesystemInfo{}, nil
}

// FilesystemExistsByUUID consulta `btrfs filesystem show` para ver si
// el kernel conoce un filesystem con el UUID dado. No requiere que esté
// montado.
//
// Comando: btrfs filesystem show <uuid>
// - exit 0: existe → devolvemos true
// - exit != 0: no existe (o no se pudo determinar) → false
//
// Si btrfs filesystem show falla por motivos distintos a "no existe"
// (kernel sin btrfs, permisos), devolvemos error explícito en lugar de
// false silencioso. El caller (recovery) debe decidir qué hacer ante
// incertidumbre.
func (e *RealBtrfsExecutor) FilesystemExistsByUUID(ctx context.Context, btrfsUUID string) (bool, error) {
	if btrfsUUID == "" {
		return false, fmt.Errorf("FilesystemExistsByUUID: empty UUID")
	}

	// Antes de consultar, hacemos un device scan para que el kernel
	// conozca los filesystems disponibles aunque no estén montados.
	_, _ = e.runCommand(ctx, "btrfs", "device", "scan")

	cmd := exec.CommandContext(ctx, "btrfs", "filesystem", "show", btrfsUUID)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}

	// Exit code != 0 con mensaje "No filesystem found" → no existe
	outStr := strings.ToLower(string(output))
	if strings.Contains(outStr, "no filesystem found") ||
		strings.Contains(outStr, "not a btrfs filesystem") {
		return false, nil
	}

	// Cualquier otro error: propagamos. El caller decide.
	return false, fmt.Errorf("FilesystemExistsByUUID: %v: %s", err, strings.TrimSpace(string(output)))
}
