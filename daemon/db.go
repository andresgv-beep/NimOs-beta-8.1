package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// ═══════════════════════════════════
// Database
// ═══════════════════════════════════

var db *sql.DB

const dbPath = "/var/lib/nimos/config/nimos.db"

func openDB() error {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("cannot create db directory: %v", err)
	}

	var err error
	// IMPORTANTE: usamos _pragma= (sintaxis de modernc.org/sqlite) en vez de
	// _busy_timeout= (sintaxis del driver CGO mattn, que este driver IGNORA).
	// Cada _pragma se ejecuta en CADA conexión nueva del pool · crítico para
	// que busy_timeout aplique a las 8 conexiones, no solo a una. Sin esto,
	// las conexiones sin timeout fallan con "database is locked" al instante.
	//
	//   journal_mode(WAL)    · lectores concurrentes + un escritor
	//   busy_timeout(10000)  · esperar 10s si hay contención, en vez de fallar
	//   foreign_keys(1)      · CASCADE/RESTRICT en tablas storage_*
	//   synchronous(NORMAL)  · seguro en WAL, escrituras más rápidas (menos lock)
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)" +
		"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err = sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("cannot open database: %v", err)
	}

	// ── Pool de conexiones · concurrencia con WAL ──
	//
	// Beta 8.2 (01/06/2026) · fix del cuello de botella de BD.
	//
	// PROBLEMA RESUELTO: antes había SetMaxOpenConns(1) · UNA sola conexión
	// para todo el daemon. Eso serializaba TODAS las operaciones de BD ·
	// lecturas incluidas. Durante operaciones largas (instalar app, scan de
	// devices cada 30s), NimHealth y los reconcilers se bloqueaban esperando
	// la única conexión. Síntomas en producción: "NimHealth muerto durante
	// instalaciones", apps colgadas en "Instalando...", ListPools con
	// "context canceled", y el daemon llegando a caerse bajo carga.
	//
	// SOLUCIÓN: WAL permite MÚLTIPLES LECTORES concurrentes + UN escritor.
	// Subimos MaxOpenConns para aprovecharlo · las lecturas (la mayoría de las
	// ops) corren en paralelo. Las escrituras las serializa SQLite (un
	// escritor) y busy_timeout=10s hace que esperen en vez de fallar.
	//
	// Los PRAGMAs van en el DSN (_pragma=) para que se apliquen a CADA
	// conexión del pool, no solo a una.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0) // conexiones a archivo local · no reciclar

	// Force foreign_keys ON explicitly. The query string `?_foreign_keys=ON`
	// is not honored by modernc.org/sqlite, so we set it via PRAGMA after
	// opening. Required for CASCADE/RESTRICT in storage_* tables to work.
	// see docs/storage_invariants.md#5.1
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("cannot enable foreign_keys: %v", err)
	}

	if err := createTables(); err != nil {
		return fmt.Errorf("cannot create tables: %v", err)
	}

	return nil
}

