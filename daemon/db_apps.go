// db_apps.go — Repository de apps (NimOS Beta 8.1)
//
// Pattern: repository por dominio de entidad (NO por tipo de operación).
// Este archivo es DUEÑO de:
//   · Schema docker_apps + native_apps
//   · CRUD completo de ambas tablas
//   · Queries especializadas (count, by category, auto-detected only, etc.)
//
// Reglas de uso desde fuera:
//   · NO escribir SQL crudo en apps.go ni en otros archivos
//   · Usar SIEMPRE los métodos de AppsRepo para tocar las tablas
//   · El acceso conveniente vía `appsRepo` global existe para legacy/HTTP
//     handlers; el código nuevo debería recibir *AppsRepo por inyección
//
// Testing:
//   · db_apps_test.go usa una DB SQLite en /tmp con el mismo schema
//   · Setup helper: setupTestAppsDB()
//   · Cleanup automático con defer

package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════
// Models
// ═══════════════════════════════════════════════════════════════════════

// DBDockerApp representa una fila de la tabla docker_apps.
// Container apps instaladas por el user (jellyfin, plex, sonarr...).
type DBDockerApp struct {
	ID          string
	Name        string
	Icon        string
	Image       string
	Color       string
	Type        string // 'container' | 'stack'
	OpenMode    string // 'internal' | 'external'
	Port        int
	Config      string // JSON serializado: volúmenes, env, ports extra
	InstalledAt string
	InstalledBy string
}

// ToMap convierte DBDockerApp a map para serialización JSON HTTP.
// Mantiene compatibilidad con el contrato anterior (installed-apps.json).
func (a *DBDockerApp) ToMap() map[string]interface{} {
	m := map[string]interface{}{
		"id":          a.ID,
		"name":        a.Name,
		"icon":        a.Icon,
		"image":       a.Image,
		"color":       a.Color,
		"type":        a.Type,
		"openMode":    a.OpenMode,
		"port":        a.Port,
		"installedAt": a.InstalledAt,
		"installedBy": a.InstalledBy,
	}
	// Backwards compat: "external" bool derivado de openMode
	m["external"] = a.OpenMode == "external"
	return m
}

// DBNativeApp representa una fila de la tabla native_apps.
// Apps nativas Linux (samba, kvm, transmission...).
type DBNativeApp struct {
	ID           string
	Name         string
	Description  string
	Category     string
	Icon         string
	Color        string
	Port         int
	IsDesktop    bool
	IsNative     bool
	NimosApp     string
	AutoDetected bool
	InstalledAt  string
	LastSeenAt   string
}

// ToMap convierte DBNativeApp a map para serialización JSON HTTP.
func (a *DBNativeApp) ToMap() map[string]interface{} {
	m := map[string]interface{}{
		"id":           a.ID,
		"name":         a.Name,
		"description":  a.Description,
		"category":     a.Category,
		"icon":         a.Icon,
		"color":        a.Color,
		"type":         "native",
		"isDesktop":    a.IsDesktop,
		"isNative":     a.IsNative,
		"autoDetected": a.AutoDetected,
	}
	if a.Port > 0 {
		m["port"] = a.Port
	}
	if a.NimosApp != "" {
		m["nimosApp"] = a.NimosApp
	}
	return m
}

// ═══════════════════════════════════════════════════════════════════════
// AppsRepo · repository
// ═══════════════════════════════════════════════════════════════════════

// AppsRepo gestiona el acceso a las tablas docker_apps y native_apps.
// Es seguro para uso concurrente porque *sql.DB es safe (pool).
type AppsRepo struct {
	db *sql.DB
}

// NewAppsRepo crea un repositorio sobre la conexión SQLite dada.
// La conexión debe tener el schema ya aplicado (vía initAppsSchema).
func NewAppsRepo(db *sql.DB) *AppsRepo {
	return &AppsRepo{db: db}
}

// appsRepo es la instancia global · inicializada al arranque junto con DB.
// Acceso conveniente para legacy/HTTP handlers; código nuevo debería usar
// inyección de dependencias.
var appsRepo *AppsRepo

// ═══════════════════════════════════════════════════════════════════════
// Docker Apps · CRUD
// ═══════════════════════════════════════════════════════════════════════

