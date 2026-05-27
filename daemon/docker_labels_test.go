// docker_labels_test.go — Tests unitarios del sistema de labels NimOS.
//
// NO testea applyLabelsToContainer/applyLabelsToStack/listNimOSContainers
// porque requieren un daemon Docker real. Eso queda para integration tests.
//
// Cobertura aquí:
//   - SchemaVersion · valor estable, no se cambia sin querer
//   - NewNimOSLabels · construye correctamente con todos los campos
//   - ToDockerLabelArgs · formato correcto para docker run
//   - ToLabelAddArgs · formato correcto para docker container update
//   - Nombres de labels · constantes coherentes con el schema documentado

package main

import (
	"strings"
	"testing"
)

// TestSchemaVersion_StableValue · si alguien cambia esto sin querer, el test
// grita. El cambio debe ser intencional (documentado en CHANGELOG).
func TestSchemaVersion_StableValue(t *testing.T) {
	const expected = "beta_8.2"
	if SchemaVersion != expected {
		t.Fatalf("SchemaVersion cambió sin documentación: got %q, want %q. "+
			"Si es intencional, actualiza CHANGELOG.md y el reconciler "+
			"para reconocer ambas versiones durante migración.",
			SchemaVersion, expected)
	}
}

// TestLabelConstants_NoTypos · verifica que los nombres de labels son los
// documentados. Un typo aquí dejaría containers sin filtrar correctamente.
func TestLabelConstants_NoTypos(t *testing.T) {
	cases := []struct {
		name     string
		got      string
		expected string
	}{
		{"LabelSchemaVersion", LabelSchemaVersion, "com.nimos.schema_version"},
		{"LabelManaged", LabelManaged, "com.nimos.managed"},
		{"LabelAppID", LabelAppID, "com.nimos.app_id"},
		{"LabelAppVersion", LabelAppVersion, "com.nimos.app_version"},
		{"LabelInstalledBy", LabelInstalledBy, "com.nimos.installed_by"},
		{"LabelInstalledAt", LabelInstalledAt, "com.nimos.installed_at"},
		{"LabelStack", LabelStack, "com.nimos.stack"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.expected {
				t.Errorf("%s = %q, want %q", c.name, c.got, c.expected)
			}
		})
	}
}

// TestNewNimOSLabels_StackBuild · construcción para un stack.
func TestNewNimOSLabels_StackBuild(t *testing.T) {
	labels := NewNimOSLabels("nextcloud", "29.0.7", "andres", true)

	if labels.AppID != "nextcloud" {
		t.Errorf("AppID = %q, want 'nextcloud'", labels.AppID)
	}
	if labels.AppVersion != "29.0.7" {
		t.Errorf("AppVersion = %q, want '29.0.7'", labels.AppVersion)
	}
	if labels.InstalledBy != "andres" {
		t.Errorf("InstalledBy = %q, want 'andres'", labels.InstalledBy)
	}
	if !labels.IsStack {
		t.Errorf("IsStack = false, want true")
	}
	if labels.InstalledAt == "" {
		t.Error("InstalledAt vacío · debería rellenarse con time.Now().UTC()")
	}
	// Formato ISO 8601 · contiene 'T' y termina en 'Z' (UTC)
	if !strings.Contains(labels.InstalledAt, "T") || !strings.HasSuffix(labels.InstalledAt, "Z") {
		t.Errorf("InstalledAt = %q, no parece ISO 8601 UTC", labels.InstalledAt)
	}
}

// TestNewNimOSLabels_SingleContainer · construcción para single container.
func TestNewNimOSLabels_SingleContainer(t *testing.T) {
	labels := NewNimOSLabels("jellyfin", "10.11.10", "andres", false)
	if labels.IsStack {
		t.Errorf("IsStack = true, want false (es single container)")
	}
}

// TestNewNimOSLabels_EmptyVersion · acepta version vacía (apps sin
// versión declarada en catálogo).
func TestNewNimOSLabels_EmptyVersion(t *testing.T) {
	labels := NewNimOSLabels("custom-app", "", "andres", false)
	if labels.AppVersion != "" {
		t.Errorf("AppVersion = %q, want '' (vacío)", labels.AppVersion)
	}
	// El label se sigue añadiendo, solo que con valor vacío
	args := labels.ToDockerLabelArgs()
	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--label" && args[i+1] == LabelAppVersion+"=" {
			found = true
			break
		}
	}
	if !found {
		t.Error("LabelAppVersion no aparece en ToDockerLabelArgs con valor vacío")
	}
}

// TestToDockerLabelArgs_Format · verifica el formato exacto de los args
// para docker run.
func TestToDockerLabelArgs_Format(t *testing.T) {
	labels := NewNimOSLabels("test-app", "1.0", "test-user", true)
	args := labels.ToDockerLabelArgs()

	// Debe tener pares --label key=value · 7 labels * 2 args = 14
	if len(args) != 14 {
		t.Fatalf("ToDockerLabelArgs devolvió %d args, want 14 (7 labels x 2)", len(args))
	}

	// Cada par debe ser --label seguido de key=value
	for i := 0; i < len(args); i += 2 {
		if args[i] != "--label" {
			t.Errorf("args[%d] = %q, want '--label'", i, args[i])
		}
		if !strings.Contains(args[i+1], "=") {
			t.Errorf("args[%d] = %q, no contiene '='", i+1, args[i+1])
		}
	}

	// Verificar que com.nimos.managed=true está presente
	mustContain(t, args, LabelManaged+"=true")
	mustContain(t, args, LabelAppID+"=test-app")
	mustContain(t, args, LabelAppVersion+"=1.0")
	mustContain(t, args, LabelInstalledBy+"=test-user")
	mustContain(t, args, LabelStack+"=true")
	mustContain(t, args, LabelSchemaVersion+"="+SchemaVersion)
}

// TestToLabelAddArgs_Format · verifica el formato para docker container update.
func TestToLabelAddArgs_Format(t *testing.T) {
	labels := NewNimOSLabels("test-app", "1.0", "test-user", false)
	args := labels.ToLabelAddArgs()

	if len(args) != 14 {
		t.Fatalf("ToLabelAddArgs devolvió %d args, want 14", len(args))
	}

	// Cada par debe ser --label-add (NO --label · es diferente comando)
	for i := 0; i < len(args); i += 2 {
		if args[i] != "--label-add" {
			t.Errorf("args[%d] = %q, want '--label-add'", i, args[i])
		}
	}

	mustContain(t, args, LabelStack+"=false") // single container
	mustContain(t, args, LabelManaged+"=true")
}

// mustContain · helper para verificar que un valor está en un slice.
// Útil para verificar args de docker sin asumir orden.
func mustContain(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("args no contiene %q · args = %v", want, args)
}
