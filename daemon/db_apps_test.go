// db_apps_test.go — Tests del AppsRepo contra una DB SQLite real.
//
// Estos tests usan un archivo SQLite en /tmp, aplican el schema completo,
// y verifican las queries y mutaciones del repo de docker_apps y native_apps.
//
// Ejecutar:
//   cd daemon/
//   go test -run TestAppsRepo -v
//
// No requieren Docker ni hardware especial — solo archivo en disco.
//
// Cobertura:
//   · Docker apps: Create/Get/List/Delete/Count + UPSERT idempotencia
//   · Native apps: Create/Get/List/Delete/Count + filtros + autodetect
//   · Edge cases: ID vacío, nombres con SQL injection patterns, conflict
//   · Stale cleanup: DeleteStaleAutoDetected con cutoff
//
// Filosofía: los tests son la fuente de verdad del contrato del repo.
// Si cambias el repo y rompes un test, o el test estaba mal o el cambio.

package main

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ═══════════════════════════════════════════════════════════════════════
// Setup helpers
// ═══════════════════════════════════════════════════════════════════════

// setupTestAppsDB crea una DB SQLite en /tmp, aplica el schema apps,
// devuelve la conexión, el repo y la función de cleanup.
//
// Cada test usa su propio archivo (basado en t.Name()) para aislamiento.
func setupTestAppsDB(t *testing.T) (*sql.DB, *AppsRepo, func()) {
	t.Helper()

	// Sanitizar el nombre: los subtests t.Run incluyen "/" que rompe el path.
	safeName := strings.NewReplacer("/", "_", " ", "_", ":", "_").Replace(t.Name())
	tmpDB := "/tmp/nimos_apps_test_" + safeName + ".db"
	os.Remove(tmpDB)

	conn, err := sql.Open("sqlite", tmpDB+"?_journal_mode=WAL&_busy_timeout=10000")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	if _, err := conn.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}

	// Aplicar el schema embebido (el mismo que usa el daemon)
	if err := initAppsSchema(conn); err != nil {
		t.Fatalf("initAppsSchema: %v", err)
	}

	repo := NewAppsRepo(conn)

	cleanup := func() {
		conn.Close()
		os.Remove(tmpDB)
	}
	return conn, repo, cleanup
}

// ═══════════════════════════════════════════════════════════════════════
// DOCKER APPS · CRUD
// ═══════════════════════════════════════════════════════════════════════

func TestAppsRepoDockerCreateGetDelete(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	// 1. CountDockerApps: 0 al inicio
	count, err := repo.CountDockerApps(ctx)
	if err != nil {
		t.Fatalf("CountDockerApps: %v", err)
	}
	if count != 0 {
		t.Errorf("count empty: got %d, want 0", count)
	}

	// 2. CreateOrUpdateDockerApp · alta inicial
	app := &DBDockerApp{
		ID:          "jellyfin",
		Name:        "Jellyfin Media Server",
		Icon:        "/app-icons/jellyfin.svg",
		Image:       "jellyfin/jellyfin:latest",
		Color:       "#00A4DC",
		Type:        "container",
		OpenMode:    "internal",
		Port:        8096,
		InstalledBy: "andres",
	}
	if err := repo.CreateOrUpdateDockerApp(ctx, app); err != nil {
		t.Fatalf("CreateOrUpdateDockerApp: %v", err)
	}

	// 3. GetDockerApp · verifica todos los campos
	got, err := repo.GetDockerApp(ctx, "jellyfin")
	if err != nil {
		t.Fatalf("GetDockerApp: %v", err)
	}
	if got == nil {
		t.Fatal("GetDockerApp returned nil for existing app")
	}
	if got.Name != app.Name {
		t.Errorf("Name: got %q, want %q", got.Name, app.Name)
	}
	if got.Image != app.Image {
		t.Errorf("Image: got %q, want %q", got.Image, app.Image)
	}
	if got.Port != app.Port {
		t.Errorf("Port: got %d, want %d", got.Port, app.Port)
	}
	if got.InstalledBy != "andres" {
		t.Errorf("InstalledBy: got %q, want %q", got.InstalledBy, "andres")
	}
	if got.InstalledAt == "" {
		t.Error("InstalledAt should be auto-populated, got empty")
	}
	// Default: Config debería ser "{}" no ""
	if got.Config != "{}" {
		t.Errorf("Config default: got %q, want %q", got.Config, "{}")
	}

	// 4. CountDockerApps: 1 después de insertar
	count, _ = repo.CountDockerApps(ctx)
	if count != 1 {
		t.Errorf("count after insert: got %d, want 1", count)
	}

	// 5. DeleteDockerApp
	if err := repo.DeleteDockerApp(ctx, "jellyfin"); err != nil {
		t.Fatalf("DeleteDockerApp: %v", err)
	}

	// 6. GetDockerApp después del delete · debe ser nil
	got, err = repo.GetDockerApp(ctx, "jellyfin")
	if err != nil {
		t.Fatalf("GetDockerApp after delete: %v", err)
	}
	if got != nil {
		t.Errorf("GetDockerApp after delete should be nil, got %+v", got)
	}

	// 7. CountDockerApps: vuelve a 0
	count, _ = repo.CountDockerApps(ctx)
	if count != 0 {
		t.Errorf("count after delete: got %d, want 0", count)
	}
}

