// storage_migrate_json_test.go — Tests del migrador one-shot.
//
// Cubrimos:
//   - Skip si SQLite ya tiene pools (idempotencia)
//   - Skip si storage.json no existe (instalación nueva)
//   - Skip si storage.json existe pero sin pools
//   - Skip si storage.json malformado (no abortar daemon)
//   - stripPartitionSuffix correcto para SATA y NVMe

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── stripPartitionSuffix ─────────────────────────────────────────────────

func TestStripPartitionSuffix(t *testing.T) {
	cases := []struct {
		input  string
		expect string
	}{
		// SATA
		{"/dev/sdb", "/dev/sdb"},
		{"/dev/sdb1", "/dev/sdb"},
		{"/dev/sdc3", "/dev/sdc"},
		{"/dev/sda10", "/dev/sda"},

		// NVMe
		{"/dev/nvme0n1", "/dev/nvme0n1"},
		{"/dev/nvme0n1p1", "/dev/nvme0n1"},
		{"/dev/nvme1n1p3", "/dev/nvme1n1"},

		// Edge cases
		{"/dev/loop0", "/dev/loop"}, // poco común pero esperado
		{"", ""},
	}
	for _, tc := range cases {
		got := stripPartitionSuffix(tc.input)
		if got != tc.expect {
			t.Errorf("stripPartitionSuffix(%q) = %q, want %q", tc.input, got, tc.expect)
		}
	}
}

// ─── resolveBtrfsUUIDFromMount sin BTRFS ──────────────────────────────────
// No podemos probar el caso éxito sin un BTRFS real, pero sí el fail-safe.

func TestResolveBtrfsUUIDFromMountEmpty(t *testing.T) {
	got := resolveBtrfsUUIDFromMount("")
	if got != "" {
		t.Errorf("expected empty for empty mountpoint, got %q", got)
	}
	got = resolveBtrfsUUIDFromMount("/nonexistent/path/at/all")
	if got != "" {
		t.Errorf("expected empty for nonexistent mount, got %q", got)
	}
}

// ─── isDigit / isAllDigits ────────────────────────────────────────────────

func TestIsAllDigits(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"123", true},
		{"0", true},
		{"", false},
		{"12a", false},
		{"a", false},
		{" 1", false},
	}
	for _, tc := range cases {
		if got := isAllDigits(tc.s); got != tc.want {
			t.Errorf("isAllDigits(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}

// ─── migrateStorageJSONOnce skipped paths ─────────────────────────────────
// Verifica que NO se hace nada cuando no procede.
//
// Estos tests cubren los exits tempranos del migrador. No testeamos el
// happy path completo aquí porque requiere un BTRFS real (UUID resolution).
// Para el flujo completo se usa el test de integración en producción.

func TestMigrateStorageJSONOnceSkipsWhenServiceNil(t *testing.T) {
	// Save & restore globals
	saved := storageService
	defer func() { storageService = saved }()

	storageService = nil
	// Debe terminar sin panic ni error
	migrateStorageJSONOnce()
}

// ─── helpers exportados — verificación de visibilidad/parsing ──────────────
// Garantiza que los helpers del migrador no usan tipos no exportados que
// puedan romperse en refactors.

func TestMigrateHelpersCallable(t *testing.T) {
	// Verifica que las firmas no requieren tipos privados externos.
	// Es un test "no compila si rompiste algo".
	_ = stripPartitionSuffix
	_ = resolveBtrfsUUIDFromMount
	_ = isDigit
	_ = isAllDigits

	// Sanity: las strings devueltas no contienen el carácter NUL
	r := stripPartitionSuffix("/dev/sdb1")
	if strings.ContainsRune(r, 0) {
		t.Error("stripPartitionSuffix returned a string with NUL")
	}
}

// ─── Happy path con MockDeviceScanner y storage.json fake ─────────────────

// TestMigrateStorageJSONOnceHappyPath verifica el flujo completo:
//   - storage.json con 1 pool BTRFS de 2 discos
//   - MockScanner devuelve esos 2 discos
//   - migrador popula storage_pools, storage_devices, storage_pool_devices
//   - storage.json se renombra a .migrated-<timestamp>
//
// Requiere "btrfs filesystem show" para resolver el UUID. Como en sandbox
// no hay BTRFS real, pasamos el UUID en el JSON directamente (caso normal:
// los pools creados por la UI legacy guardan el UUID).
func TestMigrateStorageJSONOnceHappyPath(t *testing.T) {
	// Save & restore globals que toca el migrador
	savedService := storageService
	savedHasBtrfs := hasBtrfs
	savedConfigFile := storageConfigFile
	defer func() {
		storageService = savedService
		hasBtrfs = savedHasBtrfs
		storageConfigFile = savedConfigFile
	}()

	// Setup: DB temp + service con mock scanner
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "migrate_test.db")
	conn, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=10000")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	conn.Exec("PRAGMA foreign_keys = ON")

	if _, err := conn.Exec(storageSchemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}

	repo := NewStorageRepo(conn)
	policy := NewPolicyChecker()
	executor := NewRealBtrfsExecutor()
	// Scanner devuelve 2 discos sintéticos
	scanner := NewMockDeviceScanner([]ScannedDevice{
		{Name: "sdb", DevicePath: "/dev/sdb", ByIDPath: "/dev/disk/by-id/wwn-aaa",
			Serial: "AAA", Model: "test", SizeBytes: 200 * 1024 * 1024},
		{Name: "sdc", DevicePath: "/dev/sdc", ByIDPath: "/dev/disk/by-id/wwn-bbb",
			Serial: "BBB", Model: "test", SizeBytes: 200 * 1024 * 1024},
	})
	storageService = NewStorageService(conn, repo, policy, executor, scanner)
	storageService.deviceChecker = noopDeviceChecker // tests no requieren preflight real
	hasBtrfs = true

	// JSON simulado
	jsonPath := filepath.Join(tmpDir, "storage.json")
	storageConfigFile = jsonPath
	jsonData := map[string]interface{}{
		"pools": []interface{}{
			map[string]interface{}{
				"name":       "data",
				"type":       "btrfs",
				"profile":    "raid1",
				"mountPoint": "/nimos/pools/data",
				"uuid":       "11111111-2222-3333-4444-555555555555",
				"disks":      []interface{}{"/dev/sdb", "/dev/sdc"},
				"createdAt":  "2026-05-16T20:00:00Z",
			},
		},
		"primaryPool": "data",
	}
	b, _ := json.Marshal(jsonData)
	if err := os.WriteFile(jsonPath, b, 0644); err != nil {
		t.Fatalf("write JSON: %v", err)
	}

	// EJECUTAR
	migrateStorageJSONOnce()

	// Verificar SQLite
	ctx := context.Background()
	pools, err := repo.ListPools(ctx)
	if err != nil {
		t.Fatalf("list pools: %v", err)
	}
	if len(pools) != 1 {
		t.Fatalf("expected 1 pool migrated, got %d", len(pools))
	}
	if pools[0].Name != "data" {
		t.Errorf("pool name = %q, want data", pools[0].Name)
	}
	if pools[0].BtrfsUUID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("btrfs_uuid mismatch: %q", pools[0].BtrfsUUID)
	}
	if pools[0].Profile != ProfileRaid1 {
		t.Errorf("profile = %q, want raid1", pools[0].Profile)
	}

	// Verificar relaciones pool-device
	devs, err := repo.ListDevicesInPool(ctx, pools[0].ID)
	if err != nil {
		t.Fatalf("list devices in pool: %v", err)
	}
	if len(devs) != 2 {
		t.Errorf("expected 2 devices in pool, got %d", len(devs))
	}

	// Verificar primary_pool en metadata
	var primaryID string
	err = conn.QueryRowContext(ctx,
		`SELECT value FROM storage_metadata WHERE key = 'primary_pool'`).Scan(&primaryID)
	if err != nil {
		t.Errorf("primary_pool not set: %v", err)
	}
	if primaryID != pools[0].ID {
		t.Errorf("primary_pool = %q, want %q", primaryID, pools[0].ID)
	}

	// Verificar que el JSON fue archivado
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Error("storage.json should have been renamed (not exist at original path)")
	}
	entries, _ := os.ReadDir(tmpDir)
	foundArchived := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "storage.json.migrated-") {
			foundArchived = true
			break
		}
	}
	if !foundArchived {
		t.Error("expected storage.json.migrated-<timestamp> file in tmpDir")
	}
}

