package main

import (
	"encoding/json"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test de equivalencia — Ola 3
//
// La red de seguridad del refactor de tipos: garantiza que parseDetectedDisks
// (tipado) produce un JSON BYTE-A-BYTE idéntico a parseDetectedDisksLegacy (el
// map original) para el mismo input. Si esto pasa, la migración no cambió el
// contrato que consume el frontend.
//
// Nota: ambas funciones llaman a getSmartDetailsForDisk para los discos
// eligible, que lee de la cache SMART (smartHistory/smartDetailsCache). En el
// test esa cache está vacía → status "unknown" y detalles a cero, IGUAL para
// las dos versiones, así que la comparación sigue siendo válida.
// ─────────────────────────────────────────────────────────────────────────────

// jsonEq serializa dos valores a JSON y compara los bytes.
func jsonEq(t *testing.T, a, b interface{}) {
	t.Helper()
	ja, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal tipado: %v", err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	// Comparar a través de un round-trip a map para que el ORDEN de las keys
	// no afecte (encoding/json ordena las keys de un struct por orden de
	// campo, y las de un map alfabéticamente; lo que importa es el contenido).
	var ma, mb interface{}
	json.Unmarshal(ja, &ma)
	json.Unmarshal(jb, &mb)
	na, _ := json.Marshal(ma)
	nb, _ := json.Marshal(mb)
	if string(na) != string(nb) {
		t.Errorf("JSON difiere:\n  tipado: %s\n  legacy: %s", na, nb)
	}
}

// assertEquivalent corre ambas versiones con el mismo input y compara.
func assertEquivalent(t *testing.T, lsblkRaw, rootDisk string, poolDisks map[string]bool) {
	t.Helper()
	typed := parseDetectedDisks(lsblkRaw, rootDisk, poolDisks)
	legacy := parseDetectedDisksLegacy(lsblkRaw, rootDisk, poolDisks)
	jsonEq(t, typed, legacy)
}

func TestDisksEquivalence_EligibleDisk(t *testing.T) {
	lsblk := `{"blockdevices":[
		{"name":"sda","size":1000204886016,"type":"disk","rota":true,"mountpoint":null,
		 "model":"WDC WD10","serial":"WD-123","tran":"sata","rm":false,"fstype":null,"label":null}
	]}`
	assertEquivalent(t, lsblk, "", map[string]bool{})
}

func TestDisksEquivalence_USBPendrive(t *testing.T) {
	// usb + removable + < 10GB → usb
	lsblk := `{"blockdevices":[
		{"name":"sdb","size":8000000000,"type":"disk","rota":false,"mountpoint":null,
		 "model":"Kingston","serial":"K-9","tran":"usb","rm":true,"fstype":"vfat","label":"USB"}
	]}`
	assertEquivalent(t, lsblk, "", map[string]bool{})
}

func TestDisksEquivalence_NVMe(t *testing.T) {
	lsblk := `{"blockdevices":[
		{"name":"nvme0n1","size":512110190592,"type":"disk","rota":false,"mountpoint":null,
		 "model":"Samsung 980","serial":"S-1","tran":"nvme","rm":false,"fstype":null,"label":null}
	]}`
	assertEquivalent(t, lsblk, "", map[string]bool{})
}

func TestDisksEquivalence_Provisioned(t *testing.T) {
	lsblk := `{"blockdevices":[
		{"name":"sdc","size":2000398934016,"type":"disk","rota":true,"mountpoint":null,
		 "model":"Seagate","serial":"ST-7","tran":"sata","rm":false,"fstype":"btrfs","label":"data8"}
	]}`
	assertEquivalent(t, lsblk, "", map[string]bool{"/dev/sdc": true})
}

func TestDisksEquivalence_BootDiskExcluded(t *testing.T) {
	lsblk := `{"blockdevices":[
		{"name":"sda","size":256060514304,"type":"disk","rota":false,"mountpoint":null,
		 "model":"Boot","serial":"B-1","tran":"sata","rm":false,"fstype":null,"label":null,
		 "children":[{"name":"sda1","size":256060514304,"fstype":"ext4","label":"root","mountpoint":"/"}]}
	]}`
	// sda es root → excluido de todas las categorías
	assertEquivalent(t, lsblk, "sda", map[string]bool{})
}

func TestDisksEquivalence_PartitionsWithNulls(t *testing.T) {
	// Particiones con fstype/label/mountpoint null — el caso que más
	// fácilmente rompería la equivalencia (string "" vs null).
	lsblk := `{"blockdevices":[
		{"name":"sdd","size":4000787030016,"type":"disk","rota":true,"mountpoint":null,
		 "model":"HGST","serial":"H-3","tran":"sata","rm":false,"fstype":null,"label":null,
		 "children":[
		   {"name":"sdd1","size":1000000000,"fstype":null,"label":null,"mountpoint":null},
		   {"name":"sdd2","size":2999787030016,"fstype":"ext4","label":"data","mountpoint":"/mnt/data"}
		 ]}
	]}`
	assertEquivalent(t, lsblk, "", map[string]bool{})
}

func TestDisksEquivalence_DiskWithFullDiskFilesystem(t *testing.T) {
	// Disco con FS a disco completo (sin particiones) → hasExistingData=true.
	lsblk := `{"blockdevices":[
		{"name":"sde","size":1000204886016,"type":"disk","rota":true,"mountpoint":null,
		 "model":"Foreign","serial":"F-9","tran":"sata","rm":false,"fstype":"xfs","label":"foreign"}
	]}`
	assertEquivalent(t, lsblk, "", map[string]bool{})
}

func TestDisksEquivalence_MixedAllCategories(t *testing.T) {
	lsblk := `{"blockdevices":[
		{"name":"sda","size":256060514304,"type":"disk","rota":false,"mountpoint":null,
		 "model":"Boot","serial":"B-1","tran":"sata","rm":false,"fstype":null,"label":null,
		 "children":[{"name":"sda1","size":256060514304,"fstype":"ext4","label":"root","mountpoint":"/"}]},
		{"name":"sdb","size":2000398934016,"type":"disk","rota":true,"mountpoint":null,
		 "model":"Pool1","serial":"P-1","tran":"sata","rm":false,"fstype":"btrfs","label":"data8"},
		{"name":"sdc","size":1000204886016,"type":"disk","rota":true,"mountpoint":null,
		 "model":"Free1","serial":"FR-1","tran":"sata","rm":false,"fstype":null,"label":null},
		{"name":"sdd","size":8000000000,"type":"disk","rota":false,"mountpoint":null,
		 "model":"USB","serial":"U-1","tran":"usb","rm":true,"fstype":"vfat","label":"stick"},
		{"name":"nvme0n1","size":512110190592,"type":"disk","rota":false,"mountpoint":null,
		 "model":"NVMe","serial":"N-1","tran":"nvme","rm":false,"fstype":null,"label":null}
	]}`
	assertEquivalent(t, lsblk, "sda", map[string]bool{"/dev/sdb": true})
}

func TestDisksEquivalence_FiltersTooSmallAndNonDisk(t *testing.T) {
	lsblk := `{"blockdevices":[
		{"name":"sda","size":500000000,"type":"disk","rota":true,"tran":"sata","rm":false},
		{"name":"sr0","size":1073741824000,"type":"rom","rota":true,"tran":"sata","rm":true},
		{"name":"loop0","size":100000000000,"type":"loop","rota":false},
		{"name":"sdb","size":2000398934016,"type":"disk","rota":true,"mountpoint":null,
		 "model":"Good","serial":"G-1","tran":"sata","rm":false,"fstype":null,"label":null}
	]}`
	// Solo sdb pasa: sda es <1GB, sr0 no es disk, loop0 no es disk ni whitelisted.
	assertEquivalent(t, lsblk, "", map[string]bool{})
}

func TestDisksEquivalence_EmptyInput(t *testing.T) {
	assertEquivalent(t, `{"blockdevices":[]}`, "", map[string]bool{})
}
