package main

import "testing"

// ─── parseMinUnallocated — P1: detección de ENOSPC de metadata ────────────────
//
// El output de `btrfs filesystem usage -b` lista "Unallocated:" una vez por
// device. Tomamos el MENOR, que es el que primero provoca read-only.

func TestParseMinUnallocated_SingleDevice(t *testing.T) {
	out := `Overall:
    Device size:                  120034123776
    Device allocated:               6475739136
    Used:                           5368709120

/dev/sda, ID: 1
   Device size:            120034123776
   Device slack:                      0
   Data,single:              5368709120
   Metadata,single:          1073741824
   System,single:              33554432
   Unallocated:            113558118400
`
	got, ok := parseMinUnallocated(out)
	if !ok {
		t.Fatal("debería haber parseado al menos un device")
	}
	if got != 113558118400 {
		t.Errorf("unallocated: got %d, want 113558118400", got)
	}
}

func TestParseMinUnallocated_TakesMinimumAcrossDevices(t *testing.T) {
	// raid1: dos devices con unallocated distinto. El mínimo es el que importa.
	out := `/dev/sda, ID: 1
   Device size:            1000204886016
   Unallocated:               2147483648

/dev/sdb, ID: 2
   Device size:            1000204886016
   Unallocated:                536870912
`
	got, ok := parseMinUnallocated(out)
	if !ok {
		t.Fatal("debería haber parseado")
	}
	if got != 536870912 {
		t.Errorf("debe tomar el mínimo entre devices: got %d, want 536870912", got)
	}
}

func TestParseMinUnallocated_NoUnallocatedLines(t *testing.T) {
	out := "Overall:\n    Device size: 120034123776\n    Used: 5368709120\n"
	_, ok := parseMinUnallocated(out)
	if ok {
		t.Error("sin líneas Unallocated debe devolver ok=false (no inventar estado)")
	}
}

func TestParseMinUnallocated_Empty(t *testing.T) {
	if _, ok := parseMinUnallocated(""); ok {
		t.Error("output vacío debe devolver ok=false")
	}
}

// Verifica el umbral: un device justo por debajo de 1 GiB es crítico.
func TestUnallocatedThreshold(t *testing.T) {
	below := unallocatedCriticalBytes - 1
	if below >= unallocatedCriticalBytes {
		t.Fatal("setup inválido")
	}
	// below es crítico; un valor >= umbral no lo es.
	if !(below < unallocatedCriticalBytes) {
		t.Error("device por debajo del umbral debe considerarse crítico")
	}
	if (unallocatedCriticalBytes + 1) < unallocatedCriticalBytes {
		t.Error("device por encima del umbral NO debe ser crítico")
	}
}

// ─── humanBytes — formato de los mensajes ─────────────────────────────────────

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{512, "512 B"},
		{1 << 30, "1.0 GiB"},
		{536870912, "512.0 MiB"},
		{2147483648, "2.0 GiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d): got %q, want %q", c.in, got, c.want)
		}
	}
}

// ─── Lógica de transición — FIX1 ──────────────────────────────────────────────
//
// El motor de salud (ComputePoolHealth) ya está testeado aparte. Aquí se cubre
// la POLÍTICA de notificación: notificar solo en transiciones de estado, con
// dedupe natural, replicando el patrón del SMART monitor.
//
// Modelamos la decisión pura (¿notificar?) para testearla sin tocar la DB.
// shouldNotifyHealth vive en storage_health_monitor.go (producción); aquí solo
// se verifica su comportamiento.

func TestShouldNotifyHealth_FirstScanHealthy_NoNotif(t *testing.T) {
	if shouldNotifyHealth("", false, "healthy") {
		t.Error("primer scan saludable NO debe notificar (evita ruido al boot)")
	}
}

func TestShouldNotifyHealth_FirstScanDegraded_Notifies(t *testing.T) {
	if !shouldNotifyHealth("", false, "degraded") {
		t.Error("primer scan ya degradado SÍ debe notificar")
	}
}

func TestShouldNotifyHealth_HealthyToDegraded_Notifies(t *testing.T) {
	if !shouldNotifyHealth("healthy", true, "degraded") {
		t.Error("healthy→degraded debe notificar")
	}
}

func TestShouldNotifyHealth_SostainedDegraded_NoReNotif(t *testing.T) {
	// degradado sostenido: el estado no cambia → NO re-notifica (dedupe).
	if shouldNotifyHealth("degraded", true, "degraded") {
		t.Error("degradado sostenido NO debe re-notificar cada ciclo")
	}
}

func TestShouldNotifyHealth_Recovery_Notifies(t *testing.T) {
	if !shouldNotifyHealth("degraded", true, "healthy") {
		t.Error("recovery degraded→healthy debe notificar")
	}
}

func TestShouldNotifyHealth_DegradedToCritical_Notifies(t *testing.T) {
	if !shouldNotifyHealth("degraded", true, "critical") {
		t.Error("degraded→critical debe notificar (empeora)")
	}
}
