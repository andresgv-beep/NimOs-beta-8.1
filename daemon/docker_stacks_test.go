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
