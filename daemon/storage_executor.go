// storage_executor.go — Interfaz BtrfsExecutor.
//
// BtrfsExecutor abstrae las operaciones físicas sobre BTRFS para que:
//   1. StorageService no dependa directamente de funciones globales
//   2. Tests unitarios usen MockBtrfsExecutor (rápidos, sin sudo)
//   3. La implementación real (storage_executor_real.go) wrappea el
//      código BTRFS existente de Beta 7 sin reescribirlo.
//
// see docs/storage_api.md §6 (BtrfsExecutor)
// see docs/storage_invariants.md#4 (No borrar a ciegas)

package main

import "context"

// BtrfsExecutor representa una capa que sabe ejecutar operaciones BTRFS.
// Cada método devuelve error o nil. La identidad de los devices se pasa
// por by-id-path (estable entre reboots, dentro del mismo hardware).
//
// NOTA: estos métodos NO escriben a la DB. Solo ejecutan operaciones
// físicas sobre el filesystem. Persistir el resultado es responsabilidad
// del StorageService (capa de orquestación).
type BtrfsExecutor interface {
	// CreateFilesystem crea un filesystem BTRFS sobre los devices dados
	// con el profile indicado y label igual al pool name.
	// Tras esto, el filesystem existe pero NO está montado.
	CreateFilesystem(ctx context.Context, req CreateFilesystemRequest) (*FilesystemInfo, error)

	// MountFilesystem monta un filesystem BTRFS en mountPoint.
	// Crea mountPoint si no existe.
	MountFilesystem(ctx context.Context, byIDPath, mountPoint string) error

	// UnmountFilesystem desmonta el filesystem. Idempotente.
	UnmountFilesystem(ctx context.Context, mountPoint string) error

	// DestroyFilesystem desmonta, hace wipefs de todos los devices,
	// y libera el mount point. Si force=false, falla si hay procesos
	// usando el mount.
	DestroyFilesystem(ctx context.Context, req DestroyFilesystemRequest) error

	// AddDevice añade un device a un pool BTRFS ya existente.
	// Tras esto, el caller probablemente quiera disparar un balance.
	AddDevice(ctx context.Context, mountPoint, byIDPath string) error

	// RemoveDevice quita un device del pool. BTRFS hace el balance
	// implícitamente. Esta operación puede tardar minutos/horas.
	RemoveDevice(ctx context.Context, mountPoint, byIDPath string) error

	// ReplaceDevice sustituye oldByIDPath por newByIDPath. Usa
	// btrfs replace start (no remove+add) que es más eficiente.
	// IMPORTANTE: oldByIDPath se hace wipefs SEGURO (no a ciegas).
	// see docs/storage_invariants.md#4.2
	ReplaceDevice(ctx context.Context, mountPoint, oldByIDPath, newByIDPath string) error

	// ConvertProfile cambia el perfil de datos/metadata del pool
	// (ej: single → raid1, raid1 → raid10). Internamente ejecuta
	// btrfs balance start con el filtro de profile. Operación pesada.
	ConvertProfile(ctx context.Context, mountPoint string, newProfile Profile) error

	// WipeDevice borra firmas de filesystem y particiones del device.
	// Verifica defensivamente que el device no es boot disk ni está
	// montado. Devuelve error si no es seguro hacerlo.
	WipeDevice(ctx context.Context, byIDPath string) error

	// GetFilesystemInfo consulta BTRFS para devolver el estado actual
	// del filesystem (UUID, devices presentes, total/used bytes).
	GetFilesystemInfo(ctx context.Context, mountPoint string) (*FilesystemInfo, error)

	// FilesystemExistsByUUID comprueba si BTRFS conoce un filesystem con
	// el UUID dado, sin necesidad de tenerlo montado. Útil en recovery
	// para verificar si un pool persiste en disco tras un crash.
	// see docs/storage_state_machines.md §4 (recovery)
	FilesystemExistsByUUID(ctx context.Context, btrfsUUID string) (bool, error)
}

// CreateFilesystemRequest es el payload de CreateFilesystem.
type CreateFilesystemRequest struct {
	Label       string   // se convierte en label del filesystem
	Profile     Profile  // single, raid1, raid1c3, raid10
	ByIDPaths   []string // /dev/disk/by-id/... uno por device
	WipeFirst   bool     // si true, hace wipefs de cada device antes de mkfs
}

// DestroyFilesystemRequest es el payload de DestroyFilesystem.
type DestroyFilesystemRequest struct {
	MountPoint string
	ByIDPaths  []string // devices a wipefs tras unmount
	Force      bool     // si true, lazy umount + wipefs aunque haya procesos
}

// FilesystemInfo es la respuesta de CreateFilesystem y GetFilesystemInfo.
type FilesystemInfo struct {
	BtrfsUUID  string
	TotalBytes int64
	UsedBytes  int64
	Devices    []FilesystemDevice
}

// FilesystemDevice es un device tal y como lo reporta BTRFS.
type FilesystemDevice struct {
	ByIDPath    string // /dev/disk/by-id/... si BTRFS lo expone así
	DevicePath  string // /dev/sdb o lo que reporte el kernel
	SizeBytes   int64
	UsedBytes   int64
	DeviceID    int // ID interno de BTRFS (1, 2, 3...)
	WriteErrors int
	ReadErrors  int
	FlushErrors int
}
