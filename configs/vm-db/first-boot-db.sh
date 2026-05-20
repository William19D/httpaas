#!/bin/bash
# =============================================================
# HTTPaaS DBaaS — first-boot-db.sh
# =============================================================
# Se ejecuta en cada arranque DENTRO de la VM de base de datos
# (no en el host). Lee las "guest properties" inyectadas por la
# webapp Go vía VBoxManage guestproperty set, y configura:
#   - hostname y /etc/hosts
#   - IP estática en la interfaz host-only (enp0s3)
#   - resolver DNS apuntando al Bind9 del proyecto
#   - acceso remoto en el motor de BD (MariaDB o PostgreSQL)
#   - SSH habilitado
#
# Requiere virtualbox-guest-utils (VBoxControl disponible).
# =============================================================
set -euo pipefail

LOG=/var/log/httpaas-db-firstboot.log
exec >>"$LOG" 2>&1
echo "================ $(date -Is) first-boot-db start ================"

mkdir -p /var/lib/httpaas

# --- Helper para leer una guest property -------------------------
gp() {
    local name="$1"
    local raw
    raw="$(VBoxControl --nologo guestproperty get "$name" 2>/dev/null || true)"
    if [[ "$raw" == "No value set!" || -z "$raw" ]]; then
        echo ""
        return 0
    fi
    echo "${raw#Value: }"
}

# Esperar a que el guest-agent esté listo.
for i in 1 2 3 4 5; do
    if VBoxControl --nologo guestproperty enumerate >/dev/null 2>&1; then break; fi
    sleep 1
done

HOSTNAME="$(gp /HTTPaaS/hostname)"
FQDN="$(gp     /HTTPaaS/fqdn)"
IP="$(gp       /HTTPaaS/ip)"
NETMASK="$(gp  /HTTPaaS/netmask)"
GATEWAY="$(gp  /HTTPaaS/gateway)"
DNS="$(gp      /HTTPaaS/dns)"
ENGINE="$(gp   /HTTPaaS/engine)"

echo "HOSTNAME=$HOSTNAME FQDN=$FQDN IP=$IP GW=$GATEWAY DNS=$DNS ENGINE=$ENGINE"

if [[ -z "$HOSTNAME" || -z "$IP" ]]; then
    echo "Guest properties incompletas; reintentando en el próximo boot"
    exit 0
fi

# Idempotencia: si ya aplicamos esta configuración, salimos.
SIG="$HOSTNAME|$IP|$ENGINE"
PREV="$(cat /var/lib/httpaas/applied 2>/dev/null || true)"
if [[ "$SIG" == "$PREV" ]]; then
    echo "Configuración ya aplicada ($SIG); nada que hacer"
    exit 0
fi

# --- 1. Hostname --------------------------------------------------
echo "$HOSTNAME" > /etc/hostname
hostnamectl set-hostname "$HOSTNAME" || true

cat > /etc/hosts <<EOF
127.0.0.1       localhost
127.0.1.1       $HOSTNAME $FQDN
$IP             $FQDN $HOSTNAME

::1     localhost ip6-localhost ip6-loopback
ff02::1 ip6-allnodes
ff02::2 ip6-allrouters
EOF

# --- 2. Red estática (systemd-networkd) ---------------------------
mkdir -p /etc/systemd/network

cat > /etc/systemd/network/10-httpaas-host.network <<EOF
[Match]
Name=enp0s3

[Network]
Address=$IP/24
Gateway=$GATEWAY
DNS=$DNS
Domains=cloud.local

[Route]
Destination=192.168.56.0/24
Scope=link
EOF

cat > /etc/systemd/network/20-httpaas-nat.network <<EOF
[Match]
Name=enp0s8

[Network]
DHCP=yes
[DHCP]
RouteMetric=1024
UseDNS=no
EOF

# Fijar resolv.conf
systemctl disable --now systemd-resolved 2>/dev/null || true
rm -f /etc/resolv.conf
cat > /etc/resolv.conf <<EOF
search cloud.local
nameserver $DNS
EOF
chattr +i /etc/resolv.conf 2>/dev/null || true

systemctl enable --now systemd-networkd
systemctl restart  systemd-networkd

# Esperar a que la IP quede asignada.
for i in {1..20}; do
    if ip -4 addr show enp0s3 | grep -q "inet $IP"; then break; fi
    sleep 1
done
ip -4 addr show enp0s3 || true

# --- 3. Configurar el motor de BD para escuchar en todas las IPs --

if [[ "$ENGINE" == "mariadb" ]]; then
    echo "Configurando MariaDB para acceso remoto…"

    # Deshabilitar bind-address=127.0.0.1 (si está activo) para escuchar en 0.0.0.0
    if grep -qr "bind-address" /etc/mysql/; then
        sed -i 's/^bind-address\s*=.*$/bind-address = 0.0.0.0/' \
            /etc/mysql/mariadb.conf.d/50-server.cnf 2>/dev/null || \
        sed -i 's/^bind-address\s*=.*$/bind-address = 0.0.0.0/' \
            /etc/mysql/my.cnf 2>/dev/null || true
    else
        echo "[mysqld]" >> /etc/mysql/mariadb.conf.d/50-server.cnf
        echo "bind-address = 0.0.0.0" >> /etc/mysql/mariadb.conf.d/50-server.cnf
    fi

    systemctl enable --now mariadb
    systemctl restart mariadb

    # Asegurarse de que root puede autenticarse sin contraseña desde localhost
    # (la webapp usará SSH para ejecutar los comandos mysql como root en la VM).
    mysql -u root -e "ALTER USER 'root'@'localhost' IDENTIFIED VIA unix_socket; FLUSH PRIVILEGES;" \
        2>/dev/null || true

    echo "MariaDB configurado y en ejecución"

elif [[ "$ENGINE" == "postgresql" ]]; then
    echo "Configurando PostgreSQL para acceso remoto…"

    # Detectar versión instalada de PostgreSQL
    PG_VERSION=$(ls /etc/postgresql/ 2>/dev/null | head -1)
    PG_CONF="/etc/postgresql/$PG_VERSION/main/postgresql.conf"
    PG_HBA="/etc/postgresql/$PG_VERSION/main/pg_hba.conf"

    if [[ -n "$PG_VERSION" && -f "$PG_CONF" ]]; then
        # Escuchar en todas las interfaces
        sed -i "s/^#\?listen_addresses\s*=.*/listen_addresses = '*'/" "$PG_CONF"

        # Permitir conexiones remotas con contraseña (md5 o scram-sha-256)
        if ! grep -q "host.*all.*all.*0.0.0.0/0" "$PG_HBA"; then
            echo "host all all 0.0.0.0/0 scram-sha-256" >> "$PG_HBA"
            echo "host all all ::/0        scram-sha-256" >> "$PG_HBA"
        fi

        systemctl enable --now postgresql
        systemctl restart postgresql
        echo "PostgreSQL $PG_VERSION configurado y en ejecución"
    else
        echo "ADVERTENCIA: no se encontró instalación de PostgreSQL"
    fi
else
    echo "ENGINE desconocido: $ENGINE — nada que configurar para BD"
fi

# --- 4. SSH: habilitar y asegurar que corre -----------------------
systemctl enable --now ssh

# --- 5. Marcar como aplicado -------------------------------------
echo "$SIG" > /var/lib/httpaas/applied
echo "================ $(date -Is) first-boot-db OK ================"