func createTables() error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		username     TEXT PRIMARY KEY,
		password     TEXT NOT NULL,
		role         TEXT NOT NULL DEFAULT 'user',
		description  TEXT DEFAULT '',
		totp_secret  TEXT DEFAULT '',
		totp_enabled INTEGER DEFAULT 0,
		backup_codes TEXT DEFAULT '',
		created_at   TEXT NOT NULL,
		updated_at   TEXT
	);

	CREATE TABLE IF NOT EXISTS sessions (
		token        TEXT PRIMARY KEY,
		username     TEXT NOT NULL,
		role         TEXT NOT NULL,
		created_at   INTEGER NOT NULL,
		expires_at   INTEGER NOT NULL,
		ip           TEXT DEFAULT '',
		FOREIGN KEY (username) REFERENCES users(username) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS shares (
		name         TEXT PRIMARY KEY,
		display_name TEXT NOT NULL,
		description  TEXT DEFAULT '',
		path         TEXT NOT NULL UNIQUE,
		volume       TEXT NOT NULL,
		pool         TEXT NOT NULL,
		recycle_bin  INTEGER DEFAULT 1,
		created_by   TEXT NOT NULL,
		created_at   TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS share_permissions (
		share_name   TEXT NOT NULL,
		username     TEXT NOT NULL,
		permission   TEXT NOT NULL,
		PRIMARY KEY (share_name, username),
		FOREIGN KEY (share_name) REFERENCES shares(name) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS app_permissions (
		share_name   TEXT NOT NULL,
		app_id       TEXT NOT NULL,
		uid          INTEGER NOT NULL,
		permission   TEXT NOT NULL,
		PRIMARY KEY (share_name, app_id),
		FOREIGN KEY (share_name) REFERENCES shares(name) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS user_app_access (
		username     TEXT NOT NULL,
		app_id       TEXT NOT NULL,
		permission   TEXT NOT NULL DEFAULT 'use',
		granted_by   TEXT NOT NULL,
		granted_at   TEXT NOT NULL,
		PRIMARY KEY (username, app_id),
		FOREIGN KEY (username) REFERENCES users(username) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS preferences (
		username     TEXT NOT NULL,
		key          TEXT NOT NULL,
		value        TEXT NOT NULL,
		PRIMARY KEY (username, key)
	);

	CREATE INDEX IF NOT EXISTS idx_sessions_username ON sessions(username);
	CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
	CREATE INDEX IF NOT EXISTS idx_share_perms_user ON share_permissions(username);
	CREATE INDEX IF NOT EXISTS idx_preferences_user ON preferences(username);
	`
	_, err := db.Exec(schema)
	if err != nil {
		return err
	}

	// Migration: add backup_codes column if it doesn't exist
	db.Exec(`ALTER TABLE users ADD COLUMN backup_codes TEXT DEFAULT ''`)

	// Migration: create backup tables
	if err := createBackupTables(); err != nil {
		return fmt.Errorf("backup tables: %v", err)
	}

	// Create notification table
	if err := createNotificationTable(); err != nil {
		return fmt.Errorf("notification table: %v", err)
	}

	// Create app registry table
	if err := createAppRegistryTable(); err != nil {
		return fmt.Errorf("app registry table: %v", err)
	}

	// Create service registry tables
	if err := createServiceRegistryTables(); err != nil {
		return fmt.Errorf("service registry tables: %v", err)
	}

	// Create download tokens table (CRIT-008)
	if err := createDownloadTokensTable(); err != nil {
		return fmt.Errorf("download tokens table: %v", err)
	}

	// ── Apps module (Beta 8.1) ─────────────────────────────
	// Schema: docker_apps + native_apps (separado de app_registry).
	// Repo: AppsRepo gestiona CRUD; vive en db_apps.go.
	// Tests: db_apps_test.go valida el contrato del repo.
	if err := initAppsSchema(db); err != nil {
		return fmt.Errorf("apps schema: %v", err)
	}
	appsRepo = NewAppsRepo(db)
	// AppImagesRepo · sprint Updates (25/05/2026)
	// Tracking de digests de imágenes Docker · detección de actualizaciones.
	// Schema en apps_schema.sql · tabla docker_app_images.
	appImagesRepo = NewAppImagesRepo(db)

	// ── Operations module (Beta 8.1.x · APP-012) ─────────────
	// Schema: nimos_operations · async ops tracking.
	// Repo: OperationsRepo gestiona CRUD + state machine; vive en db_operations.go.
	// Sin consumidores hasta Fase 2 Batch 3 (dockerInstall async, dockerPull async).
	if err := initOperationsSchema(db); err != nil {
		return fmt.Errorf("operations schema: %v", err)
	}
	operationsRepo = NewOperationsRepo(db)

	// ── Schema migrations (versioned) ──
	runSchemaMigrations()

	return nil
}

// runSchemaMigrations applies versioned migrations.
// Each migration runs once and bumps user_version.
func runSchemaMigrations() {
	var version int
	db.QueryRow("PRAGMA user_version").Scan(&version)

	if version < 1 {
		// v1: Extend app_registry with type and managed_by columns
		db.Exec(`ALTER TABLE app_registry ADD COLUMN type TEXT DEFAULT 'ui'`)
		db.Exec(`ALTER TABLE app_registry ADD COLUMN managed_by TEXT DEFAULT 'none'`)

		// Update existing apps with correct type and managed_by
		updates := []struct {
			id, appType, managedBy string
		}{
			{"nimsettings", "ui", "none"},
			{"storage", "system", "internal"},
			{"network", "system", "internal"},
			{"nimtorrent", "daemon", "systemd"},
			{"appstore", "ui", "none"},
			{"files", "ui", "none"},
			{"mediaplayer", "ui", "none"},
			{"terminal", "ui", "none"},
			{"containers", "docker", "docker"},
			{"monitor", "ui", "none"},
			{"vms", "system", "internal"},
			{"texteditor", "ui", "none"},
		}
		for _, u := range updates {
			db.Exec(`UPDATE app_registry SET type = ?, managed_by = ? WHERE id = ?`,
				u.appType, u.managedBy, u.id)
		}

		// Add nimbackup if not present
		db.Exec(`INSERT OR IGNORE INTO app_registry (id, name, category, admin_only, public, type, managed_by)
			VALUES ('nimbackup', 'NimBackup', 'system', 0, 0, 'daemon', 'internal')`)

		db.Exec("PRAGMA user_version = 1")
		logMsg("schema: migrated to version 1 (app_registry extended, service registry)")
	}

	if version < 2 {
		// v2: Add NimHealth app to registry
		db.Exec(`INSERT OR IGNORE INTO app_registry (id, name, category, admin_only, public, type, managed_by)
			VALUES ('nimhealth', 'NimHealth', 'system', 0, 0, 'ui', 'none')`)
		db.Exec("PRAGMA user_version = 2")
		logMsg("schema: migrated to version 2 (nimhealth app)")
	}

	if version < 3 {
		// v3: Normalize HealthStatus vocabulary to the 6 official states
		// (disciplina §6). Adds CHECK constraints on status+health, adds
		// last_observed_at, drops unused 'optional' dependency level.
		//
		// Mapeo old→new:
		//   status:  failed → error
		//   health:  unreachable → failed
		//            unhealthy   → failed
		//            idle        → healthy  (engine OK, just no containers)
		//
		// TRANSACCIONAL: o se aplica entera o nada. Si falla por la mitad,
		// rollback automático y service_instances queda intacta.
		if err := migrateToV3(); err != nil {
			logMsg("schema: ERROR migrating to v3: %v · service_instances unchanged", err)
		} else {
			db.Exec("PRAGMA user_version = 3")
			logMsg("schema: migrated to version 3 (HealthStatus normalized)")
		}
	}

	// Future migrations go here:
	// if version < 4 { ... db.Exec("PRAGMA user_version = 4") }
}

// migrateToV3 ejecuta la migración v3 en una transacción.
// Devuelve error si cualquier paso falla; SQLite hace rollback automático.
func migrateToV3() error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() // no-op si Commit() tuvo éxito

	stmts := []string{
		// 1. Nueva tabla con CHECK constraints + last_observed_at
		`CREATE TABLE service_instances_v3 (
			id               TEXT PRIMARY KEY,
			app_id           TEXT NOT NULL,
			pool_name        TEXT NOT NULL,
			path             TEXT NOT NULL,
			status           TEXT CHECK (status IN
			                   ('running','stopped','starting','stopping','error','unknown'))
			                   DEFAULT 'unknown',
			health           TEXT CHECK (health IN
			                   ('healthy','degraded','failed','partial','unknown','stale'))
			                   DEFAULT 'unknown',
			owner            TEXT DEFAULT 'system',
			config           TEXT DEFAULT '{}',
			created_at       TEXT NOT NULL,
			updated_at       TEXT NOT NULL,
			last_observed_at TEXT,
			FOREIGN KEY (app_id) REFERENCES app_registry(id)
		)`,

		// 2. Copiar datos con mapeo CASE
		`INSERT INTO service_instances_v3
			(id, app_id, pool_name, path, status, health, owner, config,
			 created_at, updated_at, last_observed_at)
		SELECT
			id, app_id, pool_name, path,
			CASE status
				WHEN 'running'  THEN 'running'
				WHEN 'stopped'  THEN 'stopped'
				WHEN 'starting' THEN 'starting'
				WHEN 'stopping' THEN 'stopping'
				WHEN 'failed'   THEN 'error'
				WHEN 'error'    THEN 'error'
				ELSE 'unknown'
			END AS status,
			CASE health
				WHEN 'healthy'     THEN 'healthy'
				WHEN 'degraded'    THEN 'degraded'
				WHEN 'unreachable' THEN 'failed'
				WHEN 'unhealthy'   THEN 'failed'
				WHEN 'idle'        THEN 'healthy'
				WHEN 'failed'      THEN 'failed'
				WHEN 'partial'     THEN 'partial'
				WHEN 'stale'       THEN 'stale'
				WHEN 'incomplete'  THEN 'unknown'  -- NimHealth no usa este valor
				ELSE 'unknown'
			END AS health,
			owner, config, created_at, updated_at, NULL
		FROM service_instances`,

		// 3. Swap tablas
		`DROP TABLE service_instances`,
		`ALTER TABLE service_instances_v3 RENAME TO service_instances`,

		// 4. Recrear índices (perdidos al hacer DROP/RENAME)
		`CREATE INDEX IF NOT EXISTS idx_si_pool ON service_instances(pool_name)`,
		`CREATE INDEX IF NOT EXISTS idx_si_status ON service_instances(status)`,

		// 5. service_dependencies: 'optional' → 'soft' (nivel no usado en código)
		`UPDATE service_dependencies SET required='soft' WHERE required='optional'`,
	}

	for i, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}

	return tx.Commit()
}

// ═══════════════════════════════════
// Migration from JSON files
// ═══════════════════════════════════

type jsonUser struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	Role        string `json:"role"`
	Description string `json:"description"`
	TotpSecret  string `json:"totpSecret"`
	TotpEnabled bool   `json:"totpEnabled"`
	Created     string `json:"created"`
}

type jsonShare struct {
	Name           string            `json:"name"`
	DisplayName    string            `json:"displayName"`
	Description    string            `json:"description"`
	Path           string            `json:"path"`
	Volume         string            `json:"volume"`
	Pool           string            `json:"pool"`
	RecycleBin     bool              `json:"recycleBin"`
	CreatedBy      string            `json:"createdBy"`
	Created        string            `json:"created"`
	Permissions    map[string]string `json:"permissions"`
	AppPermissions []json.RawMessage `json:"appPermissions"`
}

type jsonSession struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Created  int64  `json:"created"`
}

func migrateFromJSON() {
	migratedAny := false

	// Migrate users
	if data, err := os.ReadFile(usersFile); err == nil {
		var count int
		db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
		if count == 0 {
			var users []jsonUser
			if err := json.Unmarshal(data, &users); err == nil {
				tx, _ := db.Begin()
				for _, u := range users {
					totpEnabled := 0
					if u.TotpEnabled {
						totpEnabled = 1
					}
					tx.Exec(`INSERT OR IGNORE INTO users (username, password, role, description, totp_secret, totp_enabled, created_at)
						VALUES (?, ?, ?, ?, ?, ?, ?)`,
						u.Username, u.Password, u.Role, u.Description, u.TotpSecret, totpEnabled, u.Created)
				}
				tx.Commit()
				logMsg("  migration: imported %d users from JSON", len(users))
				migratedAny = true
			}
		}
	}

	// Migrate shares
	if data, err := os.ReadFile(sharesFile); err == nil {
		var count int
		db.QueryRow("SELECT COUNT(*) FROM shares").Scan(&count)
		if count == 0 {
			var shares []jsonShare
			if err := json.Unmarshal(data, &shares); err == nil {
				tx, _ := db.Begin()
				for _, s := range shares {
					recycleBin := 0
					if s.RecycleBin {
						recycleBin = 1
					}
					tx.Exec(`INSERT OR IGNORE INTO shares (name, display_name, description, path, volume, pool, recycle_bin, created_by, created_at)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
						s.Name, s.DisplayName, s.Description, s.Path, s.Volume, s.Pool, recycleBin, s.CreatedBy, s.Created)

					for username, perm := range s.Permissions {
						tx.Exec(`INSERT OR IGNORE INTO share_permissions (share_name, username, permission)
							VALUES (?, ?, ?)`, s.Name, username, perm)
					}
				}
				tx.Commit()
				logMsg("  migration: imported %d shares from JSON", len(shares))
				migratedAny = true
			}
		}
	}

	// Migrate sessions
	sessionsFile := filepath.Join(filepath.Dir(usersFile), "sessions.json")
	if data, err := os.ReadFile(sessionsFile); err == nil {
		var count int
		db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count)
		if count == 0 {
			var sessions map[string]jsonSession
			if err := json.Unmarshal(data, &sessions); err == nil {
				tx, _ := db.Begin()
				imported := 0
				now := time.Now().UnixMilli()
				for token, s := range sessions {
					expiresAt := s.Created + sessionExpiryMs
					if expiresAt > now {
						tx.Exec(`INSERT OR IGNORE INTO sessions (token, username, role, created_at, expires_at)
							VALUES (?, ?, ?, ?, ?)`, token, s.Username, s.Role, s.Created, expiresAt)
						imported++
					}
				}
				tx.Commit()
				logMsg("  migration: imported %d active sessions from JSON", imported)
				migratedAny = true
			}
		}
	}

	// Rename old JSON files — Node.js now reads from SQLite via daemon
	if migratedAny {
		for _, f := range []string{usersFile, sharesFile, sessionsFile} {
			if _, err := os.Stat(f); err == nil {
				os.Rename(f, f+".migrated")
			}
		}
		logMsg("  migration: JSON files renamed to .migrated")
	}
}

