// storage_service.go — Capa de orquestación del módulo storage (Beta 8).
//
// StorageService es la ÚNICA capa que ejecuta operaciones. Coordina:
//   - StorageRepo (persistencia en SQLite)
//   - PolicyChecker (validación de permisos)
//   - BtrfsExecutor (operaciones reales sobre BTRFS) ← futuro Bloque 2+
//
// Patrón de cada método:
//   1. Verificar policy (¿el caller puede hacer esto?)
//   2. Crear Operation en DB con status pending/in_progress
//   3. Ejecutar la acción física (BTRFS o metadata-only)
//   4. Persistir resultado y marcar Operation completed/failed
//   5. Devolver la Operation al caller
//
// Todo dentro de transacción SQLite cuando toca múltiples tablas.
//
// see docs/storage_invariants.md
// see docs/storage_api.md §4 para firmas completas

package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
)

// ═════════════════════════════════════════════════════════════════════════════
// StorageService
// ═════════════════════════════════════════════════════════════════════════════

// StorageService es la capa de orquestación. Recibe dependencias por
// constructor para facilitar tests con mocks.
type StorageService struct {
	repo    *StorageRepo
	policy  *PolicyChecker
	btrfs   BtrfsExecutor
	scanner DeviceScanner
	clock   Clock
	db      *sql.DB // necesario para iniciar transacciones

	// deviceChecker es el preflight check ejecutado en CreatePool para
	// detectar storage preexistente en los discos antes de crear pool nuevo.
	// Inyectable para que los tests puedan saltar la validación de
	// existencia física en /dev (que falla con devices mockeados).
	//
	// Si es nil, se usa realDevicePreFlightCheck por defecto.
	//
	// DEUDA-ARQUI-OBSERVABLE-ENTITY (Beta 9): este es el punto de
	// extensión natural para soportar más kinds de storage observable
	// (ext4, mdraid, LUKS, ZFS, NTFS USB...). Hoy solo BTRFS.
	deviceChecker DeviceChecker
}

// DeviceChecker es el contrato del preflight check sobre devices.
// Toma una lista de devices resueltos y devuelve:
//   nil                     si todos están limpios y disponibles
//   *ErrDiskHasFilesystem   si alguno tiene storage detectable (con detalles)
//   error genérico          otros fallos (boot disk, holders, missing)
type DeviceChecker func(devices []*Device) error

// realDevicePreFlightCheck es el DeviceChecker de producción. Invoca
// preFlightCheck (storage_wipe.go) sobre cada device, que a su vez
// consulta el observer BTRFS y hace fallback a blkid si no está.
//
// Tests inyectan un noop o un mock vía service.deviceChecker = ...
func realDevicePreFlightCheck(devices []*Device) error {
	for _, d := range devices {
		diskPath := d.CurrentPath
		if diskPath == "" {
			diskPath = d.ByIDPath
		}
		if err := preFlightCheck(diskPath); err != nil {
			return err
		}
	}
	return nil
}

// NewStorageService crea el servicio con sus dependencias inyectadas.
func NewStorageService(db *sql.DB, repo *StorageRepo, policy *PolicyChecker,
	btrfs BtrfsExecutor, scanner DeviceScanner) *StorageService {
	return &StorageService{
		repo:    repo,
		policy:  policy,
		btrfs:   btrfs,
		scanner: scanner,
		clock:   NewRealClock(),
		db:      db,
	}
}

// SetClock inyecta un Clock personalizado. Solo para tests (FakeClock).
// En producción, se usa el RealClock por defecto.
func (s *StorageService) SetClock(c Clock) {
	s.clock = c
}

// Instancia global, conveniente para código que aún no usa inyección.
var storageService *StorageService

