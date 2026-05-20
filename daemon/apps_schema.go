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
// Si el schema falla al aplicarse, devuelve error y el daemon NO debe
// arrancar (es un estado inconsistente).
func initAppsSchema(db *sql.DB) error {
	if _, err := db.Exec(appsSchemaSQL); err != nil {
		return fmt.Errorf("apply apps schema: %w", err)
	}
	return nil
}