// CreateOrUpdateDockerApp inserta o actualiza una app Docker.
// Idempotente: si el id ya existe, actualiza los campos.
// La política UPSERT evita race conditions en reinstalaciones.
//
// NO muta el struct `app` recibido: trabaja con variables locales para
// los defaults, evitando race conditions y side-effects.
func (r *AppsRepo) CreateOrUpdateDockerApp(ctx context.Context, app *DBDockerApp) error {
	if app.ID == "" {
		return fmt.Errorf("docker app: ID required")
	}
	if app.Name == "" {
		return fmt.Errorf("docker app: Name required")
	}

	installedAt := app.InstalledAt
	if installedAt == "" {
		installedAt = time.Now().UTC().Format(time.RFC3339)
	}
	typ := app.Type
	if typ == "" {
		typ = "container"
	}
	openMode := app.OpenMode
	if openMode == "" {
		openMode = "internal"
	}
	config := app.Config
	if config == "" {
		config = "{}"
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO docker_apps
			(id, name, icon, image, color, type, open_mode, port, config, installed_at, installed_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			icon = excluded.icon,
			image = excluded.image,
			color = excluded.color,
			type = excluded.type,
			open_mode = excluded.open_mode,
			port = excluded.port,
			config = excluded.config
	`, app.ID, app.Name, app.Icon, app.Image, app.Color, typ,
		openMode, app.Port, config, installedAt, app.InstalledBy)
	if err != nil {
		return fmt.Errorf("upsert docker app %q: %w", app.ID, err)
	}
	return nil
}

// GetDockerApp obtiene una app Docker por id.
// Devuelve (nil, nil) si no existe (no es error).
func (r *AppsRepo) GetDockerApp(ctx context.Context, id string) (*DBDockerApp, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, icon, image, color, type, open_mode, port, config, installed_at, installed_by
		FROM docker_apps WHERE id = ?
	`, id)

	var a DBDockerApp
	err := row.Scan(&a.ID, &a.Name, &a.Icon, &a.Image, &a.Color, &a.Type,
		&a.OpenMode, &a.Port, &a.Config, &a.InstalledAt, &a.InstalledBy)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get docker app %q: %w", id, err)
	}
	return &a, nil
}

// ListDockerApps devuelve todas las apps Docker, ordenadas por nombre.
func (r *AppsRepo) ListDockerApps(ctx context.Context) ([]*DBDockerApp, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, icon, image, color, type, open_mode, port, config, installed_at, installed_by
		FROM docker_apps
		ORDER BY name COLLATE NOCASE
	`)
	if err != nil {
		return nil, fmt.Errorf("list docker apps: %w", err)
	}
	defer rows.Close()

	var apps []*DBDockerApp
	for rows.Next() {
		var a DBDockerApp
		if err := rows.Scan(&a.ID, &a.Name, &a.Icon, &a.Image, &a.Color, &a.Type,
			&a.OpenMode, &a.Port, &a.Config, &a.InstalledAt, &a.InstalledBy); err != nil {
			return nil, fmt.Errorf("scan docker app: %w", err)
		}
		apps = append(apps, &a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter docker apps: %w", err)
	}
	return apps, nil
}

// DeleteDockerApp elimina una app Docker por id.
// No es error si no existe (DELETE idempotente).
func (r *AppsRepo) DeleteDockerApp(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM docker_apps WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete docker app %q: %w", id, err)
	}
	return nil
}

// CountDockerApps devuelve el número total de apps Docker instaladas.
func (r *AppsRepo) CountDockerApps(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM docker_apps`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count docker apps: %w", err)
	}
	return count, nil
}

// ═══════════════════════════════════════════════════════════════════════
// Native Apps · CRUD
// ═══════════════════════════════════════════════════════════════════════