// initStorageService crea la instancia global. Llamar tras
// initStorageRepo() y initStoragePolicy().
func initStorageService() {
	executor := NewRealBtrfsExecutor()
	scanner := NewLsblkDeviceScanner()
	storageService = NewStorageService(db, storageRepo, storagePolicy, executor, scanner)
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers compartidos para los métodos del service
// ─────────────────────────────────────────────────────────────────────────────

// runInTx ejecuta fn dentro de una transacción. Si fn devuelve error,
// hace rollback automático. Si fn devuelve nil, hace commit.
//
// Centralizar este patrón evita repetir BeginTx/defer Rollback/Commit
// en cada método del service.
func (s *StorageService) runInTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("BeginTx: %w", err)
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// checkPolicy es helper que valida y devuelve error semántico si no permite.
// Centraliza el patrón "policy.Allows + error con código".
func (s *StorageService) checkPolicy(pool *Pool, op OperationType) error {
	allowed, code := s.policy.AllowsWithReason(pool, op)
	if !allowed {
		return &ServiceError{
			Code: code,
			Msg:  fmt.Sprintf("operation %s not permitted on pool %s", op, pool.ID),
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ServiceError — error con código semántico
// ─────────────────────────────────────────────────────────────────────────────

// ServiceError es el error que devuelven los métodos del service cuando
// fallan por una razón identificable. El handler HTTP puede leer el Code
// y devolver el código HTTP correcto al frontend.
type ServiceError struct {
	Code string // ErrCode* (ver storage_types.go)
	Msg  string

	// Details es información estructurada opcional sobre el error, serializada
	// como `error.details` en la respuesta HTTP. Útil cuando el frontend necesita
	// más que un mensaje (ej: DISK_HAS_FILESYSTEM necesita saber qué pool, qué
	// UUID, qué profile detectó para construir el wizard de doble intención).
	//
	// Si Details es nil, el JSON no incluye el campo. Si contiene un struct
	// con json tags, se serializa según esos tags.
	Details interface{} `json:"-"`
}

func (e *ServiceError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Msg)
}

// errFromCode devuelve un ServiceError con el código y mensaje dados.
func errFromCode(code, msg string) error {
	return &ServiceError{Code: code, Msg: msg}
}

// errFromCodeWithDetails devuelve un ServiceError con código, mensaje y
// payload estructurado adicional para que la HTTP layer pueda exponerlo
// en error.details.
func errFromCodeWithDetails(code, msg string, details interface{}) error {
	return &ServiceError{Code: code, Msg: msg, Details: details}
}

// ═════════════════════════════════════════════════════════════════════════════
// Queries síncronas — proyecciones que el frontend pedirá
// ═════════════════════════════════════════════════════════════════════════════

// ListPools devuelve todos los pools con sus devices cargados y enriquecidos
// con datos runtime (Usage, Health, IsPrimary, Mounted).
// Esta es la query principal que el frontend usa para mostrar el estado.
func (s *StorageService) ListPools(ctx context.Context) ([]*Pool, error) {
	pools, err := s.repo.ListPools(ctx)
	if err != nil {
		return nil, err
	}

	primaryPool := getPrimaryPoolName()

	// Hidratar cada pool con sus devices y capabilities + enriquecer
	for _, p := range pools {
		devices, err := s.repo.ListDevicesInPool(ctx, p.ID)
		if err != nil {
			return nil, fmt.Errorf("ListPools: hydrate devices for %s: %w", p.ID, err)
		}
		p.Devices = make([]Device, len(devices))
		for i, d := range devices {
			p.Devices[i] = *d
		}

		caps, err := s.repo.GetPoolCapabilities(ctx, p.ID)
		if err != nil {
			return nil, fmt.Errorf("ListPools: hydrate caps for %s: %w", p.ID, err)
		}
		p.Capabilities = caps

		// Campos derivados runtime (Usage, Health, IsPrimary, Mounted)
		enrichPool(p, primaryPool)
	}

	return pools, nil
}

// GetPool devuelve un pool por su ID con devices y capabilities hidratados
// y enriquecido con datos runtime (Usage, Health, IsPrimary, Mounted).
func (s *StorageService) GetPool(ctx context.Context, id string) (*Pool, error) {
	pool, err := s.repo.GetPool(ctx, id)
	if err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, errFromCode(ErrCodePoolNotFound,
			fmt.Sprintf("pool %s not found", id))
	}

	devices, err := s.repo.ListDevicesInPool(ctx, pool.ID)
	if err != nil {
		return nil, err
	}
	pool.Devices = make([]Device, len(devices))
	for i, d := range devices {
		pool.Devices[i] = *d
	}

	caps, err := s.repo.GetPoolCapabilities(ctx, pool.ID)
	if err != nil {
		return nil, err
	}
	pool.Capabilities = caps

	// Campos derivados runtime (Usage, Health, IsPrimary, Mounted)
	enrichPool(pool, getPrimaryPoolName())

	return pool, nil
}

// ListDevices devuelve todos los devices del sistema.
func (s *StorageService) ListDevices(ctx context.Context) ([]*Device, error) {
	return s.repo.ListDevices(ctx)
}

// ListAvailableDevices devuelve devices libres (no asignados a pool).
func (s *StorageService) ListAvailableDevices(ctx context.Context) ([]*Device, error) {
	return s.repo.ListAvailableDevices(ctx)
}

// ListOperations devuelve operaciones del journal según filtro.
// Útil para el activity timeline del frontend.
func (s *StorageService) ListOperations(ctx context.Context, f OperationFilter) ([]*Operation, error) {
	return s.repo.ListOperations(ctx, f)
}

// GetGeneration devuelve el contador global de mutaciones.
// El frontend puede usarlo para detectar si algo cambió antes de re-fetch.
func (s *StorageService) GetGeneration(ctx context.Context) (int64, error) {
	return s.repo.GetGlobalGeneration(ctx)
}

// ═════════════════════════════════════════════════════════════════════════════
// Mutaciones síncronas — metadata-only (sin BTRFS)
// ═════════════════════════════════════════════════════════════════════════════
//
// Estas operaciones solo modifican metadata en SQLite, sin tocar el
// filesystem real. Generan Operation con status=completed inmediato.
// see docs/storage_api.md §4.2

// RenamePool cambia el nombre legible de un pool.
// Síncrona. El id interno no cambia. Las shares siguen funcionando.
func (s *StorageService) RenamePool(ctx context.Context, id, newName string) (*Operation, error) {
	pool, err := s.repo.GetPool(ctx, id)
	if err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, errFromCode(ErrCodePoolNotFound, fmt.Sprintf("pool %s not found", id))
	}

	if err := s.checkPolicy(pool, OpTypeRenamePool); err != nil {
		return nil, err
	}

	// Verificar que el nombre no esté tomado
	other, err := s.repo.GetPoolByName(ctx, newName)
	if err != nil {
		return nil, err
	}
	if other != nil && other.ID != pool.ID {
		return nil, errFromCode(ErrCodePoolNameTaken,
			fmt.Sprintf("pool name %q already in use", newName))
	}

	// Crear Operation + ejecutar dentro de la misma tx
	op := &Operation{
		ID:     newUUID(),
		Type:   OpTypeRenamePool,
		PoolID: &pool.ID,
		Status: OpStatusInProgress,
		Data:   rawJSON(map[string]string{"from": pool.Name, "to": newName}),
	}

	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		if err := s.repo.CreateOperation(ctx, tx, op); err != nil {
			return err
		}
		if err := s.repo.RenamePool(ctx, tx, pool.ID, newName); err != nil {
			return err
		}
		return s.repo.UpdateOperationStatus(ctx, tx, op.ID, OpStatusCompleted, nil, nil)
	})
	if err != nil {
		return nil, err
	}

	// Recargar la operation con su completed_at actualizado
	return s.repo.GetOperation(ctx, op.ID)
}

