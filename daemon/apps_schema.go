// apps_schema.go — Schema SQLite del módulo de apps (Beta 8.1).
//
// El schema se embebe en el binario con //go:embed para que no haya que
// distribuir archivos externos. El daemon lo aplica al arranque dentro de
// initAppsSchema() llamado desde openDB().
//
// El schema es idempotente (todos los CREATE usan IF NOT EXISTS), así que
// se puede aplicar en cada arranque sin problemas.
//
// Diseñado por DOMINIO DE ENTIDAD:
//   docker_apps  → containers Docker
//   native_apps  → packages Linux
//
// app_registry queda en db.go (apps internas del SO + permisos), separación limpia.

package main

import (
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed apps_schema.sql
var appsSchemaSQL string

// initAppsSchema aplica el schema del módulo apps a la base de datos.
// Es idempotente: se puede llamar en cada arranque sin efectos secundarios.
//
// Estructura:
//
//  1. Aplicar schema base (CREATE TABLE IF NOT EXISTS) desde apps_schema.sql.
//  2. Aplicar migrations columnares (ALTER TABLE ADD COLUMN). SQLite devuelve
//     "duplicate column" si ya existe; ignoramos el error porque es esperado
//     cuando el daemon arranca sobre una DB ya migrada. Patrón usado también
//     en backup.go y db.go.
//
// Si el schema base falla al aplicarse, devuelve error y el daemon NO debe
// arrancar (es un estado inconsistente). Las migrations columnares NO abortan
// el arranque porque su fallo más probable es "ya existe", que es OK.
func initAppsSchema(db *sql.DB) error {
	if _, err := db.Exec(appsSchemaSQL); err != nil {
		return fmt.Errorf("apply apps schema: %w", err)
	}

	// Migration Batch 2 (Beta 8.1.x):
	//   APP-033 · ports_json para multi-port persistence
	//   APP-031 · deleting flag para race-free uninstall
	db.Exec(`ALTER TABLE docker_apps ADD COLUMN ports_json TEXT DEFAULT '[]'`)
	db.Exec(`ALTER TABLE docker_apps ADD COLUMN deleting INTEGER DEFAULT 0`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_docker_apps_deleting ON docker_apps(deleting)`)

	return nil
}