// ═══════════════════════════════════
// User operations
// ═══════════════════════════════════

// dbUsersListRaw returns typed user summaries from the DB.
func dbUsersListRaw() ([]DBUserSummary, error) {
	rows, err := db.Query(`SELECT username, role, description, totp_enabled, created_at FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []DBUserSummary
	for rows.Next() {
		var u DBUserSummary
		var totpEnabled int
		rows.Scan(&u.Username, &u.Role, &u.Description, &totpEnabled, &u.CreatedAt)
		u.TotpEnabled = totpEnabled == 1
		users = append(users, u)
	}
	if users == nil {
		users = []DBUserSummary{}
	}
	return users, nil
}

// dbUsersGetRaw returns a typed DBUser struct.
func dbUsersGetRaw(username string) (*DBUser, error) {
	var u DBUser
	u.Username = username
	var totpEnabled int
	var backupCodesJSON string
	var updatedAt sql.NullString
	err := db.QueryRow(`SELECT password, role, description, totp_secret, totp_enabled, backup_codes, created_at, updated_at FROM users WHERE username = ?`, username).
		Scan(&u.Password, &u.Role, &u.Description, &u.TotpSecret, &totpEnabled, &backupCodesJSON, &u.CreatedAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("user not found: %s", username)
	}
	u.TotpEnabled = totpEnabled == 1

	// Parse backup codes JSON array
	if backupCodesJSON != "" {
		var codes []interface{}
		if json.Unmarshal([]byte(backupCodesJSON), &codes) == nil {
			u.BackupCodes = codes
		}
	}

	return &u, nil
}

func dbUsersCreate(username, password, role, description string) error {
	_, err := db.Exec(`INSERT INTO users (username, password, role, description, created_at) VALUES (?, ?, ?, ?, ?)`,
		username, password, role, description, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func dbUsersUpdate(username string, u UserUpdate) error {
	sets := []string{}
	args := []interface{}{}
	if u.Password != nil {
		sets = append(sets, "password = ?")
		args = append(args, *u.Password)
	}
	if u.Role != nil {
		sets = append(sets, "role = ?")
		args = append(args, *u.Role)
	}
	if u.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *u.Description)
	}
	if u.TotpSecret != nil {
		sets = append(sets, "totp_secret = ?")
		args = append(args, *u.TotpSecret)
	}
	if u.TotpEnabled != nil {
		sets = append(sets, "totp_enabled = ?")
		if *u.TotpEnabled {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	if u.BackupCodes != nil {
		sets = append(sets, "backup_codes = ?")
		jsonData, _ := json.Marshal(u.BackupCodes)
		args = append(args, string(jsonData))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Format(time.RFC3339Nano))
	args = append(args, username)

	query := "UPDATE users SET " + joinStrings(sets, ", ") + " WHERE username = ?"
	_, err := db.Exec(query, args...)
	return err
}

func dbSessionsDeleteByUsername(username string) {
	db.Exec(`DELETE FROM sessions WHERE username = ?`, username)
}

func dbUsersDelete(username string) error {
	_, err := db.Exec(`DELETE FROM users WHERE username = ?`, username)
	return err
}

func dbUsersVerifyPassword(username string) (string, error) {
	var pwd string
	err := db.QueryRow(`SELECT password FROM users WHERE username = ?`, username).Scan(&pwd)
	if err != nil {
		return "", fmt.Errorf("user not found: %s", username)
	}
	return pwd, nil
}

// ═══════════════════════════════════
// Session operations
// ═══════════════════════════════════

const sessionExpiryMs int64 = 24 * 60 * 60 * 1000 // 24 hours (sliding — renewed on each request)

func dbSessionCreate(token, username, role, ip string) error {
	now := time.Now().UnixMilli()
	expires := now + sessionExpiryMs
	_, err := db.Exec(`INSERT OR REPLACE INTO sessions (token, username, role, created_at, expires_at, ip) VALUES (?, ?, ?, ?, ?, ?)`,
		token, username, role, now, expires, ip)
	return err
}

func dbSessionGet(token string) (*DBSession, error) {
	var s DBSession
	err := db.QueryRow(`SELECT username, role, created_at, expires_at, ip FROM sessions WHERE token = ?`, token).
		Scan(&s.Username, &s.Role, &s.CreatedAt, &s.ExpiresAt, &s.IP)
	if err != nil {
		return nil, fmt.Errorf("session not found")
	}
	if time.Now().UnixMilli() > s.ExpiresAt {
		db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
		return nil, fmt.Errorf("session expired")
	}
	// Sliding expiry: renew on each use so active users stay logged in
	newExpiry := time.Now().UnixMilli() + sessionExpiryMs
	db.Exec(`UPDATE sessions SET expires_at = ? WHERE token = ?`, newExpiry, token)
	return &s, nil
}

func dbSessionDelete(token string) error {
	_, err := db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func dbSessionCleanup() int64 {
	now := time.Now().UnixMilli()
	result, _ := db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now)
	n, _ := result.RowsAffected()
	// Also clean expired download tokens
	db.Exec(`DELETE FROM download_tokens WHERE expires_at < ?`, now)
	return n
}

// ═══════════════════════════════════
// Download tokens (CRIT-008: short-lived, one-time-use)
// ═══════════════════════════════════

func createDownloadTokensTable() error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS download_tokens (
		token      TEXT PRIMARY KEY,
		username   TEXT NOT NULL,
		role       TEXT NOT NULL,
		share      TEXT NOT NULL,
		path       TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	)`)
	return err
}