// SetPoolCompression cambia la compresión de un pool.
// Síncrona. Solo afecta a archivos escritos a partir del cambio.
func (s *StorageService) SetPoolCompression(ctx context.Context, id, algorithm string) (*Operation, error) {
	// Usar GetPool del service (no del repo) porque hidrata capabilities,
	// que policy necesita para validar la op.
	pool, err := s.GetPool(ctx, id)
	if err != nil {
		return nil, err
	}

	if err := s.checkPolicy(pool, OpTypeSetCompression); err != nil {
		return nil, err
	}

	op := &Operation{
		ID:     newUUID(),
		Type:   OpTypeSetCompression,
		PoolID: &pool.ID,
		Status: OpStatusInProgress,
		Data:   rawJSON(map[string]string{"from": pool.Compression, "to": algorithm}),
	}

	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		if err := s.repo.CreateOperation(ctx, tx, op); err != nil {
			return err
		}
		if err := s.repo.SetPoolCompression(ctx, tx, pool.ID, algorithm); err != nil {
			return err
		}
		return s.repo.UpdateOperationStatus(ctx, tx, op.ID, OpStatusCompleted, nil, nil)
	})
	if err != nil {
		return nil, err
	}

	return s.repo.GetOperation(ctx, op.ID)
}

// ═════════════════════════════════════════════════════════════════════════════
// Stubs de mutaciones async (Bloque 2+)
// ═════════════════════════════════════════════════════════════════════════════
//
// Estos métodos están declarados como esqueleto. Su implementación real
// llegará cuando integremos BtrfsExecutor (Bloque 2 en adelante).
// De momento devuelven "not implemented" para que el código que los llame
// falle explícitamente en vez de silenciosamente.

// CreatePoolRequest es el payload de CreatePool.
//
// FORMATO DUAL DE ENTRADA (Postel's Law — "be liberal in what you accept"):
//
//   El campo de devices puede llegar de dos formas, y exactamente UNA de
//   ellas debe estar presente:
//
//     1. DeviceIDs — UUIDs internos de Beta 8 (forma canónica, estable)
//        Usado por:
//          · Tests
//          · Clientes que ya hicieron GET /v2/devices y conocen los IDs
//          · Llamadas internas desde otro código Go
//
//     2. Disks — paths de Linux (/dev/sdb, /dev/sdc)
//        Usado por:
//          · UI humana (el usuario piensa en paths, no UUIDs)
//          · Migración legacy desde storage.json
//          · Scripts/CLI manuales
//
//   Validate() normaliza ambos formatos a DeviceIDs internamente. Después
//   de Validate(), el resto del código del service trabaja siempre con
//   DeviceIDs (forma canónica). Una sola fuente de verdad.
//
// ERRORES de Validate():
//   · Ningún campo presente   → ErrCodeBadRequest "no devices specified"
//   · Ambos campos presentes  → ErrCodeBadRequest "specify EITHER disks OR device_ids"
//   · Path inexistente        → ErrCodeDeviceNotFound "device path %q not registered"
//   · Profile inválido        → ErrCodeProfileInvalid
//   · Insufficient disks      → ErrCodeInsufficientDisks
type CreatePoolRequest struct {
	Name    string  `json:"name"`
	Profile Profile `json:"profile"`

	// Exactamente UNO de estos dos debe estar presente.
	// Validate() los normaliza: rellena DeviceIDs a partir de Disks si hace falta.
	DeviceIDs []string `json:"device_ids,omitempty"`
	Disks     []string `json:"disks,omitempty"`

	Compression string `json:"compression,omitempty"`
	WipeFirst   bool   `json:"wipe_first,omitempty"`
}

// Validate verifica el request y normaliza Disks → DeviceIDs si aplica.
// Tras una llamada exitosa, req.DeviceIDs está poblado y Disks vacío.
//
// Por qué normaliza in-place y no devuelve un nuevo struct:
//   · El service ya recibe el request por valor (no se comparte estado externo)
//   · Evita allocar otra estructura
//   · El caller que quiera el request original lo conserva por valor antes de llamar
//
// Llamada típica:
//
//   if err := req.Validate(ctx, repo); err != nil { return nil, err }
//   // a partir de aquí req.DeviceIDs está garantizado poblado
func (req *CreatePoolRequest) Validate(ctx context.Context, repo *StorageRepo) error {
	// Validaciones que no dependen del repo
	if req.Name == "" {
		return errFromCode(ErrCodeBadRequest, "pool name is required")
	}
	if !req.Profile.IsValid() {
		return errFromCode(ErrCodeProfileInvalid,
			fmt.Sprintf("invalid profile %q", req.Profile))
	}

	// Resolver el formato dual de devices
	hasIDs := len(req.DeviceIDs) > 0
	hasDisks := len(req.Disks) > 0

	switch {
	case hasIDs && hasDisks:
		return errFromCode(ErrCodeBadRequest,
			"specify EITHER disks OR device_ids, not both")
	case !hasIDs && !hasDisks:
		return errFromCode(ErrCodeBadRequest, "no devices specified")
	case hasDisks:
		// Resolver paths → IDs vía repo.ListDevices.
		// Es O(N*M) pero N (discos del pool) es típicamente 2-8 y M
		// (devices totales en el sistema) es típicamente 2-20.
		// Para sistemas con 100+ discos, optimizar a un índice path→ID
		// pero no es necesario para el caso de uso real de NimOS.
		allDevices, err := repo.ListDevices(ctx)
		if err != nil {
			return fmt.Errorf("Validate: list devices: %w", err)
		}
		byPath := make(map[string]*Device, len(allDevices))
		for _, d := range allDevices {
			byPath[d.CurrentPath] = d
		}
		ids := make([]string, 0, len(req.Disks))
		for _, path := range req.Disks {
			d, ok := byPath[path]
			if !ok {
				return errFromCode(ErrCodeDeviceNotFound,
					fmt.Sprintf("device path %q not registered (run scan first?)", path))
			}
			ids = append(ids, d.ID)
		}
		req.DeviceIDs = ids
		req.Disks = nil // forma canónica para el resto del flujo
	}

	// Validación que requiere DeviceIDs ya resueltos
	if len(req.DeviceIDs) < req.Profile.MinDisks() {
		return errFromCode(ErrCodeInsufficientDisks,
			fmt.Sprintf("profile %s requires at least %d disks, got %d",
				req.Profile, req.Profile.MinDisks(), len(req.DeviceIDs)))
	}

	return nil
}