// TestAppsRepoDockerUpsert verifica que CreateOrUpdate es idempotente.
// Crítico: si el user reinstala una app, NO debe duplicarse.
func TestAppsRepoDockerUpsert(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	// 1. Insertar v1
	v1 := &DBDockerApp{
		ID:          "plex",
		Name:        "Plex",
		Image:       "plexinc/pms-docker:1.0",
		Port:        32400,
		InstalledBy: "andres",
	}
	if err := repo.CreateOrUpdateDockerApp(ctx, v1); err != nil {
		t.Fatalf("insert v1: %v", err)
	}

	originalInstalledAt := ""
	if got, _ := repo.GetDockerApp(ctx, "plex"); got != nil {
		originalInstalledAt = got.InstalledAt
	}

	// Esperar 1s para que el timestamp pueda variar
	time.Sleep(1100 * time.Millisecond)

	// 2. Actualizar v2 con mismo ID (simula reinstall con nueva versión)
	v2 := &DBDockerApp{
		ID:          "plex",
		Name:        "Plex Media Server", // nombre actualizado
		Image:       "plexinc/pms-docker:1.32",
		Port:        32400,
		Color:       "#E5A00D", // color añadido
		InstalledBy: "andres",
	}
	if err := repo.CreateOrUpdateDockerApp(ctx, v2); err != nil {
		t.Fatalf("upsert v2: %v", err)
	}

	// 3. Verificar que sigue habiendo SOLO 1 fila
	count, _ := repo.CountDockerApps(ctx)
	if count != 1 {
		t.Errorf("count after upsert: got %d, want 1 (upsert must NOT duplicate)", count)
	}

	// 4. Verificar campos actualizados
	got, _ := repo.GetDockerApp(ctx, "plex")
	if got == nil {
		t.Fatal("app disappeared after upsert")
	}
	if got.Name != "Plex Media Server" {
		t.Errorf("Name not updated: got %q", got.Name)
	}
	if got.Image != "plexinc/pms-docker:1.32" {
		t.Errorf("Image not updated: got %q", got.Image)
	}
	if got.Color != "#E5A00D" {
		t.Errorf("Color not updated: got %q", got.Color)
	}

	// 5. installed_at NO debe cambiar al hacer upsert
	// (la app fue instalada originalmente, no es una instalación nueva)
	// Nota: nuestro esquema mantiene installed_at del INSERT original
	if got.InstalledAt != originalInstalledAt {
		t.Errorf("InstalledAt should NOT change on upsert: was %q, now %q",
			originalInstalledAt, got.InstalledAt)
	}
}

func TestAppsRepoDockerListOrdered(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	// Insertar 3 apps en orden no alfabético
	apps := []*DBDockerApp{
		{ID: "sonarr", Name: "Sonarr", InstalledBy: "andres"},
		{ID: "jellyfin", Name: "Jellyfin", InstalledBy: "andres"},
		{ID: "plex", Name: "Plex", InstalledBy: "andres"},
	}
	for _, a := range apps {
		if err := repo.CreateOrUpdateDockerApp(ctx, a); err != nil {
			t.Fatalf("insert %s: %v", a.ID, err)
		}
	}

	// ListDockerApps debe devolverlos ordenados por nombre case-insensitive
	list, err := repo.ListDockerApps(ctx)
	if err != nil {
		t.Fatalf("ListDockerApps: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len: got %d, want 3", len(list))
	}
	expected := []string{"Jellyfin", "Plex", "Sonarr"}
	for i, want := range expected {
		if list[i].Name != want {
			t.Errorf("list[%d]: got %q, want %q", i, list[i].Name, want)
		}
	}
}