const downloadTokenExpiryMs int64 = 60 * 1000 // 60 seconds

func dbDownloadTokenCreate(username, role, share, path string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	hashed := sha256Hex(token)
	now := time.Now().UnixMilli()
	expires := now + downloadTokenExpiryMs
	_, err := db.Exec(`INSERT INTO download_tokens (token, username, role, share, path, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hashed, username, role, share, path, now, expires)
	if err != nil {
		return "", err
	}
	return token, nil
}

// dbDownloadTokenConsume validates and deletes (one-time-use) a download token.
// Returns username, role, share, path if valid.
func dbDownloadTokenConsume(rawToken string) (string, string, string, string, error) {
	hashed := sha256Hex(rawToken)
	var username, role, share, path string
	var expiresAt int64
	err := db.QueryRow(`SELECT username, role, share, path, expires_at FROM download_tokens WHERE token = ?`, hashed).
		Scan(&username, &role, &share, &path, &expiresAt)
	if err != nil {
		return "", "", "", "", fmt.Errorf("invalid download token")
	}
	// Always delete (one-time-use)
	db.Exec(`DELETE FROM download_tokens WHERE token = ?`, hashed)
	if time.Now().UnixMilli() > expiresAt {
		return "", "", "", "", fmt.Errorf("download token expired")
	}
	return username, role, share, path, nil
}

// ═══════════════════════════════════
// Share operations (data layer)
// ═══════════════════════════════════

// dbSharesListRaw returns typed share structs from the DB.
// This is the primary query — other functions build on top of it.
func dbSharesListRaw() ([]DBShare, error) {
	rows, err := db.Query(`SELECT name, display_name, description, path, volume, pool, recycle_bin, created_by, created_at FROM shares ORDER BY created_at`)
	if err != nil {
		return nil, err
	}

	// Collect rows first, then close before subqueries
	type shareRow struct {
		DBShare
		recycleBinInt int
	}
	var shareRows []shareRow
	for rows.Next() {
		var s shareRow
		rows.Scan(&s.Name, &s.DisplayName, &s.Description, &s.Path, &s.Volume, &s.Pool, &s.recycleBinInt, &s.CreatedBy, &s.CreatedAt)
		s.RecycleBin = s.recycleBinInt == 1
		shareRows = append(shareRows, s)
	}
	rows.Close()

	var shares []DBShare
	for _, sr := range shareRows {
		s := sr.DBShare
		s.Permissions = map[string]string{}

		prows, _ := db.Query(`SELECT username, permission FROM share_permissions WHERE share_name = ?`, s.Name)
		if prows != nil {
			for prows.Next() {
				var u, p string
				prows.Scan(&u, &p)
				s.Permissions[u] = p
			}
			prows.Close()
		}

		arows, _ := db.Query(`SELECT app_id, uid, permission FROM app_permissions WHERE share_name = ?`, s.Name)
		if arows != nil {
			for arows.Next() {
				var ap AppPermission
				arows.Scan(&ap.AppId, &ap.Uid, &ap.Permission)
				s.AppPermissions = append(s.AppPermissions, ap)
			}
			arows.Close()
		}
		if s.AppPermissions == nil {
			s.AppPermissions = []AppPermission{}
		}

		shares = append(shares, s)
	}
	if shares == nil {
		shares = []DBShare{}
	}
	return shares, nil
}

// dbSharesGetRaw returns a single typed share struct.
func dbSharesGetRaw(name string) (*DBShare, error) {
	raw, err := dbSharesListRaw()
	if err != nil {
		return nil, err
	}
	for _, s := range raw {
		if s.Name == name {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("share not found: %s", name)
}

func dbSharesCreate(name, displayName, desc, path, volume, pool, createdBy string) error {
	_, err := db.Exec(`INSERT INTO shares (name, display_name, description, path, volume, pool, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		name, displayName, desc, path, volume, pool, createdBy, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func dbSharesUpdate(name string, u ShareUpdate) error {
	sets := []string{}
	args := []interface{}{}
	if u.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *u.Description)
	}
	if u.RecycleBin != nil {
		sets = append(sets, "recycle_bin = ?")
		if *u.RecycleBin {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, name)
	query := "UPDATE shares SET " + joinStrings(sets, ", ") + " WHERE name = ?"
	_, err := db.Exec(query, args...)
	return err
}

func dbSharesDelete(name string) error {
	_, err := db.Exec(`DELETE FROM shares WHERE name = ?`, name)
	return err
}

func dbShareSetPermission(shareName, username, permission string) error {
	if permission == "none" || permission == "" {
		_, err := db.Exec(`DELETE FROM share_permissions WHERE share_name = ? AND username = ?`, shareName, username)
		return err
	}
	_, err := db.Exec(`INSERT OR REPLACE INTO share_permissions (share_name, username, permission) VALUES (?, ?, ?)`,
		shareName, username, permission)
	return err
}

func dbShareSetAppPermission(shareName, appId string, uid int, permission string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO app_permissions (share_name, app_id, uid, permission) VALUES (?, ?, ?, ?)`,
		shareName, appId, uid, permission)
	return err
}

func dbShareRemoveAppPermission(shareName, appId string) error {
	_, err := db.Exec(`DELETE FROM app_permissions WHERE share_name = ? AND app_id = ?`, shareName, appId)
	return err
}

// ═══════════════════════════════════
// Preferences operations
// ═══════════════════════════════════

func dbPrefsSet(username, key, value string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO preferences (username, key, value) VALUES (?, ?, ?)`,
		username, key, value)
	return err
}

func dbPrefsDelete(username, key string) error {
	_, err := db.Exec(`DELETE FROM preferences WHERE username = ? AND key = ?`, username, key)
	return err
}

// ═══════════════════════════════════
// App Registry — stored in DB, not hardcoded
// ═══════════════════════════════════

func createAppRegistryTable() error {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS app_registry (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		category    TEXT NOT NULL DEFAULT 'app',
		admin_only  INTEGER DEFAULT 0,
		public      INTEGER DEFAULT 0
	);`)
	if err != nil {
		return err
	}

	// Seed default apps if table is empty
	var count int
	db.QueryRow("SELECT COUNT(*) FROM app_registry").Scan(&count)
	if count == 0 {
		tx, _ := db.Begin()
		seedApps := []struct {
			id, name, category string
			adminOnly, public  int
		}{
			{"nimsettings", "NimSettings", "system", 0, 0},
			{"storage", "Storage", "system", 1, 0},
			{"network", "Network", "system", 1, 0},
			{"nimtorrent", "NimTorrent", "app", 0, 0},
			{"appstore", "App Store", "system", 0, 0},
			{"files", "Files", "app", 0, 1},
			{"mediaplayer", "Media Player", "app", 0, 1},
			{"terminal", "Terminal", "system", 0, 0},
			{"containers", "Containers", "system", 0, 0},
			{"monitor", "System Monitor", "system", 0, 0},
			{"vms", "Virtual Machines", "system", 0, 0},
			{"texteditor", "Text Editor", "app", 0, 0},
		}
		for _, a := range seedApps {
			tx.Exec(`INSERT OR IGNORE INTO app_registry (id, name, category, admin_only, public) VALUES (?, ?, ?, ?, ?)`,
				a.id, a.name, a.category, a.adminOnly, a.public)
		}
		tx.Commit()
		logMsg("app_registry: seeded %d default apps", len(seedApps))
	}
	return nil
}

// isPublicApp checks if an app is accessible to all authenticated users
func isPublicApp(appId string) bool {
	var pub int
	err := db.QueryRow(`SELECT public FROM app_registry WHERE id = ?`, appId).Scan(&pub)
	if err != nil {
		return false
	}
	return pub == 1
}

// isAdminOnlyApp checks if an app is restricted to admin users
func isAdminOnlyApp(appId string) bool {
	var adminOnly int
	err := db.QueryRow(`SELECT admin_only FROM app_registry WHERE id = ?`, appId).Scan(&adminOnly)
	if err != nil {
		return false
	}
	return adminOnly == 1
}

// dbListAppRegistry returns all registered apps for the admin panel
func dbListAppRegistry() ([]DBAppRegistryEntry, error) {
	rows, err := db.Query(`SELECT id, name, category, admin_only, public FROM app_registry ORDER BY category, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DBAppRegistryEntry
	for rows.Next() {
		var a DBAppRegistryEntry
		var adminOnly, public int
		rows.Scan(&a.Id, &a.Name, &a.Category, &adminOnly, &public)
		a.AdminOnly = adminOnly == 1
		a.Public = public == 1
		result = append(result, a)
	}
	if result == nil {
		result = []DBAppRegistryEntry{}
	}
	return result, nil
}

// Check if a user has access to an app
// Admin always has access. Public apps are always accessible.
// For everything else, check user_app_access table.
func dbUserHasAppAccess(username, role, appId string) bool {
	if role == "admin" {
		return true
	}
	if isPublicApp(appId) {
		return true
	}
	if isAdminOnlyApp(appId) {
		return false
	}
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM user_app_access WHERE username = ? AND app_id = ?`, username, appId).Scan(&count)
	if err != nil {
		return false
	}
	return count > 0
}

// Get permission level for a user+app ('use', 'manage', or '')
func dbUserGetAppPermission(username, role, appId string) string {
	if role == "admin" {
		return "manage"
	}
	if isPublicApp(appId) {
		return "use"
	}
	if isAdminOnlyApp(appId) {
		return ""
	}
	var perm string
	err := db.QueryRow(`SELECT permission FROM user_app_access WHERE username = ? AND app_id = ?`, username, appId).Scan(&perm)
	if err != nil {
		return ""
	}
	return perm
}

// List all app access for a user
func dbUserListAppAccess(username string) ([]DBAppGrant, error) {
	rows, err := db.Query(`SELECT app_id, permission, granted_by, granted_at FROM user_app_access WHERE username = ? ORDER BY app_id`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DBAppGrant
	for rows.Next() {
		g := DBAppGrant{Username: username}
		rows.Scan(&g.AppId, &g.Permission, &g.GrantedBy, &g.GrantedAt)
		result = append(result, g)
	}
	if result == nil {
		result = []DBAppGrant{}
	}
	return result, nil
}

// List all app access entries (for admin panel)
func dbAppAccessListAll() ([]DBAppGrant, error) {
	rows, err := db.Query(`SELECT username, app_id, permission, granted_by, granted_at FROM user_app_access ORDER BY username, app_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DBAppGrant
	for rows.Next() {
		var g DBAppGrant
		rows.Scan(&g.Username, &g.AppId, &g.Permission, &g.GrantedBy, &g.GrantedAt)
		result = append(result, g)
	}
	if result == nil {
		result = []DBAppGrant{}
	}
	return result, nil
}

// Grant app access
func dbAppAccessGrant(username, appId, permission, grantedBy string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO user_app_access (username, app_id, permission, granted_by, granted_at) VALUES (?, ?, ?, ?, ?)`,
		username, appId, permission, grantedBy, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// Revoke app access
func dbAppAccessRevoke(username, appId string) error {
	_, err := db.Exec(`DELETE FROM user_app_access WHERE username = ? AND app_id = ?`, username, appId)
	return err
}

// Revoke all app access for a user
func dbAppAccessRevokeAll(username string) error {
	_, err := db.Exec(`DELETE FROM user_app_access WHERE username = ?`, username)
	return err
}

// ═══════════════════════════════════
// Helpers
// ═══════════════════════════════════

func joinStrings(parts []string, sep string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}
