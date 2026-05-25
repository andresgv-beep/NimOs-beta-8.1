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
//
//	docker_apps  → containers Docker
//	native_apps  → packages Linux
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
// Estructura (orden estricto, justificado por DISCIPLINE §1 robustez):
//
//  1. Defensive cleanup pre-schema · DROP de índices que referencian columnas
//     que pueden no existir en versiones intermedias. Sin esto, un daemon que
//     crashea entre el ALTER y el CREATE INDEX puede dejar el sistema en estado
//     irrecuperable (el INDEX nuevo apunta a una columna fantasma).
//
//  2. Schema base (CREATE TABLE IF NOT EXISTS) desde apps_schema.sql.
//
//  3. Migrations columnares (ALTER TABLE ADD COLUMN). SQLite devuelve
//     "duplicate column" si ya existe; ignoramos el error porque es esperado
//     cuando el daemon arranca sobre una DB ya migrada.
//
//  4. Re-creación de índices que dependen de columnas migradas. Solo aquí,
//     porque las columnas ya están garantizadas.
//
//  5. Verificación post-migration · si la columna crítica no existe tras todo
//     esto, abortamos con error claro en vez de dejar al daemon en estado
//     zombi (esto evita crash loops con error genérico).
//
// Si el schema base falla al aplicarse, devuelve error y el daemon NO debe
// arrancar (es un estado inconsistente). Las migrations columnares NO abortan
// el arranque porque su fallo más probable es "ya existe", que es OK.
func initAppsSchema(db *sql.DB) error {
	// ── 1. Defensive cleanup ─────────────────────────────────────────
	//
	// Caso edge documentado por el núcleo (24/05/2026):
	//   - Install previo dejó la tabla docker_apps sin la columna `deleting`
	//   - El daemon arranca, ejecuta el schema (no-op porque la tabla existe),
	//     intenta CREATE INDEX sobre `deleting` → falla → crash loop infinito
	//
	// Fix: borrar el índice antes del ALTER. Si la columna ya existe (caso
	// normal post-migration), el DROP-CREATE es no-op funcional. Si no existe
	// (caso edge), evitamos referenciar una columna fantasma.
	db.Exec(`DROP INDEX IF EXISTS idx_docker_apps_deleting`)

	// ── 2. Schema base ───────────────────────────────────────────────
	if _, err := db.Exec(appsSchemaSQL); err != nil {
		return fmt.Errorf("apply apps schema: %w", err)
	}

	// ── 3. Migrations columnares (Batch 2, Beta 8.1.x) ───────────────
	//   APP-033 · ports_json para multi-port persistence
	//   APP-031 · deleting flag para race-free uninstall
	db.Exec(`ALTER TABLE docker_apps ADD COLUMN ports_json TEXT DEFAULT '[]'`)
	db.Exec(`ALTER TABLE docker_apps ADD COLUMN deleting INTEGER DEFAULT 0`)

	// ── 4. Re-creación de índices migrados ───────────────────────────
	// El índice debe crearse DESPUÉS del ALTER que añade la columna.
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_docker_apps_deleting ON docker_apps(deleting)`)

	// ── 5. Verificación post-migration ───────────────────────────────
	//
	// Si tras todo el proceso la columna crítica no existe, algo grave pasó
	// (disco lleno, BD corrupta, race con otro proceso). Mejor abortar el
	// arranque con error claro que dejar al daemon en zombi state.
	//
	// Esta verificación es BARATA (una query a sqlite_master) y se ejecuta una
	// sola vez por arranque del daemon · justifica su existencia.
	if err := verifyDockerAppsSchema(db); err != nil {
		return fmt.Errorf("docker_apps schema verification: %w", err)
	}

	return nil
}

// verifyDockerAppsSchema confirma que la tabla docker_apps tiene las columnas
// críticas tras aplicar el schema + migrations. Devuelve error descriptivo si
// falta alguna · el daemon abortará con mensaje claro en vez de crash loop
// genérico aguas abajo.
//
// Solo verifica columnas que han sido añadidas vía ALTER post-CREATE, porque
// las del CREATE TABLE original están garantizadas si initAppsSchema no falló
// en el step 2.
func verifyDockerAppsSchema(db *sql.DB) error {
	required := []string{"deleting", "ports_json"}
	for _, col := range required {
		var name string
		// PRAGMA table_info devuelve una fila por columna. Buscamos la nuestra.
		rows, err := db.Query(`PRAGMA table_info(docker_apps)`)
		if err != nil {
			return fmt.Errorf("query table_info: %w", err)
		}
		found := false
		for rows.Next() {
			var cid int
			var dflt sql.NullString
			var ctype string
			var notnull, pk int
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return fmt.Errorf("scan table_info: %w", err)
			}
			if name == col {
				found = true
				break
			}
		}
		rows.Close()
		if !found {
			return fmt.Errorf("column %q missing after migration · BD en estado inconsistente, revisar logs previos del daemon", col)
		}
	}
	return nil
}