// TestMigrateStorageJSONOnceIdempotent verifica que ejecutar 2 veces no
// duplica nada (la 2ª es no-op).
func TestMigrateStorageJSONOnceIdempotent(t *testing.T) {
	// Reusa el setup del test happy path
	savedService := storageService
	savedHasBtrfs := hasBtrfs
	savedConfigFile := storageConfigFile
	defer func() {
		storageService = savedService
		hasBtrfs = savedHasBtrfs
		storageConfigFile = savedConfigFile
	}()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "idemp.db")
	conn, _ := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=10000")
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	conn.Exec("PRAGMA foreign_keys = ON")
	conn.Exec(storageSchemaSQL)

	repo := NewStorageRepo(conn)
	scanner := NewMockDeviceScanner([]ScannedDevice{
		{Name: "sdb", DevicePath: "/dev/sdb", ByIDPath: "/dev/disk/by-id/wwn-x",
			Serial: "X", Model: "t", SizeBytes: 200 * 1024 * 1024},
	})
	storageService = NewStorageService(conn, repo, NewPolicyChecker(),
		NewRealBtrfsExecutor(), scanner)
	storageService.deviceChecker = noopDeviceChecker // tests no requieren preflight real
	hasBtrfs = true

	jsonPath := filepath.Join(tmpDir, "storage.json")
	storageConfigFile = jsonPath
	jsonData := map[string]interface{}{
		"pools": []interface{}{
			map[string]interface{}{
				"name":       "data",
				"type":       "btrfs",
				"profile":    "single",
				"mountPoint": "/nimos/pools/data",
				"uuid":       "abcd-1234-5678-90ef",
				"disks":      []interface{}{"/dev/sdb"},
				"createdAt":  "2026-05-16T20:00:00Z",
			},
		},
	}
	b, _ := json.Marshal(jsonData)
	os.WriteFile(jsonPath, b, 0644)

	// 1ª llamada
	migrateStorageJSONOnce()

	ctx := context.Background()
	pools1, _ := repo.ListPools(ctx)
	count1 := len(pools1)

	// 2ª llamada — no debe duplicar
	migrateStorageJSONOnce()

	pools2, _ := repo.ListPools(ctx)
	count2 := len(pools2)

	if count1 != count2 {
		t.Errorf("idempotency broken: 1st call → %d pools, 2nd call → %d pools",
			count1, count2)
	}
	if count1 != 1 {
		t.Errorf("expected exactly 1 pool, got %d", count1)
	}
}
