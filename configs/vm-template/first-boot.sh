#!/bin/bash
# =============================================================
# HTTPaaS - first-boot.sh
# =============================================================
# Se ejecuta en cada arranque DENTRO de la VM (no en el host).
# Lee las "guest properties" inyectadas por la webapp Go vía
# VBoxManage guestproperty set, y configura:
#   - hostname y /etc/hosts
#   - IP estática en la interfaz host-only
#   - resolver DNS (apunta al servidor Bind9)
#   - ServerName de Apache
#   - reinicia los servicios afectados
#
# Si ya está aplicado en un arranque previo, el script es idempotente:
# detecta el flag /var/lib/httpaas/applied y se va. Cuando la webapp
# necesita re-aplicar configuración, borra el flag por SSH.
#
# Requiere virtualbox-guest-utils para tener VBoxControl disponible.
# =============================================================
set -euo pipefail

LOG=/var/log/httpaas-firstboot.log
exec >>"$LOG" 2>&1
echo "================ $(date -Is) first-boot start ================"

mkdir -p /var/lib/httpaas

# --- Helper para leer una guest property -------------------------
gp() {
    local name="$1"
    # Salida típica: "Value: foo"; -1 si no existe.
    local raw
    raw="$(VBoxControl --nologo guestproperty get "$name" 2>/dev/null || true)"
    if [[ "$raw" == "No value set!" || -z "$raw" ]]; then
        echo ""
        return 0
    fi
    echo "${raw#Value: }"
}

# Esperamos a que el demonio de VBoxService esté listo (a veces tarda 1-2s).
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

echo "HOSTNAME=$HOSTNAME FQDN=$FQDN IP=$IP NETMASK=$NETMASK GW=$GATEWAY DNS=$DNS"

if [[ -z "$HOSTNAME" || -z "$IP" ]]; then
    echo "guest properties incompletas; reintentando en el próximo boot"
    exit 0
fi

# Idempotencia: si ya configuramos esta combinación, salimos.
SIG="$HOSTNAME|$IP|$DNS"
PREV="$(cat /var/lib/httpaas/applied 2>/dev/null || true)"
if [[ "$SIG" == "$PREV" ]]; then
    echo "configuración ya aplicada ($SIG); nada que hacer"
    exit 0
fi

# --- 1. Hostname --------------------------------------------------
echo "$HOSTNAME" >/etc/hostname
hostnamectl set-hostname "$HOSTNAME" || true

# Reescribir /etc/hosts dejando 127.0.0.1 y agregando entrada propia.
cat >/etc/hosts <<EOF
127.0.0.1       localhost
127.0.1.1       $HOSTNAME $FQDN
$IP             $FQDN $HOSTNAME

# IPv6 mínimo
::1     localhost ip6-localhost ip6-loopback
ff02::1 ip6-allnodes
ff02::2 ip6-allrouters
EOF

# --- 2. Red estática vía systemd-networkd ------------------------
# Debian 13 trae systemd-networkd disponible; lo usamos en lugar de
# /etc/network/interfaces para evitar incompatibilidades con NetworkManager.
#
# enp0s3 = NIC1 (host-only) ; enp0s8 = NIC2 (NAT) en VirtualBox.
mkdir -p /etc/systemd/network

cat >/etc/systemd/network/10-httpaas-host.network <<EOF
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

cat >/etc/systemd/network/20-httpaas-nat.network <<EOF
[Match]
Name=enp0s8

[Network]
DHCP=yes
# Esta NIC es para Internet (apt-get, etc.). No la queremos como default
# si la host-only ya da gateway; bajamos su métrica de ruta.
[DHCP]
RouteMetric=1024
UseDNS=no
EOF

# Resolver. Usamos resolv.conf estático apuntando al DNS del proyecto.
# Si systemd-resolved está activo, deshabilitamos su stub para que
# nada lo sobreescriba.
systemctl disable --now systemd-resolved 2>/dev/null || true
rm -f /etc/resolv.conf
cat >/etc/resolv.conf <<EOF
search cloud.local
nameserver $DNS
EOF
chattr +i /etc/resolv.conf 2>/dev/null || true

systemctl enable --now systemd-networkd
systemctl restart  systemd-networkd

# Esperar a que la IP suba antes de seguir.
for i in {1..20}; do
    if ip -4 addr show enp0s3 | grep -q "inet $IP"; then break; fi
    sleep 1
done
ip -4 addr show enp0s3 || true

# --- 3. Apache: sustituir __HOSTNAME__ ---------------------------
if [[ -f /etc/apache2/sites-available/000-default.conf ]]; then
    sed -i "s/__HOSTNAME__/$HOSTNAME/g" /etc/apache2/sites-available/000-default.conf
fi
systemctl enable --now apache2
systemctl reload  apache2 || systemctl restart apache2

# --- 4. SSH: ya viene activado, pero por si acaso ----------------
systemctl enable --now ssh

# --- 5. Marcar aplicado ------------------------------------------
echo "$SIG" >/var/lib/httpaas/applied
echo "================ $(date -Is) first-boot OK ================"
