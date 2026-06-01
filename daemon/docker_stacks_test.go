// docker_stacks_test.go — Tests del flujo de stacks.
//
// Cubre fillUnresolvedPathVars (APP-067 · fix bug navidrome MUSIC_PATH):
// variables ${VAR} sin definir reciben un default seguro bajo CONFIG_PATH,
// pero las que ya están definidas o tienen default inline NO se tocan.

package main

import (
	"testing"
)

const testContainerPath = "/nimos/pools/data7/docker/containers/navidrome"

// TestFillUnresolvedPathVars_NavidromeMusicPath · el caso real del bug.
// El compose usa ${MUSIC_PATH} sin default y NimOS no la conoce → debe
// recibir un default seguro {containerPath}/music.
func TestFillUnresolvedPathVars_NavidromeMusicPath(t *testing.T) {
	compose := `services:
  navidrome:
    image: deluan/navidrome:latest
    volumes:
      - ${CONFIG_PATH}/data:/data
      - ${MUSIC_PATH}:/music:ro
`
	autoEnv := map[string]interface{}{
		"CONFIG_PATH": testContainerPath,
		"HOST_IP":     "192.168.1.131",
		"TZ":          "Europe/Madrid",
	}

	result := fillUnresolvedPathVars(compose, autoEnv, testContainerPath)

	// MUSIC_PATH debe haberse rellenado
	music, ok := result["MUSIC_PATH"]
	if !ok {
		t.Fatal("MUSIC_PATH no se rellenó · el deploy seguiría fallando")
	}
	want := testContainerPath + "/music"
	if music != want {
		t.Errorf("MUSIC_PATH = %v, want %q", music, want)
	}

	// CONFIG_PATH NO debe haberse tocado (ya estaba)
	if result["CONFIG_PATH"] != testContainerPath {
		t.Errorf("CONFIG_PATH se modificó: %v", result["CONFIG_PATH"])
	}
}

// TestFillUnresolvedPathVars_AlreadyDefined · variable ya en autoEnv no se toca.
func TestFillUnresolvedPathVars_AlreadyDefined(t *testing.T) {
	compose := `services:
  app:
    volumes:
      - ${MUSIC_PATH}:/music
`
	autoEnv := map[string]interface{}{
		"CONFIG_PATH": testContainerPath,
		"MUSIC_PATH":  "/mnt/biblioteca/musica", // usuario ya la definió
	}

	result := fillUnresolvedPathVars(compose, autoEnv, testContainerPath)

	// Debe conservar el valor del usuario, NO sobrescribir con default
	if result["MUSIC_PATH"] != "/mnt/biblioteca/musica" {
		t.Errorf("MUSIC_PATH = %v, want '/mnt/biblioteca/musica' (no debe pisarse)", result["MUSIC_PATH"])
	}
}

// TestFillUnresolvedPathVars_InlineDefault · variable con default inline
// ${VAR:-x} no se toca · docker-compose la resuelve sola.
func TestFillUnresolvedPathVars_InlineDefault(t *testing.T) {
	compose := `services:
  app:
    ports:
      - "${HOST_PORT:-8080}:80"
    volumes:
      - ${DATA_DIR:-/var/data}:/data
`
	autoEnv := map[string]interface{}{
		"CONFIG_PATH": testContainerPath,
	}

	result := fillUnresolvedPathVars(compose, autoEnv, testContainerPath)

	// Ninguna con default inline debe añadirse · compose las resuelve
	if _, ok := result["HOST_PORT"]; ok {
		t.Error("HOST_PORT con default inline NO debería añadirse")
	}
	if _, ok := result["DATA_DIR"]; ok {
		t.Error("DATA_DIR con default inline NO debería añadirse")
	}
}

// TestFillUnresolvedPathVars_MultipleUnresolved · varias variables sin definir.
func TestFillUnresolvedPathVars_MultipleUnresolved(t *testing.T) {
	compose := `services:
  app:
    volumes:
      - ${MUSIC_PATH}:/music
      - ${PHOTOS_DIR}:/photos
      - ${MEDIA}:/media
`
	autoEnv := map[string]interface{}{
		"CONFIG_PATH": testContainerPath,
	}

	result := fillUnresolvedPathVars(compose, autoEnv, testContainerPath)

	cases := map[string]string{
		"MUSIC_PATH": testContainerPath + "/music",
		"PHOTOS_DIR": testContainerPath + "/photos",
		"MEDIA":      testContainerPath + "/media",
	}
	for varName, want := range cases {
		got, ok := result[varName]
		if !ok {
			t.Errorf("%s no se rellenó", varName)
			continue
		}
		if got != want {
			t.Errorf("%s = %v, want %q", varName, got, want)
		}
	}
}

