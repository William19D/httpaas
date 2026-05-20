#!/bin/bash
# =============================================================
# HTTPaaS — setup-db-templates.sh
# =============================================================
# Prepara las dos VMs plantilla para el servicio administrado
# de bases de datos (DBaaS):
#
#   httpaas-mariadb    — Debian 13 CLI + MariaDB Server
#   httpaas-postgresql — Debian 13 CLI + PostgreSQL Server
#
# Requisito previo: tener ya instalado Debian 13 en modo CLI
# en ambas VMs y configurado el acceso SSH con la llave pública
# definida en config.json (ssh.key_path + .pub).
#
# Uso:
#   ./setup-db-templates.sh [--mariadb-ip IP] [--pgsql-ip IP]
#
# Por defecto usa:
#   MariaDB    → 192.168.56.21
#   PostgreSQL → 192.168.56.22
# =============================================================
set -euo pipefail

source "$(dirname "$0")/common.sh"

MARIADB_IP="${MARIADB_IP:-192.168.56.21}"
PGSQL_IP="${PGSQL_IP:-192.168.56.22}"

# Parsear argumentos
while [[ $# -gt 0 ]]; do
    case "$1" in
        --mariadb-ip) MARIADB_IP="$2"; shift 2;;
        --pgsql-ip)   PGSQL_IP="$2";   shift 2;;
        *) echo "Argumento desconocido: $1"; exit 1;;
    esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIGS_DIR="$(dirname "$SCRIPT_DIR")/configs/vm-db"
FIRSTBOOT_SCRIPT="$CONFIGS_DIR/first-boot-db.sh"

SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR"

# Función para ejecutar un comando en una VM vía SSH
vm_ssh() {
    local ip="$1"; shift
    ssh $SSH_OPTS root@"$ip" -- "$@"
}

# Función para copiar un archivo a una VM
vm_scp() {
    local src="$1" dst_ip="$2" dst_path="$3"
    scp $SSH_OPTS "$src" root@"$dst_ip":"$dst_path"
}

# ── Configurar VM de MariaDB ──────────────────────────────────
setup_mariadb() {
    local ip="$MARIADB_IP"
    log_info "Configurando VM MariaDB en $ip…"

    # 1. Actualizar e instalar paquetes
    vm_ssh "$ip" bash -c "
        export DEBIAN_FRONTEND=noninteractive
        apt-get update -q
        apt-get install -y -q mariadb-server openssh-server curl wget \
            virtualbox-guest-utils virtualbox-guest-dkms
    "

    # 2. Copiar script first-boot
    vm_scp "$FIRSTBOOT_SCRIPT" "$ip" "/usr/local/bin/httpaas-db-firstboot.sh"
    vm_ssh "$ip" chmod +x /usr/local/bin/httpaas-db-firstboot.sh

    # 3. Instalar y habilitar el servicio systemd
    vm_scp "$CONFIGS_DIR/httpaas-db-firstboot.service" "$ip" \
        "/etc/systemd/system/httpaas-db-firstboot.service"
    vm_ssh "$ip" bash -c "
        systemctl daemon-reload
        systemctl enable httpaas-db-firstboot.service
    "

    # 4. Asegurarse de que MariaDB está instalado y funcionando
    vm_ssh "$ip" bash -c "
        systemctl enable --now mariadb
        mysql -u root -e 'SELECT VERSION();'
    "

    # 5. Apagar la VM para convertir el disco a multiattach
    log_info "MariaDB configurado. Apagando VM para configurar disco multiattach…"
    vm_ssh "$ip" poweroff || true
    sleep 8

    log_ok "VM MariaDB lista. Ahora convierte su disco a multiattach:"
    log_info "  VBoxManage modifyhd <ruta-al-disco.vdi> --type multiattach"
    log_info "  (o usa VBoxManage storageattach con --mtype multiattach)"
}

# ── Configurar VM de PostgreSQL ───────────────────────────────
setup_postgresql() {
    local ip="$PGSQL_IP"
    log_info "Configurando VM PostgreSQL en $ip…"

    # 1. Actualizar e instalar paquetes
    vm_ssh "$ip" bash -c "
        export DEBIAN_FRONTEND=noninteractive
        apt-get update -q
        apt-get install -y -q postgresql openssh-server curl wget \
            virtualbox-guest-utils virtualbox-guest-dkms
    "

    # 2. Copiar script first-boot
    vm_scp "$FIRSTBOOT_SCRIPT" "$ip" "/usr/local/bin/httpaas-db-firstboot.sh"
    vm_ssh "$ip" chmod +x /usr/local/bin/httpaas-db-firstboot.sh

    # 3. Instalar y habilitar el servicio systemd
    vm_scp "$CONFIGS_DIR/httpaas-db-firstboot.service" "$ip" \
        "/etc/systemd/system/httpaas-db-firstboot.service"
    vm_ssh "$ip" bash -c "
        systemctl daemon-reload
        systemctl enable httpaas-db-firstboot.service
    "

    # 4. Asegurar que PostgreSQL funciona
    vm_ssh "$ip" bash -c "
        systemctl enable --now postgresql
        sudo -u postgres psql -c 'SELECT version();'
    "

    # 5. Apagar la VM
    log_info "PostgreSQL configurado. Apagando VM para configurar disco multiattach…"
    vm_ssh "$ip" poweroff || true
    sleep 8

    log_ok "VM PostgreSQL lista. Ahora convierte su disco a multiattach:"
    log_info "  VBoxManage modifyhd <ruta-al-disco.vdi> --type multiattach"
}

# ── Main ──────────────────────────────────────────────────────
log_info "=== Configuración de plantillas DBaaS ==="
log_info "MariaDB IP: $MARIADB_IP"
log_info "PostgreSQL IP: $PGSQL_IP"
echo ""

setup_mariadb
echo ""
setup_postgresql

echo ""
log_ok "=== Plantillas DBaaS listas ==="
log_info "Próximos pasos:"
log_info "1. Convierte ambos discos a multiattach (ver comandos arriba)"
log_info "2. Actualiza config.json con las rutas de los discos:"
log_info "   mariadb_template_disk, postgresql_template_disk"
log_info "3. Arranca httpaas.exe / httpaas y ve a /dbaas"
