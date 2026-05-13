#!/bin/bash
# =============================================================
# HTTPaaS - setup-host-network.sh
# =============================================================
# Configura la red host-only de VirtualBox que conecta el host
# (donde corre la webapp Go) con todas las VMs (DNS + instancias
# Apache).
#
# Idempotente: si vboxnet0 ya existe con la IP correcta, no hace nada.
#
# REQUIERE: VirtualBox >= 7 instalado, usuario en grupo vboxusers.
# =============================================================
set -euo pipefail

source "$(dirname "$0")/common.sh"

NET_NAME="${NET_NAME:-vboxnet0}"
NET_IP="${NET_IP:-192.168.56.1}"
NET_MASK="${NET_MASK:-255.255.255.0}"
NET_RANGE="${NET_RANGE:-192.168.56.0/24}"

log_step "Configurando red host-only $NET_NAME ($NET_IP/$NET_MASK)"

require_cmd VBoxManage

# 1. ¿Existe ya?
if VBoxManage list hostonlyifs 2>/dev/null | grep -q "^Name: *${NET_NAME}$"; then
    log_info "Interface $NET_NAME ya existe."
else
    log_info "Creando interfaz host-only..."
    # `hostonlyif create` no permite escoger el nombre; el nombre lo asigna
    # VirtualBox secuencialmente (vboxnet0, vboxnet1, ...). En la práctica,
    # en una instalación limpia siempre sale vboxnet0.
    CREATED="$(VBoxManage hostonlyif create | sed -n "s/.*'\(vboxnet[0-9]\+\)'.*/\1/p")"
    if [[ -z "$CREATED" ]]; then
        die "VBoxManage hostonlyif create no devolvió un nombre. ¿Permisos?"
    fi
    if [[ "$CREATED" != "$NET_NAME" ]]; then
        log_warn "Se creó $CREATED pero esperábamos $NET_NAME. Ajustando config..."
        NET_NAME="$CREATED"
    fi
fi

# 2. Asignar IP fija al adaptador.
log_info "Asignando IP $NET_IP/$NET_MASK al $NET_NAME"
VBoxManage hostonlyif ipconfig "$NET_NAME" \
    --ip "$NET_IP" --netmask "$NET_MASK"

# 3. Apagar el DHCP de VirtualBox para esta red (si estaba activo).
#    Lo manejamos con IPs estáticas, así no hay sorpresas.
if VBoxManage list dhcpservers 2>/dev/null | grep -q "HostInterfaceNetworking-${NET_NAME}"; then
    log_info "Deshabilitando DHCP server de VirtualBox sobre $NET_NAME"
    VBoxManage dhcpserver remove --interface "$NET_NAME" 2>/dev/null || true
fi

# 4. Verificar que la red está en el rango deseado.
HOST_IP="$(ip -4 addr show "$NET_NAME" 2>/dev/null | awk '/inet / {print $2}' | head -n1 || true)"
if [[ -z "$HOST_IP" ]]; then
    log_warn "No se ve interfaz $NET_NAME en el host todavía; revisa con 'ip addr'."
else
    log_info "Host tiene $NET_NAME = $HOST_IP"
fi

# 5. Persistir el nombre por si cambió.
mkdir -p "$REPO_ROOT/webapp/data"
echo "$NET_NAME" > "$REPO_ROOT/webapp/data/.hostonly_iface"

log_ok "Red host-only lista: $NET_NAME ($NET_IP/$NET_MASK)"
