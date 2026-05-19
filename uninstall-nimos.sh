#!/usr/bin/env bash
# ╔══════════════════════════════════════════════════════════════╗
# ║  NimOS Beta 8.1 — Remove Total (uninstall)                  ║
# ║  Para NAS de testeo — borra TODO rastro de NimOS            ║
# ║                                                              ║
# ║  Uso:                                                        ║
# ║    sudo bash uninstall-nimos.sh                              ║
# ║    sudo bash uninstall-nimos.sh --wipe-disks                 ║
# ║                                                              ║
# ║  ⚠  NO toca los discos físicos del pool por defecto.        ║
# ║  ⚠  Usa --wipe-disks SOLO si quieres limpiar BTRFS de       ║
# ║      los discos también (útil para test 100% limpio).       ║
# ╚══════════════════════════════════════════════════════════════╝

set -uo pipefail  # nota: NO -e — queremos continuar aunque algo falle

# ── Colors ──
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log()  { echo -e "${GREEN}[remove]${NC} $*"; }
warn() { echo -e "${YELLOW}[skip]${NC}    $*"; }
err()  { echo -e "${RED}[error]${NC}   $*" >&2; }
step() { echo -e "\n${CYAN}${BOLD}━━━ $* ━━━${NC}"; }
ok()   { echo -e "  ${GREEN}✔${NC} $*"; }

# ── Pre-flight ──
if [[ $EUID -ne 0 ]]; then
  err "Este script debe ejecutarse como root (sudo)"
  exit 1
fi

WIPE_DISKS=false
if [[ "${1:-}" == "--wipe-disks" ]]; then
  WIPE_DISKS=true
fi

# ── Confirmación ──
echo -e "${BOLD}${YELLOW}"
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║  ESTO BORRARÁ TODA LA INSTALACIÓN DE NimOS DEL SISTEMA      ║"
echo "║                                                              ║"
echo "║  Se eliminarán:                                              ║"
echo "║    · /opt/nimos                                              ║"
echo "║    · /var/lib/nimos     (incluye DB SQLite)                  ║"
echo "║    · /etc/nimos                                              ║"
echo "║    · /var/log/nimos                                          ║"
echo "║    · Servicios systemd: nimos-daemon, nimos-torrentd         ║"
echo "║    · Usuario 'nimos'                                         ║"
echo "║    · Entrada avahi /etc/avahi/services/nimos.service         ║"
if $WIPE_DISKS; then
echo "║                                                              ║"
echo "║  ⚠  --wipe-disks ACTIVADO:                                  ║"
echo "║      Se hará wipefs en discos con BTRFS detectado            ║"
echo "║      EXCEPTO el disco de boot.                               ║"
fi
echo "╚══════════════════════════════════════════════════════════════╝"
echo -e "${NC}"

read -p "Para continuar escribe 'BORRAR': " confirm
if [[ "$confirm" != "BORRAR" ]]; then
  err "Cancelado"
  exit 1
fi

# ── Detener servicios ──
step "Deteniendo servicios"

if systemctl is-active --quiet nimos-daemon 2>/dev/null; then
  systemctl stop nimos-daemon && ok "nimos-daemon stopped"
else
  warn "nimos-daemon no estaba activo"
fi

if systemctl is-enabled --quiet nimos-daemon 2>/dev/null; then
  systemctl disable nimos-daemon 2>/dev/null && ok "nimos-daemon disabled"
fi

if systemctl is-active --quiet nimos-torrentd 2>/dev/null; then
  systemctl stop nimos-torrentd && ok "nimos-torrentd stopped"
else
  warn "nimos-torrentd no estaba activo"
fi

if systemctl is-enabled --quiet nimos-torrentd 2>/dev/null; then
  systemctl disable nimos-torrentd 2>/dev/null && ok "nimos-torrentd disabled"
fi

# Servicio legacy Node.js (por si quedaba)
if systemctl list-unit-files 2>/dev/null | grep -q "^nimos.service"; then
  systemctl stop nimos 2>/dev/null || true
  systemctl disable nimos 2>/dev/null || true
  ok "Legacy nimos.service detenido"
fi

# ── Desmontar pools NimOS (si existen) ──
step "Desmontando pools managed (si hay)"