// TestFillUnresolvedPathVars_NoVars · compose sin variables · no añade nada.
func TestFillUnresolvedPathVars_NoVars(t *testing.T) {
	compose := `services:
  app:
    image: nginx:latest
    ports:
      - "80:80"
`
	autoEnv := map[string]interface{}{
		"CONFIG_PATH": testContainerPath,
	}

	result := fillUnresolvedPathVars(compose, autoEnv, testContainerPath)

	if len(result) != 1 {
		t.Errorf("se añadieron vars de más · result = %v", result)
	}
}

// TestDefaultDirNameForVar · derivación de nombre de directorio.
func TestDefaultDirNameForVar(t *testing.T) {
	cases := map[string]string{
		"MUSIC_PATH":     "music",
		"PHOTOS_DIR":     "photos",
		"MEDIA_LOCATION": "media",
		"DOWNLOADS_FOLDER": "downloads",
		"MEDIA":          "media",
		"DATA":           "data",
	}
	for varName, want := range cases {
		got := defaultDirNameForVar(varName)
		if got != want {
			t.Errorf("defaultDirNameForVar(%q) = %q, want %q", varName, got, want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// APP-068 · Permisos por UID de imagen
// ─────────────────────────────────────────────────────────────────────────

func TestResolveVolumeHostPath(t *testing.T) {
	env := map[string]interface{}{
		"CONFIG_PATH":      "/nimos/pools/data8/docker/containers/grafana",
		"DB_DATA_LOCATION": "/nimos/pools/data8/docker/containers/immich/postgres",
		"UPLOAD_LOCATION":  "/nimos/pools/data8/docker/containers/immich/upload",
	}
	cases := []struct {
		name string
		vol  string
		want string
	}{
		{"grafana data", "${CONFIG_PATH}/data:/var/lib/grafana", "/nimos/pools/data8/docker/containers/grafana/data"},
		{"postgres", "${DB_DATA_LOCATION}:/var/lib/postgresql/data", "/nimos/pools/data8/docker/containers/immich/postgres"},
		{"upload con opts", "${UPLOAD_LOCATION}:/usr/src/app/upload:rw", "/nimos/pools/data8/docker/containers/immich/upload"},
		{"volumen con nombre", "model-cache:/cache", ""},                 // no bind mount
		{"localtime fuera del pool", "/etc/localtime:/etc/localtime:ro", "/etc/localtime"}, // resuelve pero fuera del pool (se filtra luego)
		{"var no resuelta", "${UNKNOWN_VAR}:/data", ""},                  // queda con $ → no resoluble
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveVolumeHostPath(c.vol, env)
			if got != c.want {
				t.Errorf("resolveVolumeHostPath(%q) = %q, want %q", c.vol, got, c.want)
			}
		})
	}
}

func TestExpandComposeVars(t *testing.T) {
	env := map[string]interface{}{
		"CONFIG_PATH": "/nimos/pools/data8/x",
		"PORT":        3001,
	}
	cases := map[string]string{
		"${CONFIG_PATH}/data": "/nimos/pools/data8/x/data",
		"$CONFIG_PATH":        "/nimos/pools/data8/x",
		"port ${PORT}":        "port 3001",
		"${MISSING}/y":        "${MISSING}/y", // no resuelta · se deja
		"sin vars":            "sin vars",
	}
	for in, want := range cases {
		got := expandComposeVars(in, env)
		if got != want {
			t.Errorf("expandComposeVars(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestApplyUIDPermissions_ParsesImmichStack · verifica que el parser entiende
// un compose multi-servicio (Immich) y no peta. No aplica ACLs reales (no hay
// Docker), pero confirma que parsea servicios+volúmenes sin error.
func TestApplyUIDPermissions_ParsesMultiService(t *testing.T) {
	compose := `services:
  immich-server:
    image: ghcr.io/immich-app/immich-server:release
    volumes:
      - ${UPLOAD_LOCATION}:/usr/src/app/upload
      - /etc/localtime:/etc/localtime:ro
  database:
    image: docker.io/tensorchord/pgvecto-rs:pg14-v0.2.0
    volumes:
      - ${DB_DATA_LOCATION}:/var/lib/postgresql/data
  redis:
    image: docker.io/valkey/valkey:8-bookworm
volumes:
  model-cache:
`
	env := map[string]interface{}{
		"UPLOAD_LOCATION":  "/nimos/pools/data8/docker/containers/immich/upload",
		"DB_DATA_LOCATION": "/nimos/pools/data8/docker/containers/immich/postgres",
	}
	// No debe entrar en pánico ni fallar al parsear. Las llamadas a docker
	// inspect/setfacl fallarán silenciosamente sin Docker, pero el parseo
	// y la lógica de rutas deben funcionar.
	applyUIDPermissions(compose, env)
	// Si llegamos aquí sin panic, el parser funcionó.
}
