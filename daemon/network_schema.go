// network_schema.go — Schema SQLite del módulo network (Beta 8 v4).
//
// El schema se embebe en el binario con //go:embed para que no haya que
// distribuir archivos externos. El daemon lo aplica al arranque dentro
// de initNetworkSchema(), llamado desde main.go DESPUÉS de
// initNimosCoreSchema() (las tablas nimos_secrets y nimos_breakers son
// dependencia: network_ddns tiene FK a nimos_secrets).
//
// El schema es idempotente (todos los CREATE usan IF NOT EXISTS). Se
// puede aplicar en cada arranque sin efectos secundarios.
//
// Tablas que contiene:
//   · network_ports       — puertos del daemon (triple generation)
//   · network_ddns        — DDNS configs (FK a nimos_secrets)
//   · network_observed    — snapshots históricos del observer
//   · network_operations  — operaciones auditables (triggered_by + request_id)
//   · network_events      — log auditable con dedupe + categorías
//
// Para inspeccionar fuera de Go: ver el archivo .sql.

package main

import (
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed network_schema.sql
var networkSchemaSQL string

// initNetworkSchema aplica el schema del módulo network a la base de
// datos. Es idempotente.
//
// PRECONDICIÓN: nimos_core_schema debe estar ya aplicado, porque
// network_ddns tiene FK a nimos_secrets(id). Si se llama antes, la
// FK fallará en CREATE TABLE.
//
// El parámetro conn permite pasar tanto el `db` global como una conexión
// temporal para tests. En producción se llama con `db`.
//
// Si el schema falla al aplicarse, devuelve error y el daemon NO debe
// continuar (sin tablas network el módulo no funciona).
func initNetworkSchema(conn *sql.DB) error {
	if conn == nil {
		return fmt.Errorf("initNetworkSchema: conn is nil")
	}

	// Verificación defensiva: foreign keys deben estar activadas.
	// Sin esto, la FK network_ddns → nimos_secrets es decorativa.
	var fkEnabled int
	if err := conn.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled); err != nil {
		return fmt.Errorf("cannot read foreign_keys pragma: %v", err)
	}
	if fkEnabled != 1 {
		return fmt.Errorf("foreign_keys is OFF (%d). Must be ON before applying network schema", fkEnabled)
	}

	// Verificación defensiva: nimos_secrets debe existir (precondición).
	// Mejor mensaje de error que un constraint failure críptico.
	var hasSecrets int
	if err := conn.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'nimos_secrets'
	`).Scan(&hasSecrets); err != nil {
		return fmt.Errorf("cannot check nimos_secrets existence: %v", err)
	}
	if hasSecrets == 0 {
		return fmt.Errorf("nimos_secrets table missing — call initNimosCoreSchema first")
	}

	if _, err := conn.Exec(networkSchemaSQL); err != nil {
		return fmt.Errorf("cannot apply network schema: %v", err)
	}
	// Migraciones columnares idempotentes (patrón del proyecto: SQLite
	// devuelve "duplicate column" si ya existe — se ignora). Para DBs
	// creadas antes de que la config de exposición tuviera puertos.
	conn.Exec(`ALTER TABLE network_exposure_config ADD COLUMN http_port INTEGER NOT NULL DEFAULT 80`)
	conn.Exec(`ALTER TABLE network_exposure_config ADD COLUMN https_port INTEGER NOT NULL DEFAULT 443`)

	return nil
}
