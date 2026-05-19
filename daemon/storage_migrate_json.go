// storage_migrate_json.go — Migración one-shot storage.json → SQLite.
//
// CONTEXTO HISTÓRICO:
//   Beta 7 guardaba pools en /var/lib/nimos/config/storage.json.
//   Beta 8 introdujo el stack SQLite (storage_pools, storage_devices,
//   storage_pool_devices), pero hasta Fase 7 los dos coexistían:
//   la UI escribía en JSON via handlers legacy, mientras que el
//   stack nuevo permanecía vacío en producción.
//
//   Este migrador es el puente: lee storage.json una vez, popula
//   las tablas SQLite, y renombra el JSON a .migrated-YYYYMMDD.
//
// SEMÁNTICA:
//   - One-shot: se ejecuta al boot. Si SQLite ya tiene pools, NO toca nada.
//   - Best-effort: errores parciales se loggean, no abortan el daemon.
//   - Idempotente: ejecutarlo 2 veces no causa daño (la 2ª vez es no-op).
//   - Trazable: tras éxito, el JSON se renombra (no se borra) por si
//     hace falta inspección post-mortem.
//
// PRE-CONDICIONES:
//   - storageService inicializado (acceso a Repo + Scanner)
//   - Schema storage aplicado
//   - hasBtrfs == true (no migramos en sistemas sin BTRFS)
//
// POST-CONDICIONES (en caso de éxito):
//   - storage_pools tiene N filas (una por pool del JSON)
//   - storage_devices tiene M filas (una por disco usado en cualquier pool)
//   - storage_pool_devices tiene las relaciones N:M correctas
//   - storage_metadata['primary_pool'] = UUID del primary pool del JSON
//   - storage.json renombrado a storage.json.migrated-YYYYMMDD-HHMMSS
//
// FALLO NO FATAL:
//   Si la migración falla parcialmente, el daemon sigue. Lo que se haya
//   migrado se queda. La próxima vez NO reintenta (ya hay filas en SQLite).
//   El operador puede borrar manualmente las filas y volver a renombrar
//   el JSON quitándole el sufijo para reintentar.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// migrateStorageJSONOnce ejecuta la migración si las condiciones se cumplen.
// Llamada desde main.go tras initStorageModule y antes de servir HTTP.
//
// No devuelve error: en caso de fallo, loggea y sigue. La intención es que
// el daemon arranque siempre, aunque la migración falle.
func migrateStorageJSONOnce() {
	if storageService == nil {
		logMsg("migrateStorageJSONOnce: storageService not initialized, skipping")
		return
	}
	if !hasBtrfs {
		logMsg("migrateStorageJSONOnce: BTRFS not available, skipping")
		return
	}

	ctx := context.Background()

	// 1. ¿Hay pools en SQLite ya? Si sí, no migramos (idempotencia).
	hasPools, err := storageService.repo.HasAnyPool(ctx)
	if err != nil {
		logMsg("migrateStorageJSONOnce: cannot check existing pools: %v", err)
		return
	}
	if hasPools {
		// Ya migrado o uso normal de v2. No tocar.
		return
	}

	// 2. ¿Existe storage.json con pools dentro?
	data, err := os.ReadFile(storageConfigFile)
	if err != nil {
		// JSON no existe → instalación nueva, nada que migrar. Silencio.
		return
	}

	var conf map[string]interface{}
	if err := json.Unmarshal(data, &conf); err != nil {
		logMsg("migrateStorageJSONOnce: storage.json malformed, skipping: %v", err)
		return
	}

	confPools, _ := conf["pools"].([]interface{})
	if len(confPools) == 0 {
		// JSON existe pero sin pools → nada que hacer.
		return
	}

	logMsg("migrateStorageJSONOnce: found %d pools in storage.json, migrating to SQLite...", len(confPools))

	// 3. Scan de devices para poblar storage_devices con datos completos
	//    (serial, by_id_path, etc. — datos que el JSON no guarda).
	if _, err := storageService.ScanDevices(ctx); err != nil {
		logMsg("migrateStorageJSONOnce: device scan failed: %v", err)
		// No abortamos: puede haber pools sin discos visibles (raro pero posible)
	}

	// 4. Migrar cada pool
	migrated := 0
	failed := 0
	for _, raw := range confPools {
		poolConf, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if err := migrateOnePoolFromJSON(ctx, poolConf); err != nil {
			poolName, _ := poolConf["name"].(string)
			logMsg("migrateStorageJSONOnce: pool %q failed: %v", poolName, err)
			failed++
			continue
		}
		migrated++
	}

	// 5. primary_pool en storage_metadata
	if primaryName, _ := conf["primaryPool"].(string); primaryName != "" {
		if err := setPrimaryPoolByName(ctx, primaryName); err != nil {
			logMsg("migrateStorageJSONOnce: cannot set primary_pool: %v", err)
		}
	}

	// 6. Si TODOS los pools migraron OK, archivar el JSON. Si alguno
	//    falló, dejamos el JSON intacto para que el operador inspeccione.
	if failed == 0 && migrated > 0 {
		archivedName := storageConfigFile + ".migrated-" + time.Now().UTC().Format("20060102-150405")
		if err := os.Rename(storageConfigFile, archivedName); err != nil {
			logMsg("migrateStorageJSONOnce: cannot archive JSON: %v", err)
		} else {
			logMsg("migrateStorageJSONOnce: archived storage.json → %s", archivedName)
		}
	}

	logMsg("migrateStorageJSONOnce: done · %d migrated · %d failed", migrated, failed)
}