// CreateOrUpdateNativeApp inserta o actualiza una app native.
// Útil tanto para registro manual como para autodetect en arranque.
// LastSeenAt se actualiza siempre; InstalledAt solo en la primera inserción.
//
// NO muta el struct `app` recibido: trabaja con variables locales para
// LastSeenAt e InstalledAt, evitando race conditions si el caller
// comparte la misma referencia entre goroutines y haciendo el método
// "side-effect free" respecto al input.
//
// Defaults aplicados aquí (única fuente de verdad):
//   · Category vacía → "system"
//   · InstalledAt vacío → now
//   · IsNative no se defaultea (es bool obligatorio, evita ambigüedad)
func (r *AppsRepo) CreateOrUpdateNativeApp(ctx context.Context, app *DBNativeApp) error {
	if app.ID == "" {
		return fmt.Errorf("native app: ID required")
	}
	if app.Name == "" {
		return fmt.Errorf("native app: Name required")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	installedAt := app.InstalledAt
	if installedAt == "" {
		installedAt = now
	}
	lastSeenAt := now
	category := app.Category
	if category == "" {
		category = "system"
	}

	isDesktopInt := boolToInt(app.IsDesktop)
	isNativeInt := boolToInt(app.IsNative)
	autoDetectedInt := boolToInt(app.AutoDetected)

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO native_apps
			(id, name, description, category, icon, color, port, is_desktop, is_native, nimos_app, auto_detected, installed_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			category = excluded.category,
			icon = excluded.icon,
			color = excluded.color,
			port = excluded.port,
			is_desktop = excluded.is_desktop,
			is_native = excluded.is_native,
			nimos_app = excluded.nimos_app,
			auto_detected = excluded.auto_detected,
			last_seen_at = excluded.last_seen_at
	`, app.ID, app.Name, app.Description, category, app.Icon, app.Color,
		app.Port, isDesktopInt, isNativeInt, app.NimosApp, autoDetectedInt,
		installedAt, lastSeenAt)
	if err != nil {
		return fmt.Errorf("upsert native app %q: %w", app.ID, err)
	}
	return nil
}

// boolToInt convierte bool→int para SQLite (1/0). Helper interno
// para mantener consistencia en todo el repo.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetNativeApp obtiene una app native por id.
// Devuelve (nil, nil) si no existe.
func (r *AppsRepo) GetNativeApp(ctx context.Context, id string) (*DBNativeApp, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, category, icon, color, port, is_desktop, is_native, nimos_app, auto_detected, installed_at, last_seen_at
		FROM native_apps WHERE id = ?
	`, id)

	var a DBNativeApp
	var isDesktopInt, isNativeInt, autoDetectedInt int
	err := row.Scan(&a.ID, &a.Name, &a.Description, &a.Category, &a.Icon, &a.Color,
		&a.Port, &isDesktopInt, &isNativeInt, &a.NimosApp, &autoDetectedInt,
		&a.InstalledAt, &a.LastSeenAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get native app %q: %w", id, err)
	}
	a.IsDesktop = isDesktopInt == 1
	a.IsNative = isNativeInt == 1
	a.AutoDetected = autoDetectedInt == 1
	return &a, nil
}

// ListNativeApps devuelve todas las apps native, ordenadas por categoría y nombre.
func (r *AppsRepo) ListNativeApps(ctx context.Context) ([]*DBNativeApp, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, category, icon, color, port, is_desktop, is_native, nimos_app, auto_detected, installed_at, last_seen_at
		FROM native_apps
		ORDER BY category, name COLLATE NOCASE
	`)
	if err != nil {
		return nil, fmt.Errorf("list native apps: %w", err)
	}
	defer rows.Close()

	var apps []*DBNativeApp
	for rows.Next() {
		var a DBNativeApp
		var isDesktopInt, isNativeInt, autoDetectedInt int
		if err := rows.Scan(&a.ID, &a.Name, &a.Description, &a.Category, &a.Icon, &a.Color,
			&a.Port, &isDesktopInt, &isNativeInt, &a.NimosApp, &autoDetectedInt,
			&a.InstalledAt, &a.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan native app: %w", err)
		}
		a.IsDesktop = isDesktopInt == 1
		a.IsNative = isNativeInt == 1
		a.AutoDetected = autoDetectedInt == 1
		apps = append(apps, &a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter native apps: %w", err)
	}
	return apps, nil
}

// DeleteNativeApp elimina una app native por id.
// No es error si no existe.
func (r *AppsRepo) DeleteNativeApp(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM native_apps WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete native app %q: %w", id, err)
	}
	return nil
}

// ListNativeAppsByCategory filtra apps native por categoría.
// Útil para UI que agrupa por sección (system, downloads, office).
func (r *AppsRepo) ListNativeAppsByCategory(ctx context.Context, category string) ([]*DBNativeApp, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, category, icon, color, port, is_desktop, is_native, nimos_app, auto_detected, installed_at, last_seen_at
		FROM native_apps
		WHERE category = ?
		ORDER BY name COLLATE NOCASE
	`, category)
	if err != nil {
		return nil, fmt.Errorf("list native apps by category %q: %w", category, err)
	}
	defer rows.Close()

	var apps []*DBNativeApp
	for rows.Next() {
		var a DBNativeApp
		var isDesktopInt, isNativeInt, autoDetectedInt int
		if err := rows.Scan(&a.ID, &a.Name, &a.Description, &a.Category, &a.Icon, &a.Color,
			&a.Port, &isDesktopInt, &isNativeInt, &a.NimosApp, &autoDetectedInt,
			&a.InstalledAt, &a.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan native app: %w", err)
		}
		a.IsDesktop = isDesktopInt == 1
		a.IsNative = isNativeInt == 1
		a.AutoDetected = autoDetectedInt == 1
		apps = append(apps, &a)
	}
	return apps, rows.Err()
}

// CountNativeApps devuelve el número total de apps native.
func (r *AppsRepo) CountNativeApps(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_apps`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count native apps: %w", err)
	}
	return count, nil
}

// DeleteStaleAutoDetected elimina apps autodetectadas que no se ven hace más de `olderThan`.
// Usado al final del escaneo de arranque para limpiar apps desinstaladas manualmente.
//
// Devuelve el número de filas eliminadas.
func (r *AppsRepo) DeleteStaleAutoDetected(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM native_apps
		WHERE auto_detected = 1 AND last_seen_at < ?
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete stale auto-detected: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		// RowsAffected puede fallar en drivers raros; no es fatal porque
		// el DELETE sí se ejecutó. Loggeamos y devolvemos 0 con error
		// para que el caller decida.
		return 0, fmt.Errorf("delete stale auto-detected: rows affected: %w", err)
	}
	return affected, nil
}