// CreatePool crea un nuevo pool BTRFS con los devices indicados.
// Asíncrona conceptualmente (genera Operation) pero ejecuta inline en
// Beta 8. El frontend hace polling vía la Operation.
//
// Pasos:
//   1. Validar request (name único, devices existen y están libres, profile válido)
//   2. Crear Operation con status in_progress
//   3. Ejecutar btrfs (mkfs, mount, identity file)
//   4. Persistir pool + assignments + capabilities en DB
//   5. Marcar Operation completed (o failed con rollback)
//   6. Devolver la Operation
func (s *StorageService) CreatePool(ctx context.Context, req CreatePoolRequest) (*Operation, error) {
	// ─── Validación + normalización (Disks → DeviceIDs si aplica) ──────
	// req.Validate() es el contrato:
	//   · Name no vacío
	//   · Profile válido
	//   · Exactamente uno de Disks o DeviceIDs presente
	//   · Después de Validate, req.DeviceIDs siempre está poblado
	//   · DeviceIDs cumple req.Profile.MinDisks()
	if err := req.Validate(ctx, s.repo); err != nil {
		return nil, err
	}

	// ¿Nombre ya tomado?
	existing, err := s.repo.GetPoolByName(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, errFromCode(ErrCodePoolNameTaken,
			fmt.Sprintf("pool name %q already in use", req.Name))
	}

	// ¿Devices existen y están libres? (Validate ya garantizó que existen
	// y son válidos como entidades; aquí verificamos disponibilidad.)
	devices := make([]*Device, 0, len(req.DeviceIDs))
	for _, id := range req.DeviceIDs {
		d, err := s.repo.GetDevice(ctx, id)
		if err != nil {
			return nil, err
		}
		if d == nil {
			// Race posible: el device existía cuando Validate corrió pero
			// fue borrado entre medias. Mensaje claro.
			return nil, errFromCode(ErrCodeDeviceNotFound,
				fmt.Sprintf("device %q not found (concurrent delete?)", id))
		}
		// ¿Ya está en un pool?
		inPool, err := s.deviceIsAssigned(ctx, d.ID)
		if err != nil {
			return nil, err
		}
		if inPool {
			return nil, errFromCode(ErrCodeDeviceInUse,
				fmt.Sprintf("device %q is already in a pool", d.ID))
		}
		devices = append(devices, d)
	}

	// ─── Pre-flight: detectar storage existente en los discos ──────────
	// Bloque C3.4 (Beta 8.1) — protección contra pérdida silenciosa de
	// datos. Si algún disco tiene un filesystem detectable, abortamos
	// con error tipado para que la UI muestre el wizard de doble
	// intención (importar vs destruir).
	//
	// WipeFirst=true salta el check: el usuario aceptó conscientemente
	// destruir lo que haya en los discos.
	//
	// El error tipado *ErrDiskHasFilesystem fluye TAL CUAL hasta
	// writeServiceError, que lo serializa con error.details rico para
	// la UI. No lo envolvemos en ServiceError para no perder los campos.
	//
	// DEUDA-ARQUI-OBSERVABLE-ENTITY (Beta 9): hoy checkDevicesAvailable
	// invoca preFlightCheck BTRFS-céntrico (vía DeviceChecker inyectable).
	// El día que NimOS soporte otros tipos de entidad (ext4, LUKS,
	// mdraid, NTFS USB...), solo cambia la impl del DeviceChecker — su
	// firma y su llamada desde CreatePool no cambian. Punto de extensión
	// deliberado.
	if !req.WipeFirst {
		if err := s.checkDevicesAvailable(ctx, devices); err != nil {
			return nil, err
		}
	}

	// ─── Crear Operation con status in_progress ────────────────────────
	op := &Operation{
		ID:     newUUID(),
		Type:   OpTypeCreatePool,
		Status: OpStatusInProgress,
		Data: rawJSON(map[string]interface{}{
			"name":       req.Name,
			"profile":    string(req.Profile),
			"device_ids": req.DeviceIDs,
		}),
	}
	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		return s.repo.CreateOperation(ctx, tx, op)
	})
	if err != nil {
		return nil, err
	}

	// ─── Ejecutar BTRFS ────────────────────────────────────────────────
	byIDPaths := make([]string, len(devices))
	for i, d := range devices {
		byIDPaths[i] = d.ByIDPath
	}

	fsInfo, err := s.btrfs.CreateFilesystem(ctx, CreateFilesystemRequest{
		Label:     req.Name,
		Profile:   req.Profile,
		ByIDPaths: byIDPaths,
		WipeFirst: req.WipeFirst,
	})
	if err != nil {
		s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeBtrfsCommandFailed)
		return s.repo.GetOperation(ctx, op.ID)
	}

	// Montar
	mountPoint := filepath.Join("/nimos/pools", req.Name)
	if err := s.btrfs.MountFilesystem(ctx, byIDPaths[0], mountPoint); err != nil {
		s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeMountFailed)
		return s.repo.GetOperation(ctx, op.ID)
	}

	// ─── Persistir en DB (pool + devices + capabilities) ───────────────
	poolID := newUUID()
	pool := &Pool{
		ID:           poolID,
		Name:         req.Name,
		BtrfsUUID:    fsInfo.BtrfsUUID,
		Profile:      req.Profile,
		MountPoint:   mountPoint,
		Role:         RoleData,
		ControlState: ControlStateManaged,
		Compression:  defaultIfEmpty(req.Compression, "none"),
	}

	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		if err := s.repo.CreatePool(ctx, tx, pool); err != nil {
			return err
		}
		for _, d := range devices {
			if err := s.repo.AssignDeviceToPool(ctx, tx, poolID, d.ID); err != nil {
				return err
			}
		}
		if err := s.repo.SetPoolCapabilities(ctx, tx, poolID,
			DefaultBtrfsManagedCapabilities()); err != nil {
			return err
		}
		// Actualizar la operation con el pool_id ahora que lo conocemos
		op.PoolID = &poolID
		if _, err := tx.ExecContext(ctx,
			`UPDATE storage_operations SET pool_id = ? WHERE id = ?`,
			poolID, op.ID); err != nil {
			return err
		}
		return s.repo.UpdateOperationStatus(ctx, tx, op.ID, OpStatusCompleted, nil, nil)
	})
	if err != nil {
		s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeInternal)
		return s.repo.GetOperation(ctx, op.ID)
	}

	return s.repo.GetOperation(ctx, op.ID)
}