// migrateOnePoolFromJSON migra un solo pool del JSON a SQLite.
// Si el pool no tiene UUID BTRFS (caso anómalo), intenta resolverlo via
// `btrfs filesystem show <mountPoint>`. Si tampoco se puede, lo skippea.
func migrateOnePoolFromJSON(ctx context.Context, poolConf map[string]interface{}) error {
	name, _ := poolConf["name"].(string)
	if name == "" {
		return fmt.Errorf("pool has no name")
	}

	poolType, _ := poolConf["type"].(string)
	if poolType != "" && poolType != "btrfs" {
		return fmt.Errorf("pool type %q not supported in Beta 8 (BTRFS-only)", poolType)
	}

	profile, _ := poolConf["profile"].(string)
	if profile == "" {
		profile = "single"
	}
	// El JSON podría tener profile no soportado por el schema. Normalizamos.
	switch profile {
	case "single", "raid1", "raid1c3", "raid10":
		// OK
	default:
		return fmt.Errorf("profile %q not supported", profile)
	}

	mountPoint, _ := poolConf["mountPoint"].(string)
	if mountPoint == "" {
		mountPoint = nimosPoolsDir + "/" + name
	}

	// UUID: 1º del JSON, 2º via btrfs filesystem show
	btrfsUUID, _ := poolConf["uuid"].(string)
	if btrfsUUID == "" {
		btrfsUUID = resolveBtrfsUUIDFromMount(mountPoint)
	}
	if btrfsUUID == "" {
		return fmt.Errorf("cannot resolve BTRFS UUID for pool %q (mount: %s)", name, mountPoint)
	}

	createdAt, _ := poolConf["createdAt"].(string)
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339)
	}

	// Resolver discos: el JSON guarda paths como /dev/sdb, /dev/sdc.
	// Tras ScanDevices ya tenemos los devices en SQLite con identidad
	// completa. Cruzamos por current_path.
	disksRaw, _ := poolConf["disks"].([]interface{})
	if len(disksRaw) == 0 {
		return fmt.Errorf("pool %q has no disks in JSON", name)
	}

	var deviceIDs []string
	for _, d := range disksRaw {
		diskPath, _ := d.(string)
		if diskPath == "" {
			continue
		}
		// Normalizar: el JSON puede guardar "/dev/sdb1" (con partición)
		// pero los devices de Beta 8 son por disco entero ("/dev/sdb").
		// Strippear sufijo numérico de partición.
		basePath := stripPartitionSuffix(diskPath)

		// Buscar device por current_path. El repo no tiene helper
		// directo para esto, así que listamos y filtramos.
		// Es O(n) pero al migrar tienes 2-4 discos típicamente.
		allDevices, err := storageService.repo.ListDevices(ctx)
		if err != nil {
			return fmt.Errorf("list devices: %w", err)
		}
		var found *Device
		for i := range allDevices {
			if allDevices[i].CurrentPath == basePath {
				found = allDevices[i]
				break
			}
		}
		if found == nil {
			return fmt.Errorf("device %s not found in storage_devices (scan must have failed)", basePath)
		}
		deviceIDs = append(deviceIDs, found.ID)
	}

	if len(deviceIDs) == 0 {
		return fmt.Errorf("pool %q resolved zero devices", name)
	}

	// Parsear createdAt o usar now si malformado
	createdAtT, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		createdAtT = time.Now().UTC()
	}

	// Insertar pool + relaciones en una transacción
	tx, err := storageService.repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() // safe si Commit ya pasó

	poolID := newUUID()
	pool := &Pool{
		ID:           poolID,
		Name:         name,
		BtrfsUUID:    btrfsUUID,
		Profile:      Profile(profile),
		MountPoint:   mountPoint,
		Role:         RoleData,
		Compression:  "none",
		ControlState: ControlStateManaged,
		CreatedAt:    createdAtT,
		Generation:   0,
	}
	if err := storageService.repo.CreatePool(ctx, tx, pool); err != nil {
		return fmt.Errorf("create pool: %w", err)
	}

	for _, devID := range deviceIDs {
		if err := storageService.repo.AssignDeviceToPool(ctx, tx, poolID, devID); err != nil {
			return fmt.Errorf("assign device %s: %w", devID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	logMsg("migrate: pool %q (%s, %d devices) → SQLite ok", name, profile, len(deviceIDs))
	return nil
}

// resolveBtrfsUUIDFromMount intenta extraer el UUID del BTRFS via shell.
// Si falla devuelve "".
func resolveBtrfsUUIDFromMount(mountPoint string) string {
	if mountPoint == "" {
		return ""
	}
	out, ok := runSafe("btrfs", "filesystem", "show", mountPoint)
	if !ok {
		return ""
	}
	// La salida tiene una línea tipo "Label: 'data'  uuid: <UUID>"
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, "uuid:"); idx >= 0 {
			rest := strings.TrimSpace(line[idx+len("uuid:"):])
			// Tomar primer token (puede haber más texto después)
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return ""
}

// stripPartitionSuffix convierte "/dev/sdb1" → "/dev/sdb".
// Maneja también nvme0n1p1 → nvme0n1 (sufijo "pN" para NVMe).
//
// Casos:
//   /dev/sdb       → /dev/sdb       (sin partición)
//   /dev/sdb1      → /dev/sdb
//   /dev/sda10     → /dev/sda
//   /dev/nvme0n1   → /dev/nvme0n1   (¡el "1" es parte del nombre!)
//   /dev/nvme0n1p1 → /dev/nvme0n1
func stripPartitionSuffix(path string) string {
	if path == "" {
		return ""
	}
	// Reconocer NVMe: si contiene "nvme" y termina en "pN" (p + dígitos)
	// entonces strippear ese sufijo. Si no, dejar tal cual porque el
	// "1" final puede ser parte del namespace (nvme0n1).
	if strings.Contains(path, "nvme") {
		// Buscar la última "p" precedida de dígito y seguida solo de dígitos.
		for i := len(path) - 1; i >= 1; i-- {
			if path[i] == 'p' && isDigit(path[i-1]) {
				rest := path[i+1:]
				if rest != "" && isAllDigits(rest) {
					return path[:i]
				}
			}
		}
		// No tiene sufijo "pN" → devolver tal cual
		return path
	}
	// SATA: /dev/sdb1 → /dev/sdb. Strippear dígitos finales.
	for i := len(path) - 1; i >= 0; i-- {
		if !isDigit(path[i]) {
			return path[:i+1]
		}
	}
	return path
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

// setPrimaryPoolByName escribe la metadata 'primary_pool' usando el UUID
// del pool migrado.
func setPrimaryPoolByName(ctx context.Context, poolName string) error {
	pool, err := storageService.repo.GetPoolByName(ctx, poolName)
	if err != nil {
		return fmt.Errorf("lookup primary pool: %w", err)
	}
	if pool == nil {
		return fmt.Errorf("primary pool %q not found after migration", poolName)
	}
	_, err = storageService.repo.db.ExecContext(ctx,
		`INSERT INTO storage_metadata (key, value) VALUES ('primary_pool', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, pool.ID)
	if err != nil {
		return fmt.Errorf("set primary_pool metadata: %w", err)
	}
	return nil
}