// TestAppsRepoDockerValidation verifica que campos obligatorios son rechazados.
func TestAppsRepoDockerValidation(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	tests := []struct {
		name    string
		app     *DBDockerApp
		wantErr string
	}{
		{
			name:    "empty ID",
			app:     &DBDockerApp{Name: "Test", InstalledBy: "andres"},
			wantErr: "ID required",
		},
		{
			name:    "empty Name",
			app:     &DBDockerApp{ID: "test", InstalledBy: "andres"},
			wantErr: "Name required",
		},
		// APP-032 · validación de Type
		{
			name: "invalid Type",
			app: &DBDockerApp{
				ID: "test", Name: "Test", Type: "fubar", InstalledBy: "andres",
			},
			wantErr: "type must be 'container' or 'stack'",
		},
		{
			name: "Type case-sensitive (Stack with capital S rejected)",
			app: &DBDockerApp{
				ID: "test", Name: "Test", Type: "Stack", InstalledBy: "andres",
			},
			wantErr: "type must be 'container' or 'stack'",
		},
		// APP-032 · validación de OpenMode
		{
			name: "invalid OpenMode",
			app: &DBDockerApp{
				ID: "test", Name: "Test", OpenMode: "auto", InstalledBy: "andres",
			},
			wantErr: "open_mode must be 'internal' or 'external'",
		},
		{
			name: "OpenMode arbitrary value rejected",
			app: &DBDockerApp{
				ID: "test", Name: "Test", OpenMode: "tab", InstalledBy: "andres",
			},
			wantErr: "open_mode must be 'internal' or 'external'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := repo.CreateOrUpdateDockerApp(ctx, tt.app)
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error: got %q, want contains %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestAppsRepoDockerDeleteIdempotent verifica que DELETE de algo que no existe
// NO devuelve error (semántica idempotente).
func TestAppsRepoDockerDeleteIdempotent(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	// Eliminar app inexistente · no debe ser error
	if err := repo.DeleteDockerApp(ctx, "nonexistent"); err != nil {
		t.Errorf("DeleteDockerApp on nonexistent: got error %v, want nil (idempotent)", err)
	}
}

// TestAppsRepoDockerSQLInjection verifica que IDs maliciosos no rompen la DB.
// SQL injection protection vía prepared statements (ya lo hacemos con ? params).
func TestAppsRepoDockerSQLInjection(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	// Insertar app legítima
	repo.CreateOrUpdateDockerApp(ctx, &DBDockerApp{
		ID:          "jellyfin",
		Name:        "Jellyfin",
		InstalledBy: "andres",
	})

	// Intentar SQL injection en GET con ID malicioso
	malicious := "'; DROP TABLE docker_apps; --"
	got, err := repo.GetDockerApp(ctx, malicious)
	if err != nil {
		t.Fatalf("GetDockerApp with malicious ID should return nil cleanly: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for malicious ID, got %+v", got)
	}

	// Verificar que la tabla sigue existiendo y la app sigue ahí
	count, err := repo.CountDockerApps(ctx)
	if err != nil {
		t.Fatalf("CountDockerApps after injection attempt: %v", err)
	}
	if count != 1 {
		t.Errorf("table corrupted after injection attempt: count=%d", count)
	}
}

// ═══════════════════════════════════════════════════════════════════════
// NATIVE APPS · CRUD
// ═══════════════════════════════════════════════════════════════════════

func TestAppsRepoNativeCreateGetDelete(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	app := &DBNativeApp{
		ID:           "samba",
		Name:         "Samba (SMB)",
		Description:  "File sharing protocol",
		Category:     "system",
		Icon:         "📁",
		Color:        "#4A90A4",
		Port:         445,
		IsDesktop:    false,
		IsNative:     true,
		AutoDetected: true,
	}
	if err := repo.CreateOrUpdateNativeApp(ctx, app); err != nil {
		t.Fatalf("CreateOrUpdateNativeApp: %v", err)
	}

	got, err := repo.GetNativeApp(ctx, "samba")
	if err != nil {
		t.Fatalf("GetNativeApp: %v", err)
	}
	if got == nil {
		t.Fatal("GetNativeApp returned nil")
	}

	// Verificar todos los campos · especialmente los booleanos (típico bug)
	if got.Name != app.Name {
		t.Errorf("Name: got %q, want %q", got.Name, app.Name)
	}
	if got.Port != 445 {
		t.Errorf("Port: got %d, want 445", got.Port)
	}
	if got.IsDesktop != false {
		t.Errorf("IsDesktop: got %v, want false", got.IsDesktop)
	}
	if got.IsNative != true {
		t.Errorf("IsNative: got %v, want true", got.IsNative)
	}
	if got.AutoDetected != true {
		t.Errorf("AutoDetected: got %v, want true", got.AutoDetected)
	}
	if got.InstalledAt == "" || got.LastSeenAt == "" {
		t.Error("InstalledAt or LastSeenAt empty after insert")
	}

	// Delete
	if err := repo.DeleteNativeApp(ctx, "samba"); err != nil {
		t.Fatalf("DeleteNativeApp: %v", err)
	}
	got, _ = repo.GetNativeApp(ctx, "samba")
	if got != nil {
		t.Error("app should be gone after delete")
	}
}

// TestAppsRepoNativeUpsertUpdatesLastSeen verifica que reescanear actualiza
// el LastSeenAt pero NO el InstalledAt (importante para detectar desinstalaciones).
func TestAppsRepoNativeUpsertUpdatesLastSeen(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	// Insert inicial
	app := &DBNativeApp{
		ID:           "transmission",
		Name:         "Transmission",
		Category:     "downloads",
		IsNative:     true,
		AutoDetected: true,
	}
	repo.CreateOrUpdateNativeApp(ctx, app)

	got1, _ := repo.GetNativeApp(ctx, "transmission")
	originalInstalledAt := got1.InstalledAt

	// Pequeña espera para que los timestamps puedan diferir
	time.Sleep(1100 * time.Millisecond)

	// Re-escanear · simulación de bootstrap detection cycle
	app2 := &DBNativeApp{
		ID:           "transmission",
		Name:         "Transmission", // sin cambios
		Category:     "downloads",
		IsNative:     true,
		AutoDetected: true,
	}
	repo.CreateOrUpdateNativeApp(ctx, app2)

	got2, _ := repo.GetNativeApp(ctx, "transmission")

	if got2.InstalledAt != originalInstalledAt {
		t.Errorf("InstalledAt should remain stable: was %q, now %q",
			originalInstalledAt, got2.InstalledAt)
	}
	if got2.LastSeenAt == originalInstalledAt {
		t.Error("LastSeenAt should have been updated on re-scan")
	}
}

func TestAppsRepoNativeByCategory(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	// Insertar apps de distintas categorías
	apps := []*DBNativeApp{
		{ID: "samba", Name: "Samba", Category: "system", IsNative: true},
		{ID: "transmission", Name: "Transmission", Category: "downloads", IsNative: true},
		{ID: "amule", Name: "aMule", Category: "downloads", IsNative: true},
		{ID: "libreoffice", Name: "LibreOffice", Category: "office", IsDesktop: true},
	}
	for _, a := range apps {
		repo.CreateOrUpdateNativeApp(ctx, a)
	}

	// ListByCategory('downloads') debe devolver 2
	downloads, err := repo.ListNativeAppsByCategory(ctx, "downloads")
	if err != nil {
		t.Fatalf("ListNativeAppsByCategory: %v", err)
	}
	if len(downloads) != 2 {
		t.Errorf("downloads count: got %d, want 2", len(downloads))
	}

	// Ordenado por nombre case-insensitive
	expectedNames := []string{"aMule", "Transmission"}
	for i, want := range expectedNames {
		if downloads[i].Name != want {
			t.Errorf("downloads[%d]: got %q, want %q", i, downloads[i].Name, want)
		}
	}

	// ListByCategory('system') = 1
	system, _ := repo.ListNativeAppsByCategory(ctx, "system")
	if len(system) != 1 {
		t.Errorf("system count: got %d, want 1", len(system))
	}

	// Categoría inexistente · lista vacía
	none, err := repo.ListNativeAppsByCategory(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("ListNativeAppsByCategory nonexistent: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("nonexistent count: got %d, want 0", len(none))
	}
}

// TestAppsRepoNativeStaleCleanup verifica que las apps autodetectadas que
// hace tiempo no se ven se eliminan. Crítico para reflejar desinstalaciones
// manuales del user via apt remove.
func TestAppsRepoNativeStaleCleanup(t *testing.T) {
	conn, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	// Insertar 3 apps autodetectadas
	apps := []*DBNativeApp{
		{ID: "samba", Name: "Samba", AutoDetected: true, IsNative: true},
		{ID: "transmission", Name: "Transmission", AutoDetected: true, IsNative: true},
		{ID: "manual_install", Name: "Manual Install", AutoDetected: false, IsNative: true},
	}
	for _, a := range apps {
		repo.CreateOrUpdateNativeApp(ctx, a)
	}

	// Hack: poner last_seen_at antiguo en samba (simulando que ya no se ve)
	oldTime := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	_, err := conn.Exec(`UPDATE native_apps SET last_seen_at = ? WHERE id = ?`, oldTime, "samba")
	if err != nil {
		t.Fatalf("backdate samba: %v", err)
	}

	// Limpiar apps autodetectadas no vistas hace 1 hora
	deleted, err := repo.DeleteStaleAutoDetected(ctx, 1*time.Hour)
	if err != nil {
		t.Fatalf("DeleteStaleAutoDetected: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted count: got %d, want 1 (only samba is stale)", deleted)
	}

	// samba debe haberse ido
	got, _ := repo.GetNativeApp(ctx, "samba")
	if got != nil {
		t.Error("samba should have been deleted as stale")
	}

	// transmission (autodetected reciente) sigue
	got, _ = repo.GetNativeApp(ctx, "transmission")
	if got == nil {
		t.Error("transmission should NOT have been deleted (recent)")
	}

	// manual_install (NO autodetected) sigue aunque sea viejo
	// Backdatarla para verificar que el filtro auto_detected=1 funciona
	_, err = conn.Exec(`UPDATE native_apps SET last_seen_at = ? WHERE id = ?`, oldTime, "manual_install")
	if err != nil {
		t.Fatalf("backdate manual: %v", err)
	}
	deleted, _ = repo.DeleteStaleAutoDetected(ctx, 1*time.Hour)
	if deleted != 0 {
		t.Errorf("manual_install should NOT be deleted (not auto_detected): deleted=%d", deleted)
	}
	got, _ = repo.GetNativeApp(ctx, "manual_install")
	if got == nil {
		t.Error("manual_install should NOT have been deleted (not auto_detected)")
	}
}

// TestAppsRepoNativeListEmptyDB verifica que ListNativeApps en DB vacía
// devuelve slice vacío sin error (importante para HTTP handlers).
func TestAppsRepoNativeListEmptyDB(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	list, err := repo.ListNativeApps(ctx)
	if err != nil {
		t.Fatalf("ListNativeApps empty: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("list empty: got len %d, want 0", len(list))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// MAPPING · ToMap helpers
// ═══════════════════════════════════════════════════════════════════════

func TestDBDockerAppToMap(t *testing.T) {
	app := &DBDockerApp{
		ID:          "jellyfin",
		Name:        "Jellyfin",
		Image:       "jellyfin/jellyfin:latest",
		Port:        8096,
		OpenMode:    "external",
		InstalledBy: "andres",
		InstalledAt: "2026-05-17T12:00:00Z",
	}

	m := app.ToMap()

	// Campos básicos
	if m["id"] != "jellyfin" {
		t.Errorf("id: got %v, want %q", m["id"], "jellyfin")
	}
	if m["openMode"] != "external" {
		t.Errorf("openMode: got %v, want %q", m["openMode"], "external")
	}

	// Compat: "external" bool derivado de openMode
	if m["external"] != true {
		t.Errorf("external (compat) should be true for openMode=external, got %v", m["external"])
	}

	// Internal openMode → external=false
	app.OpenMode = "internal"
	m2 := app.ToMap()
	if m2["external"] != false {
		t.Errorf("external (compat) should be false for openMode=internal, got %v", m2["external"])
	}
}

func TestDBNativeAppToMap(t *testing.T) {
	app := &DBNativeApp{
		ID:           "samba",
		Name:         "Samba",
		Category:     "system",
		Port:         445,
		IsDesktop:    false,
		IsNative:     true,
		AutoDetected: true,
		NimosApp:     "shares",
	}

	m := app.ToMap()

	if m["id"] != "samba" {
		t.Errorf("id: got %v", m["id"])
	}
	if m["isNative"] != true {
		t.Errorf("isNative: got %v", m["isNative"])
	}
	if m["nimosApp"] != "shares" {
		t.Errorf("nimosApp: got %v", m["nimosApp"])
	}
	if m["autoDetected"] != true {
		t.Errorf("autoDetected: got %v", m["autoDetected"])
	}
	if m["port"] != 445 {
		t.Errorf("port: got %v, want 445", m["port"])
	}

	// Port 0 NO debe aparecer en el map (es un default)
	app.Port = 0
	m2 := app.ToMap()
	if _, exists := m2["port"]; exists {
		t.Error("port should NOT be present when 0")
	}

	// NimosApp vacío NO debe aparecer
	app.NimosApp = ""
	m3 := app.ToMap()
	if _, exists := m3["nimosApp"]; exists {
		t.Error("nimosApp should NOT be present when empty")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// SCHEMA · idempotency
// ═══════════════════════════════════════════════════════════════════════

// TestAppsSchemaIdempotent verifica que aplicar el schema 2 veces NO falla.
// Esto es CRÍTICO porque initAppsSchema se llama en cada arranque del daemon.
func TestAppsSchemaIdempotent(t *testing.T) {
	tmpDB := "/tmp/nimos_apps_idempotent_test.db"
	os.Remove(tmpDB)
	defer os.Remove(tmpDB)

	conn, err := sql.Open("sqlite", tmpDB)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)

	// Aplicar schema 3 veces · debería ser todas exitosas
	for i := 1; i <= 3; i++ {
		if err := initAppsSchema(conn); err != nil {
			t.Fatalf("initAppsSchema call %d: %v", i, err)
		}
	}

	// Verificar que las tablas existen y son funcionales
	repo := NewAppsRepo(conn)
	app := &DBDockerApp{ID: "test", Name: "Test", InstalledBy: "andres"}
	if err := repo.CreateOrUpdateDockerApp(context.Background(), app); err != nil {
		t.Errorf("repo still works after triple init: %v", err)
	}
}

// ═══════════════════════════════════════════════════════════════════════
// REGRESSION TESTS · Bug fixes documentados
// ═══════════════════════════════════════════════════════════════════════

// TestAppsRepoDoesNotMutateInputStruct verifica que CreateOrUpdate*App NO
// modifica el struct recibido por parámetro.
//
// Bug fix: antes el método hacía `app.LastSeenAt = now` y `app.InstalledAt = now`,
// lo que mutaba el struct del caller. Si dos goroutines compartían el
// mismo struct, había race; si el caller usaba el struct después,
// veía valores que él no había puesto.
//
// El test pasa un struct con campos vacíos, llama al método, y verifica
// que el struct devuelto al caller sigue con los campos vacíos.
func TestAppsRepoDoesNotMutateInputStruct(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	// Docker app con campos vacíos que el método "defaultea"
	dockerApp := &DBDockerApp{
		ID:          "test-docker",
		Name:        "Test Docker",
		InstalledBy: "andres",
		// InstalledAt, Type, OpenMode, Config: todos vacíos
	}
	originalInstalledAt := dockerApp.InstalledAt
	originalType := dockerApp.Type
	originalOpenMode := dockerApp.OpenMode
	originalConfig := dockerApp.Config

	if err := repo.CreateOrUpdateDockerApp(ctx, dockerApp); err != nil {
		t.Fatalf("CreateOrUpdateDockerApp: %v", err)
	}

	if dockerApp.InstalledAt != originalInstalledAt {
		t.Errorf("docker: InstalledAt was mutated: %q → %q", originalInstalledAt, dockerApp.InstalledAt)
	}
	if dockerApp.Type != originalType {
		t.Errorf("docker: Type was mutated: %q → %q", originalType, dockerApp.Type)
	}
	if dockerApp.OpenMode != originalOpenMode {
		t.Errorf("docker: OpenMode was mutated: %q → %q", originalOpenMode, dockerApp.OpenMode)
	}
	if dockerApp.Config != originalConfig {
		t.Errorf("docker: Config was mutated: %q → %q", originalConfig, dockerApp.Config)
	}

	// Native app con campos vacíos
	nativeApp := &DBNativeApp{
		ID:       "test-native",
		Name:     "Test Native",
		IsNative: true,
		// Category, InstalledAt, LastSeenAt: vacíos
	}
	originalNCat := nativeApp.Category
	originalNInst := nativeApp.InstalledAt
	originalNLast := nativeApp.LastSeenAt

	if err := repo.CreateOrUpdateNativeApp(ctx, nativeApp); err != nil {
		t.Fatalf("CreateOrUpdateNativeApp: %v", err)
	}

	if nativeApp.Category != originalNCat {
		t.Errorf("native: Category was mutated: %q → %q", originalNCat, nativeApp.Category)
	}
	if nativeApp.InstalledAt != originalNInst {
		t.Errorf("native: InstalledAt was mutated: %q → %q", originalNInst, nativeApp.InstalledAt)
	}
	if nativeApp.LastSeenAt != originalNLast {
		t.Errorf("native: LastSeenAt was mutated: %q → %q", originalNLast, nativeApp.LastSeenAt)
	}

	// Pero los defaults SÍ se persistieron en DB (verificación cruzada)
	got, err := repo.GetDockerApp(ctx, "test-docker")
	if err != nil || got == nil {
		t.Fatalf("GetDockerApp: %v", err)
	}
	if got.Type != "container" {
		t.Errorf("expected Type default 'container' persisted, got %q", got.Type)
	}
	if got.OpenMode != "internal" {
		t.Errorf("expected OpenMode default 'internal' persisted, got %q", got.OpenMode)
	}
	if got.Config != "{}" {
		t.Errorf("expected Config default '{}' persisted, got %q", got.Config)
	}
	if got.InstalledAt == "" {
		t.Error("expected InstalledAt to be set in DB")
	}

	gotN, err := repo.GetNativeApp(ctx, "test-native")
	if err != nil || gotN == nil {
		t.Fatalf("GetNativeApp: %v", err)
	}
	if gotN.Category != "system" {
		t.Errorf("expected Category default 'system' persisted, got %q", gotN.Category)
	}
	if gotN.LastSeenAt == "" {
		t.Error("expected LastSeenAt to be set in DB")
	}
}

// TestAppsRepoNativeIsNativeFalseIsRespected verifica que cuando
// IsNative=false se persiste como 0, no se sobreescribe con default true.
//
// Bug fix: antes el método tenía `isNativeInt := 1; if !app.IsNative { isNativeInt = 0 }`,
// que parecía aplicar default true pero en realidad respetaba siempre el
// valor. El refactor lo hizo explícito con boolToInt(), pero el test
// asegura que el comportamiento no se rompe en futuras refactorizaciones.
func TestAppsRepoNativeIsNativeFalseIsRespected(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	app := &DBNativeApp{
		ID:       "non-native",
		Name:     "Not a native app",
		IsNative: false, // explícitamente false
	}
	if err := repo.CreateOrUpdateNativeApp(ctx, app); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetNativeApp(ctx, "non-native")
	if err != nil || got == nil {
		t.Fatalf("Get: %v", err)
	}
	if got.IsNative {
		t.Error("IsNative=false was not respected: got true after roundtrip")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// APP-033 · Multi-port persistence (PortsJSON)
// ═══════════════════════════════════════════════════════════════════════

// TestDBDockerAppParsedPorts_FromJSON · lógica pura del parser.
// PortsJSON válido con varios elementos → array completo.
func TestDBDockerAppParsedPorts_FromJSON(t *testing.T) {
	app := &DBDockerApp{
		ID:        "transmission",
		PortsJSON: `[{"Host":9091,"Declared":9091,"Protocol":"tcp"},{"Host":51413,"Declared":51413,"Protocol":"tcp"},{"Host":51413,"Declared":51413,"Protocol":"udp"}]`,
		Port:      9091, // legacy también presente
	}
	ports := app.parsedPorts()
	if len(ports) != 3 {
		t.Fatalf("parsedPorts: got %d ports, want 3", len(ports))
	}
	// Verifica que UDP llega correctamente (no se descarta)
	hasUDP := false
	for _, p := range ports {
		if p.Protocol == "udp" {
			hasUDP = true
		}
	}
	if !hasUDP {
		t.Error("parsedPorts: UDP protocol lost in roundtrip")
	}
}

// TestDBDockerAppParsedPorts_FallbackToLegacyPort · cuando PortsJSON
// está vacío o '[]', usar Port legacy como fuente.
func TestDBDockerAppParsedPorts_FallbackToLegacyPort(t *testing.T) {
	cases := []struct {
		name      string
		portsJSON string
		port      int
		wantLen   int
		wantPort  int
	}{
		{"empty json + Port set", "", 8096, 1, 8096},
		{"literal empty array + Port set", "[]", 8096, 1, 8096},
		{"empty json + Port 0", "", 0, 0, 0},
		{"literal empty array + Port 0", "[]", 0, 0, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := &DBDockerApp{ID: "jellyfin", PortsJSON: c.portsJSON, Port: c.port}
			ports := app.parsedPorts()
			if len(ports) != c.wantLen {
				t.Fatalf("len = %d, want %d", len(ports), c.wantLen)
			}
			if c.wantLen > 0 && ports[0].Host != c.wantPort {
				t.Errorf("ports[0].Host = %d, want %d", ports[0].Host, c.wantPort)
			}
		})
	}
}

// TestDBDockerAppParsedPorts_HandlesMalformed · JSON inválido cae al
// fallback en lugar de panic.
func TestDBDockerAppParsedPorts_HandlesMalformed(t *testing.T) {
	app := &DBDockerApp{
		ID:        "broken",
		PortsJSON: `{not valid json[}}`,
		Port:      8080,
	}
	ports := app.parsedPorts()
	// Fallback al Port legacy
	if len(ports) != 1 {
		t.Fatalf("expected fallback to single Port, got %d", len(ports))
	}
	if ports[0].Host != 8080 {
		t.Errorf("fallback Host = %d, want 8080", ports[0].Host)
	}
}

// TestAppsRepoDockerPortsJSON_RoundTrip · verifica que PortsJSON sobrevive
// INSERT → SELECT y parsedPorts deserializa correctamente.
func TestAppsRepoDockerPortsJSON_RoundTrip(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	multiPortJSON := `[{"Host":8096,"Declared":8096,"Protocol":"tcp"},{"Host":7359,"Declared":7359,"Protocol":"udp"}]`
	app := &DBDockerApp{
		ID:          "jellyfin",
		Name:        "Jellyfin",
		Type:        "container",
		Port:        8096,
		PortsJSON:   multiPortJSON,
		InstalledBy: "andres",
	}
	if err := repo.CreateOrUpdateDockerApp(ctx, app); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.GetDockerApp(ctx, "jellyfin")
	if err != nil || got == nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PortsJSON != multiPortJSON {
		t.Errorf("PortsJSON roundtrip mismatch:\ngot:  %s\nwant: %s", got.PortsJSON, multiPortJSON)
	}
	ports := got.parsedPorts()
	if len(ports) != 2 {
		t.Fatalf("parsedPorts after roundtrip: got %d, want 2", len(ports))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// APP-031 · Race-free uninstall (flag deleting)
// ═══════════════════════════════════════════════════════════════════════

// TestAppsRepoDockerDeleting_MarkSetsFlag · MarkDockerAppDeleting cambia
// el flag y persiste.
func TestAppsRepoDockerDeleting_MarkSetsFlag(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	app := &DBDockerApp{
		ID:          "jellyfin",
		Name:        "Jellyfin",
		Type:        "container",
		InstalledBy: "andres",
	}
	if err := repo.CreateOrUpdateDockerApp(ctx, app); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Inicialmente Deleting=false
	got, _ := repo.GetDockerApp(ctx, "jellyfin")
	if got == nil || got.Deleting {
		t.Fatalf("expected Deleting=false initially, got app=%v", got)
	}

	if err := repo.MarkDockerAppDeleting(ctx, "jellyfin"); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	got, _ = repo.GetDockerApp(ctx, "jellyfin")
	if got == nil || !got.Deleting {
		t.Fatalf("expected Deleting=true after Mark, got app=%v", got)
	}
}

// TestAppsRepoDockerDeleting_MarkNonexistent · Mark de app que no existe
// es idempotente (no error).
func TestAppsRepoDockerDeleting_MarkNonexistent(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	if err := repo.MarkDockerAppDeleting(ctx, "nonexistent"); err != nil {
		t.Errorf("Mark on nonexistent: got error %v, want nil (idempotent)", err)
	}
}

// TestAppsRepoDockerDeleting_ListFiltersByDefault · ListDockerApps excluye
// las marcadas como deleting=1, garantizando que NimHealth no las muestra.
func TestAppsRepoDockerDeleting_ListFiltersByDefault(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	// 2 apps: jellyfin (activa), immich (siendo borrada)
	for _, app := range []*DBDockerApp{
		{ID: "jellyfin", Name: "Jellyfin", Type: "container", InstalledBy: "andres"},
		{ID: "immich", Name: "Immich", Type: "stack", InstalledBy: "andres"},
	} {
		if err := repo.CreateOrUpdateDockerApp(ctx, app); err != nil {
			t.Fatalf("Create %s: %v", app.ID, err)
		}
	}
	if err := repo.MarkDockerAppDeleting(ctx, "immich"); err != nil {
		t.Fatalf("Mark immich: %v", err)
	}

	// ListDockerApps · debe devolver solo jellyfin
	active, err := repo.ListDockerApps(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active count = %d, want 1", len(active))
	}
	if active[0].ID != "jellyfin" {
		t.Errorf("expected jellyfin in active, got %s", active[0].ID)
	}

	// ListIncludingDeleting · debe devolver ambas
	all, err := repo.ListDockerAppsIncludingDeleting(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("total count = %d, want 2", len(all))
	}
}

// TestAppsRepoDockerDeleting_CountExcludesDeleting · CountDockerApps debe
// ser consistente con ListDockerApps (excluye deleting=1).
func TestAppsRepoDockerDeleting_CountExcludesDeleting(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	for _, app := range []*DBDockerApp{
		{ID: "a", Name: "A", Type: "container", InstalledBy: "andres"},
		{ID: "b", Name: "B", Type: "container", InstalledBy: "andres"},
		{ID: "c", Name: "C", Type: "container", InstalledBy: "andres"},
	} {
		if err := repo.CreateOrUpdateDockerApp(ctx, app); err != nil {
			t.Fatalf("Create %s: %v", app.ID, err)
		}
	}
	if err := repo.MarkDockerAppDeleting(ctx, "b"); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	count, err := repo.CountDockerApps(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2 (3 total, 1 deleting)", count)
	}
}

// TestAppsRepoDockerDeleting_FullLifecycle · simula el flujo completo
// post-APP-031 desde Mark hasta DELETE final.
func TestAppsRepoDockerDeleting_FullLifecycle(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	// 1. App creada normalmente
	if err := repo.CreateOrUpdateDockerApp(ctx, &DBDockerApp{
		ID: "jellyfin", Name: "Jellyfin", Type: "container", InstalledBy: "andres",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 2. Mark como deleting (handler hace esto síncrono)
	if err := repo.MarkDockerAppDeleting(ctx, "jellyfin"); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	// Lista activa: 0
	active, _ := repo.ListDockerApps(ctx)
	if len(active) != 0 {
		t.Errorf("during deleting, active count = %d, want 0", len(active))
	}
	// Get sigue devolviéndola con Deleting=true (necesario para cleanup)
	got, _ := repo.GetDockerApp(ctx, "jellyfin")
	if got == nil || !got.Deleting {
		t.Fatalf("during deleting, Get should return app with Deleting=true")
	}

	// 3. Delete final (goroutine cleanup)
	if err := repo.DeleteDockerApp(ctx, "jellyfin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ = repo.GetDockerApp(ctx, "jellyfin")
	if got != nil {
		t.Errorf("post-Delete, Get should return nil, got %v", got)
	}
}

// TestAppsRepoDockerReinstall_AfterDeleting · reinstalar (UPSERT) una app
// marcada deleting debe reactivarla. Caso de uso: user cancela el delete
// y reinstala antes de que la goroutine de cleanup termine.
func TestAppsRepoDockerReinstall_AfterDeleting(t *testing.T) {
	_, repo, cleanup := setupTestAppsDB(t)
	defer cleanup()
	ctx := context.Background()

	if err := repo.CreateOrUpdateDockerApp(ctx, &DBDockerApp{
		ID: "jellyfin", Name: "Jellyfin", Type: "container", InstalledBy: "andres",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.MarkDockerAppDeleting(ctx, "jellyfin"); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	// Re-install vía UPSERT con Deleting=false (default)
	if err := repo.CreateOrUpdateDockerApp(ctx, &DBDockerApp{
		ID: "jellyfin", Name: "Jellyfin v2", Type: "container", InstalledBy: "andres",
	}); err != nil {
		t.Fatalf("Reinstall: %v", err)
	}
	got, _ := repo.GetDockerApp(ctx, "jellyfin")
	if got == nil || got.Deleting {
		t.Errorf("reinstall should clear Deleting flag, got app=%v", got)
	}
	if got.Name != "Jellyfin v2" {
		t.Errorf("reinstall should update Name, got %q", got.Name)
	}
}