// DestroyPool destruye un pool BTRFS y libera sus devices.
// Asíncrona conceptualmente; ejecuta inline en Beta 8.
func (s *StorageService) DestroyPool(ctx context.Context, poolID string) (*Operation, error) {
	pool, err := s.GetPool(ctx, poolID)
	if err != nil {
		return nil, err
	}

	if err := s.checkPolicy(pool, OpTypeDestroyPool); err != nil {
		return nil, err
	}

	// Crear operation
	op := &Operation{
		ID:     newUUID(),
		Type:   OpTypeDestroyPool,
		PoolID: &pool.ID,
		Status: OpStatusInProgress,
		Data: rawJSON(map[string]interface{}{
			"name":       pool.Name,
			"btrfs_uuid": pool.BtrfsUUID,
		}),
	}
	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		return s.repo.CreateOperation(ctx, tx, op)
	})
	if err != nil {
		return nil, err
	}

	// Recolectar by-id paths antes de borrar nada
	byIDPaths := make([]string, len(pool.Devices))
	for i, d := range pool.Devices {
		byIDPaths[i] = d.ByIDPath
	}

	// Ejecutar destroy físico
	err = s.btrfs.DestroyFilesystem(ctx, DestroyFilesystemRequest{
		MountPoint: pool.MountPoint,
		ByIDPaths:  byIDPaths,
		Force:      false,
	})
	if err != nil {
		s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeBtrfsCommandFailed)
		return s.repo.GetOperation(ctx, op.ID)
	}

	// Borrar pool de DB (CASCADE limpia pool_devices, capabilities;
	// SET NULL preserva las operations en histórico)
	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		if err := s.repo.DeletePool(ctx, tx, pool.ID); err != nil {
			return err
		}
		return s.repo.UpdateOperationStatus(ctx, tx, op.ID, OpStatusCompleted, nil, nil)
	})
	if err != nil {
		s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeInternal)
		return s.repo.GetOperation(ctx, op.ID)
	}

	return s.repo.GetOperation(ctx, op.ID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers privados de mutaciones async
// ─────────────────────────────────────────────────────────────────────────────

// markOperationFailed actualiza la operation a failed con el código dado.
// Best-effort: si la actualización falla, lo loggea pero no propaga.
func (s *StorageService) markOperationFailed(ctx context.Context, opID, errMsg, errCode string) {
	err := s.runInTx(ctx, func(tx *sql.Tx) error {
		return s.repo.UpdateOperationStatus(ctx, tx, opID, OpStatusFailed, &errMsg, &errCode)
	})
	if err != nil {
		logMsg("markOperationFailed: cannot update op %s: %v", opID, err)
	}
}

// deviceIsAssigned devuelve true si el device está asignado a algún pool.
func (s *StorageService) deviceIsAssigned(ctx context.Context, deviceID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM storage_pool_devices WHERE device_id = ?`,
		deviceID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("deviceIsAssigned: %w", err)
	}
	return count > 0, nil
}

// defaultIfEmpty devuelve fallback si s es "".
func defaultIfEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// ═════════════════════════════════════════════════════════════════════════════
// ScanDevices — descubrir hardware y reconciliar con DB
// ═════════════════════════════════════════════════════════════════════════════

// ScanResult resume el resultado de una ejecución de ScanDevices.
type ScanResult struct {
	Total    int // discos físicos vistos
	Inserted int // discos nuevos registrados en DB
	Updated  int // discos ya conocidos cuya info se actualizó
	Skipped  int // discos descartados por no tener serial
}

// ScanDevices ejecuta el scanner y persiste los resultados en la DB.
//
// Comportamiento:
//   - Cada disco visto por el scanner se hace UPSERT por serial (identidad absoluta)
//   - Devices ya en DB cuyo current_path cambió se actualizan
//   - Devices en DB que NO aparecen en el scan NO se borran (auditoría;
//     se marcarán como "missing" por el reconciler de Fase 4)
//   - Devices sin serial son rechazados (storage_invariants.md#3.3)
//   - Devices sin by_id_path se loggean como warning pero se intentan persistir
//     (por si en el siguiente scan udev los expone)
//
// Idempotente: se puede ejecutar muchas veces sin efectos secundarios.
//
// see docs/storage_state_machines.md §5 (Device lifecycle)
func (s *StorageService) ScanDevices(ctx context.Context) (*ScanResult, error) {
	scanned, err := s.scanner.ScanDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("ScanDevices: scanner failed: %w", err)
	}

	result := &ScanResult{Total: len(scanned)}

	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		for _, sd := range scanned {
			if sd.Serial == "" {
				// Defensive: el scanner ya filtra esto, pero confirmamos
				result.Skipped++
				continue
			}

			// Construir el Device (si by_id_path está vacío, usar device_path
			// como fallback temporal; siguiente scan lo corregirá)
			byIDPath := sd.ByIDPath
			if byIDPath == "" {
				byIDPath = sd.DevicePath
				logMsg("ScanDevices: warning, %s has no by-id symlink yet", sd.Serial)
			}

			dev := &Device{
				ID:          newUUID(), // se ignora si ya existe (UpsertDevice usa serial)
				Serial:      sd.Serial,
				ByIDPath:    byIDPath,
				CurrentPath: sd.DevicePath,
				WWN:         sd.WWN,
				Model:       sd.Model,
				SizeBytes:   sd.SizeBytes,
				LastSeenAt:  s.clock.Now().UTC(),
			}

			// UpsertDevice devuelve true si fue insert (nuevo), false si update.
			wasInsert, err := s.repo.UpsertDevice(ctx, tx, dev)
			if err != nil {
				return fmt.Errorf("ScanDevices: upsert %s: %w", sd.Serial, err)
			}
			if wasInsert {
				result.Inserted++
			} else {
				result.Updated++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	logMsg("ScanDevices: total=%d inserted=%d updated=%d skipped=%d",
		result.Total, result.Inserted, result.Updated, result.Skipped)
	return result, nil
}

// ═════════════════════════════════════════════════════════════════════════════
// AddDevice — expandir un pool añadiendo un disco
// ═════════════════════════════════════════════════════════════════════════════

// AddDeviceRequest es el payload de AddDevice.
type AddDeviceRequest struct {
	PoolID    string `json:"pool_id"`
	DeviceID  string `json:"device_id"`
	WipeFirst bool   `json:"wipe_first,omitempty"`
}

// AddDevice añade un device a un pool BTRFS existente.
// Genera Operation con type=add_device. Ejecuta inline en Beta 8
// (el frontend hace polling vía la Operation).
//
// Pasos:
//   1. Verificar policy (pool managed, capability add_device)
//   2. Verificar que el device existe y NO está en otro pool
//   3. Crear Operation con status in_progress
//   4. Ejecutar btrfs device add
//   5. Persistir la asignación en DB
//   6. Marcar Operation completed (o failed con rollback)
func (s *StorageService) AddDevice(ctx context.Context, req AddDeviceRequest) (*Operation, error) {
	// ─── Validaciones ──────────────────────────────────────────────────
	pool, err := s.GetPool(ctx, req.PoolID)
	if err != nil {
		return nil, err
	}

	if err := s.checkPolicy(pool, OpTypeAddDevice); err != nil {
		return nil, err
	}

	device, err := s.repo.GetDevice(ctx, req.DeviceID)
	if err != nil {
		return nil, err
	}
	if device == nil {
		return nil, errFromCode(ErrCodeDeviceNotFound,
			fmt.Sprintf("device %q not found", req.DeviceID))
	}

	inUse, err := s.deviceIsAssigned(ctx, device.ID)
	if err != nil {
		return nil, err
	}
	if inUse {
		return nil, errFromCode(ErrCodeDeviceInUse,
			fmt.Sprintf("device %q is already in a pool", device.ID))
	}

	// ─── Crear Operation ───────────────────────────────────────────────
	op := &Operation{
		ID:     newUUID(),
		Type:   OpTypeAddDevice,
		PoolID: &pool.ID,
		Status: OpStatusInProgress,
		Data: rawJSON(map[string]interface{}{
			"device_id":   device.ID,
			"device_serial": device.Serial,
		}),
	}
	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		return s.repo.CreateOperation(ctx, tx, op)
	})
	if err != nil {
		// Si esto falla por INV-1 (UNIQUE parcial), error útil al caller
		return nil, errFromCode(ErrCodeOperationInProgress,
			fmt.Sprintf("another layout operation is in progress on pool %s", pool.ID))
	}

	// ─── Wipe defensivo opcional ───────────────────────────────────────
	if req.WipeFirst {
		if err := s.btrfs.WipeDevice(ctx, device.ByIDPath); err != nil {
			s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeBtrfsCommandFailed)
			return s.repo.GetOperation(ctx, op.ID)
		}
	}

	// ─── Ejecutar btrfs device add ─────────────────────────────────────
	if err := s.btrfs.AddDevice(ctx, pool.MountPoint, device.ByIDPath); err != nil {
		s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeBtrfsCommandFailed)
		return s.repo.GetOperation(ctx, op.ID)
	}

	// ─── Persistir asignación ──────────────────────────────────────────
	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		if err := s.repo.AssignDeviceToPool(ctx, tx, pool.ID, device.ID); err != nil {
			return err
		}
		return s.repo.UpdateOperationStatus(ctx, tx, op.ID, OpStatusCompleted, nil, nil)
	})
	if err != nil {
		s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeInternal)
		return s.repo.GetOperation(ctx, op.ID)
	}

	return s.repo.GetOperation(ctx, op.ID)
}

// ═════════════════════════════════════════════════════════════════════════════
// RemoveDevice — quitar un disco del pool
// ═════════════════════════════════════════════════════════════════════════════

// RemoveDeviceRequest es el payload de RemoveDevice.
type RemoveDeviceRequest struct {
	PoolID   string `json:"pool_id"`
	DeviceID string `json:"device_id"`
}

// RemoveDevice quita un device del pool. BTRFS hace balance implícito
// (mueve datos del device a los demás). Operación pesada — puede tardar.
//
// Validaciones:
//   - Pool managed, capability remove_device
//   - Device pertenece al pool indicado
//   - Tras quitar este device, el pool sigue teniendo >= MinDisks()
//     para su profile (no degradar el profile)
func (s *StorageService) RemoveDevice(ctx context.Context, req RemoveDeviceRequest) (*Operation, error) {
	pool, err := s.GetPool(ctx, req.PoolID)
	if err != nil {
		return nil, err
	}

	if err := s.checkPolicy(pool, OpTypeRemoveDevice); err != nil {
		return nil, err
	}

	// Buscar el device en este pool
	var device *Device
	for i := range pool.Devices {
		if pool.Devices[i].ID == req.DeviceID {
			device = &pool.Devices[i]
			break
		}
	}
	if device == nil {
		return nil, errFromCode(ErrCodeDeviceNotFound,
			fmt.Sprintf("device %q is not part of pool %s", req.DeviceID, pool.ID))
	}

	// No bajar del mínimo del profile
	if len(pool.Devices)-1 < pool.Profile.MinDisks() {
		return nil, errFromCode(ErrCodeMinDisksReached,
			fmt.Sprintf("cannot remove device: profile %s requires at least %d disks",
				pool.Profile, pool.Profile.MinDisks()))
	}

	// Operation
	op := &Operation{
		ID:     newUUID(),
		Type:   OpTypeRemoveDevice,
		PoolID: &pool.ID,
		Status: OpStatusInProgress,
		Data: rawJSON(map[string]interface{}{
			"device_id":     device.ID,
			"device_serial": device.Serial,
		}),
	}
	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		return s.repo.CreateOperation(ctx, tx, op)
	})
	if err != nil {
		return nil, errFromCode(ErrCodeOperationInProgress,
			fmt.Sprintf("another layout operation is in progress on pool %s", pool.ID))
	}

	// Ejecutar btrfs device remove
	if err := s.btrfs.RemoveDevice(ctx, pool.MountPoint, device.ByIDPath); err != nil {
		s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeBtrfsCommandFailed)
		return s.repo.GetOperation(ctx, op.ID)
	}

	// Persistir desasignación
	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		if err := s.repo.UnassignDeviceFromPool(ctx, tx, pool.ID, device.ID); err != nil {
			return err
		}
		return s.repo.UpdateOperationStatus(ctx, tx, op.ID, OpStatusCompleted, nil, nil)
	})
	if err != nil {
		s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeInternal)
		return s.repo.GetOperation(ctx, op.ID)
	}

	return s.repo.GetOperation(ctx, op.ID)
}

// ═════════════════════════════════════════════════════════════════════════════
// ReplaceDevice — sustituir un disco por otro
// ═════════════════════════════════════════════════════════════════════════════

// ReplaceDeviceRequest es el payload de ReplaceDevice.
type ReplaceDeviceRequest struct {
	PoolID      string `json:"pool_id"`
	OldDeviceID string `json:"old_device_id"`
	NewDeviceID string `json:"new_device_id"`
}

// ReplaceDevice sustituye un device dentro del pool por otro. Más eficiente
// que remove+add porque btrfs replace start sincroniza desde los demás
// miembros sin un balance completo.
//
// IMPORTANTE: el old device se hace wipefs SEGURO (no a ciegas).
// see docs/storage_invariants.md#4.2
func (s *StorageService) ReplaceDevice(ctx context.Context, req ReplaceDeviceRequest) (*Operation, error) {
	pool, err := s.GetPool(ctx, req.PoolID)
	if err != nil {
		return nil, err
	}

	if err := s.checkPolicy(pool, OpTypeReplaceDevice); err != nil {
		return nil, err
	}

	// Old debe estar en el pool
	var oldDev *Device
	for i := range pool.Devices {
		if pool.Devices[i].ID == req.OldDeviceID {
			oldDev = &pool.Devices[i]
			break
		}
	}
	if oldDev == nil {
		return nil, errFromCode(ErrCodeDeviceNotFound,
			fmt.Sprintf("old device %q is not part of pool %s", req.OldDeviceID, pool.ID))
	}

	// New debe existir y NO estar en otro pool
	newDev, err := s.repo.GetDevice(ctx, req.NewDeviceID)
	if err != nil {
		return nil, err
	}
	if newDev == nil {
		return nil, errFromCode(ErrCodeDeviceNotFound,
			fmt.Sprintf("new device %q not found", req.NewDeviceID))
	}
	inUse, err := s.deviceIsAssigned(ctx, newDev.ID)
	if err != nil {
		return nil, err
	}
	if inUse {
		return nil, errFromCode(ErrCodeDeviceInUse,
			fmt.Sprintf("new device %q is already in a pool", newDev.ID))
	}

	// Validar tamaño del nuevo >= old (BTRFS no permite shrink implícito)
	if newDev.SizeBytes < oldDev.SizeBytes {
		return nil, errFromCode(ErrCodeDeviceNotEligible,
			fmt.Sprintf("new device size (%d) < old device size (%d)",
				newDev.SizeBytes, oldDev.SizeBytes))
	}

	op := &Operation{
		ID:     newUUID(),
		Type:   OpTypeReplaceDevice,
		PoolID: &pool.ID,
		Status: OpStatusInProgress,
		Data: rawJSON(map[string]interface{}{
			"old_device_id":     oldDev.ID,
			"old_device_serial": oldDev.Serial,
			"new_device_id":     newDev.ID,
			"new_device_serial": newDev.Serial,
		}),
	}
	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		return s.repo.CreateOperation(ctx, tx, op)
	})
	if err != nil {
		return nil, errFromCode(ErrCodeOperationInProgress,
			fmt.Sprintf("another layout operation is in progress on pool %s", pool.ID))
	}

	// Ejecutar btrfs replace (incluye wipefs seguro del old)
	if err := s.btrfs.ReplaceDevice(ctx, pool.MountPoint, oldDev.ByIDPath, newDev.ByIDPath); err != nil {
		s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeBtrfsCommandFailed)
		return s.repo.GetOperation(ctx, op.ID)
	}

	// Swap atómico: desasignar old, asignar new
	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		if err := s.repo.UnassignDeviceFromPool(ctx, tx, pool.ID, oldDev.ID); err != nil {
			return err
		}
		if err := s.repo.AssignDeviceToPool(ctx, tx, pool.ID, newDev.ID); err != nil {
			return err
		}
		return s.repo.UpdateOperationStatus(ctx, tx, op.ID, OpStatusCompleted, nil, nil)
	})
	if err != nil {
		s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeInternal)
		return s.repo.GetOperation(ctx, op.ID)
	}

	return s.repo.GetOperation(ctx, op.ID)
}

// ═════════════════════════════════════════════════════════════════════════════
// ConvertProfile — cambiar el perfil de un pool
// ═════════════════════════════════════════════════════════════════════════════

// ConvertProfileRequest es el payload de ConvertProfile.
type ConvertProfileRequest struct {
	PoolID     string  `json:"pool_id"`
	NewProfile Profile `json:"new_profile"`
}

// ConvertProfile cambia el perfil de un pool (ej: single → raid1, raid1 → raid10).
// Operación pesada (mueve datos). Validaciones:
//   - Pool managed, capability convert_profile
//   - El profile destino es válido y compatible con número de discos actual
//   - El profile destino es DIFERENTE al actual
func (s *StorageService) ConvertProfile(ctx context.Context, req ConvertProfileRequest) (*Operation, error) {
	pool, err := s.GetPool(ctx, req.PoolID)
	if err != nil {
		return nil, err
	}

	if err := s.checkPolicy(pool, OpTypeConvertProfile); err != nil {
		return nil, err
	}

	if !req.NewProfile.IsValid() {
		return nil, errFromCode(ErrCodeProfileInvalid,
			fmt.Sprintf("invalid profile %q", req.NewProfile))
	}
	if req.NewProfile == pool.Profile {
		return nil, errFromCode(ErrCodeBadRequest,
			fmt.Sprintf("pool is already in profile %q", req.NewProfile))
	}
	if len(pool.Devices) < req.NewProfile.MinDisks() {
		return nil, errFromCode(ErrCodeInsufficientDisks,
			fmt.Sprintf("profile %s requires at least %d disks, pool has %d",
				req.NewProfile, req.NewProfile.MinDisks(), len(pool.Devices)))
	}

	op := &Operation{
		ID:     newUUID(),
		Type:   OpTypeConvertProfile,
		PoolID: &pool.ID,
		Status: OpStatusInProgress,
		Data: rawJSON(map[string]interface{}{
			"from_profile": string(pool.Profile),
			"to_profile":   string(req.NewProfile),
		}),
	}
	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		return s.repo.CreateOperation(ctx, tx, op)
	})
	if err != nil {
		return nil, errFromCode(ErrCodeOperationInProgress,
			fmt.Sprintf("another layout operation is in progress on pool %s", pool.ID))
	}

	// Ejecutar btrfs balance con conversión
	if err := s.btrfs.ConvertProfile(ctx, pool.MountPoint, req.NewProfile); err != nil {
		s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeBtrfsCommandFailed)
		return s.repo.GetOperation(ctx, op.ID)
	}

	// Persistir el nuevo profile en la DB
	err = s.runInTx(ctx, func(tx *sql.Tx) error {
		// El profile no tiene un setter dedicado en el repo (los profiles
		// no son una columna que se cambia desde UI normalmente). Lo hago
		// vía UPDATE directo dentro de la tx para mantener atomicidad.
		_, e := tx.ExecContext(ctx,
			`UPDATE storage_pools SET profile = ?, generation = generation + 1 WHERE id = ?`,
			string(req.NewProfile), pool.ID)
		if e != nil {
			return fmt.Errorf("update profile: %w", e)
		}
		if _, e := s.repo.incrementGlobalGeneration(ctx, tx); e != nil {
			return e
		}
		return s.repo.UpdateOperationStatus(ctx, tx, op.ID, OpStatusCompleted, nil, nil)
	})
	if err != nil {
		s.markOperationFailed(ctx, op.ID, err.Error(), ErrCodeInternal)
		return s.repo.GetOperation(ctx, op.ID)
	}

	return s.repo.GetOperation(ctx, op.ID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Pre-flight checks
// ─────────────────────────────────────────────────────────────────────────────

// checkDevicesAvailable verifica que los devices están realmente disponibles
// para crear un pool nuevo. Detecta filesystems o estructuras de storage
// preexistentes que NimOS borraría silenciosamente si procediera.
//
// Devuelve:
//   nil                           si todos los devices están limpios
//   *ErrDiskHasFilesystem         si algún device tiene BTRFS detectable
//                                 (con detalles managed/observed para UX)
//   error genérico                otros fallos (disco missing, boot disk,
//                                 holders activos: LVM/dm/RAID)
//
// Delega en s.deviceChecker (inyectable). Si no está seteado, usa el
// checker de producción (realDevicePreFlightCheck → preFlightCheck BTRFS).
// Los tests inyectan noop para no requerir devices físicos en /dev.
//
// PUNTO DE EXTENSIÓN (DEUDA-ARQUI-OBSERVABLE-ENTITY, Beta 9):
// Cuando NimOS soporte detección de otras entidades observables
// (ext4, mdraid, LUKS, ZFS reimport, NTFS/exFAT en USB...), se cambia
// la implementación de DeviceChecker — su firma y su llamada desde
// CreatePool no cambian.
//
// see storage_wipe.go: preFlightCheck, ErrDiskHasFilesystem
func (s *StorageService) checkDevicesAvailable(ctx context.Context, devices []*Device) error {
	checker := s.deviceChecker
	if checker == nil {
		checker = realDevicePreFlightCheck
	}
	return checker(devices)
}

// noopDeviceChecker es un DeviceChecker que siempre pasa. Útil en tests
// donde los devices son fake (no existen en /dev) y queremos verificar
// la lógica de CreatePool sin que el preflight real falle por stat().
//
// Producción NUNCA debe usar esto — es solo para inyectar en tests
// vía service.deviceChecker = noopDeviceChecker.
func noopDeviceChecker(devices []*Device) error {
	return nil
}
