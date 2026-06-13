package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ═══════════════════════════════════════════════════════════════════════
// MANAGED FOLDERS · helpers de soporte para las ops folder.* · Beta 8.1
// ═══════════════════════════════════════════════════════════════════════

// checkFolderRelPath valida la ruta relativa de una carpeta gestionada.
// v1 es PLANO: solo primer nivel dentro del share. Reglas:
//   - no vacía
//   - sin "/" (un solo componente, primer nivel)
//   - sin "..", "." ni separadores raros
//   - longitud y caracteres razonables
func checkFolderRelPath(rel string) error {
	if rel == "" {
		return fmt.Errorf("folder path is required")
	}
	if rel != filepath.Clean(rel) {
		return fmt.Errorf("invalid folder path")
	}
	if strings.ContainsAny(rel, "/\\") {
		return fmt.Errorf("folder must be top-level (no nested paths in v1)")
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".") {
		return fmt.Errorf("invalid folder name")
	}
	if len(rel) > 255 {
		return fmt.Errorf("folder name too long")
	}
	return nil
}

// poolMountFromSharePath deriva el mount del pool desde el path de un share.
// Los shares viven en <poolMount>/shares/<name>, así que el pool es el
// directorio dos niveles por encima. Devuelve "" si no encaja el patrón.
func poolMountFromSharePath(sharePath string) string {
	// .../shares/<name> → quitar <name> y "shares"
	parent := filepath.Dir(sharePath) // .../shares
	if filepath.Base(parent) != "shares" {
		return ""
	}
	return filepath.Dir(parent) // .../<poolMount>
}

// dirIsEmpty indica si un directorio no tiene entradas. Para folder.delete:
// v1 rechaza borrar carpetas con contenido (sin borrado recursivo).
func dirIsEmpty(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	// Leer una sola entrada; si EOF, está vacío.
	_, err = f.Readdirnames(1)
	if err == io.EOF {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

// parseQgroupReferenced extrae los bytes "Referenced" de la salida de
// `btrfs qgroup show -f --raw <path>`. Con --raw los valores son enteros en
// bytes. Formato típico:
//
//	Qgroupid    Referenced    Exclusive   Path
//	--------    ----------    ---------   ----
//	0/257       163840        163840      <path>
//
// Devuelve 0 si no encuentra una fila de datos parseable.
func parseQgroupReferenced(out string) int64 {
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// La fila de datos empieza por un qgroupid "N/M".
		if !strings.Contains(fields[0], "/") {
			continue
		}
		var ref int64
		if _, err := fmt.Sscanf(fields[1], "%d", &ref); err == nil {
			return ref
		}
	}
	return 0
}