if [[ -d /nimos/pools ]]; then
  for mount in /nimos/pools/*/; do
    if mountpoint -q "$mount" 2>/dev/null; then
      umount "$mount" && ok "Desmontado: $mount" || warn "No se pudo desmontar: $mount"
    fi
  done
else
  warn "No hay directorio /nimos/pools"
fi

# ── Eliminar containers Docker creados por NimOS (si Docker está) ──
step "Limpiando containers Docker NimOS (si los hay)"

if command -v docker &>/dev/null; then
  # Containers con label nimos.app
  CONTAINERS=$(docker ps -aq --filter "label=nimos.app" 2>/dev/null)
  if [[ -n "$CONTAINERS" ]]; then
    docker stop $CONTAINERS 2>/dev/null && docker rm $CONTAINERS 2>/dev/null
    ok "Containers NimOS eliminados"
  else
    warn "No hay containers con label nimos.app"
  fi
else
  warn "Docker no instalado, saltando"
fi

# ── Eliminar archivos de servicio systemd ──
step "Eliminando archivos systemd"

for service in nimos-daemon.service nimos-torrentd.service nimos.service; do
  if [[ -f /etc/systemd/system/$service ]]; then
    rm -f /etc/systemd/system/$service && ok "Eliminado: /etc/systemd/system/$service"
  fi
done

systemctl daemon-reload && ok "systemctl daemon-reload"

# ── Eliminar directorios NimOS ──
step "Eliminando directorios NimOS"

for dir in /opt/nimos /var/lib/nimos /etc/nimos /var/log/nimos; do
  if [[ -d "$dir" ]]; then
    rm -rf "$dir" && ok "Eliminado: $dir"
  else
    warn "No existía: $dir"
  fi
done

# ── Eliminar entradas avahi ──
step "Limpiando avahi"

if [[ -f /etc/avahi/services/nimos.service ]]; then
  rm -f /etc/avahi/services/nimos.service && ok "Eliminado: /etc/avahi/services/nimos.service"
  if systemctl is-active --quiet avahi-daemon 2>/dev/null; then
    systemctl reload avahi-daemon 2>/dev/null && ok "avahi-daemon reload"
  fi
else
  warn "No había entrada avahi NimOS"
fi

# ── Eliminar usuario nimos ──
step "Eliminando usuario 'nimos'"

if id nimos &>/dev/null; then
  # Matar procesos del usuario primero (por si acaso)
  pkill -u nimos 2>/dev/null || true
  sleep 1
  userdel -r nimos 2>/dev/null && ok "Usuario 'nimos' eliminado" || warn "userdel -r falló (intenta sin -r)"
  # Por si quedó el homedir
  if [[ -d /home/nimos ]]; then
    rm -rf /home/nimos && ok "Homedir /home/nimos eliminado"
  fi
else
  warn "Usuario 'nimos' no existía"
fi

# ── Otros: chunks de upload, torrents ──
step "Limpiando directorios secundarios"

# Chunks de upload (van directo a destino, no a /var/lib)
# Buscar y eliminar cualquier .nimchunks que pueda haber quedado
if [[ -d /data/torrents ]]; then
  rm -rf /data/torrents && ok "/data/torrents eliminado"
else
  warn "No había /data/torrents"
fi

# Bind mounts a /etc/fstab — listarlos al usuario para que decida
step "Comprobando entradas en /etc/fstab"

if grep -qE "nimos|/nimos/pools" /etc/fstab 2>/dev/null; then
  warn "Hay entradas en /etc/fstab relacionadas con NimOS:"
  grep -nE "nimos|/nimos/pools" /etc/fstab
  echo ""
  echo -e "${YELLOW}   NOTA: revisa /etc/fstab manualmente y elimina las líneas si quieres.${NC}"
  echo -e "${YELLOW}   No las borra el script por seguridad (podrían referenciar discos reales).${NC}"
else
  ok "Sin entradas NimOS en /etc/fstab"
fi

# ── Wipe físico de discos (solo si --wipe-disks) ──
if $WIPE_DISKS; then
  step "Wipe físico de discos con BTRFS (modo --wipe-disks)"

  # Detectar disco de boot
  BOOT_DISK=$(lsblk -no PKNAME $(findmnt -no SOURCE /) 2>/dev/null | head -1)
  echo "  Disco de boot detectado: /dev/$BOOT_DISK (NO se tocará)"
  echo ""

  # Listar discos con BTRFS
  BTRFS_DISKS=$(lsblk -lnpo NAME,FSTYPE 2>/dev/null | awk '$2=="btrfs"{print $1}' | sort -u)

  if [[ -z "$BTRFS_DISKS" ]]; then
    warn "No hay discos con BTRFS detectado"
  else
    echo "  Discos con BTRFS encontrados:"
    echo "$BTRFS_DISKS" | sed 's/^/    /'
    echo ""
    read -p "  ¿Hacer wipefs en estos discos? (escribe 'WIPE' para confirmar): " wipe_confirm
    if [[ "$wipe_confirm" == "WIPE" ]]; then
      for disk in $BTRFS_DISKS; do
        # Saltar disco de boot
        if [[ "$disk" == "/dev/$BOOT_DISK" || "$disk" == "/dev/${BOOT_DISK}1" || "$disk" == "/dev/${BOOT_DISK}2" ]]; then
          warn "Saltando $disk (disco de boot)"
          continue
        fi
        # Saltar particiones, solo discos enteros
        if [[ ! -b "$disk" ]]; then
          continue
        fi
        wipefs -a "$disk" 2>/dev/null && ok "Wiped: $disk" || warn "wipefs falló en $disk"
      done
    else
      warn "Wipe cancelado por el usuario"
    fi
  fi
fi

# ── Resumen final ──
echo ""
echo -e "${GREEN}${BOLD}"
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║  ✔ NimOS desinstalado completamente                          ║"
echo "║                                                              ║"
echo "║  Ahora puedes instalar limpio:                               ║"
echo "║    bash install.sh                                           ║"
echo "║                                                              ║"
echo "║  Si hubo entradas en /etc/fstab, revísalas manualmente.     ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo -e "${NC}"

exit 0
