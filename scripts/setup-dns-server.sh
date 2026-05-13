#!/bin/bash
# =============================================================
# HTTPaaS - setup-dns-server.sh
# =============================================================
# Crea/configura la VM "httpaas-dns" que aloja Bind9 con la
# zona cloud.local. Pasos:
#
#  1. Clona la VM plantilla httpaas-template como "httpaas-dns".
#     (Si la VM ya existe, salta este paso).
#  2. Configura red para que la VM tenga IP fija 192.168.56.10.
#  3. La arranca, espera SSH.
#  4. Sube archivos de configuración Bind9 desde configs/bind9/.
#  5. Genera una clave TSIG, la inyecta en Bind9 y la deja en
#     webapp/data/dnskey.conf (lo que la webapp Go usa con nsupdate -k).
#  6. Reinicia Bind9 y verifica que responde a dig.
#
# Este script asume que setup-template-vm.sh y setup-host-network.sh
# ya corrieron antes.
# =============================================================
set -euo pipefail
source "$(dirname "$0")/common.sh"

DNS_VM="$HTTPAAS_DNS_VM"
DNS_IP="$HTTPAAS_DNS_IP"
DOMAIN="$HTTPAAS_DOMAIN"
TSIG_NAME="$HTTPAAS_TSIG_NAME"

require_cmd VBoxManage
require_cmd ssh
require_cmd scp
require_cmd dig

ensure_ssh_key

# -----------------------------------------------------------------
# 1. Clonar plantilla -> httpaas-dns (si no existe).
# -----------------------------------------------------------------
if VBoxManage list vms | grep -q "\"$DNS_VM\""; then
    log_info "VM $DNS_VM ya existe; no se clona."
else
    if ! VBoxManage list vms | grep -q "\"$HTTPAAS_TEMPLATE_VM\""; then
        die "Plantilla $HTTPAAS_TEMPLATE_VM no existe. Ejecuta setup-template-vm.sh primero."
    fi

    log_step "Clonando $HTTPAAS_TEMPLATE_VM → $DNS_VM"

    # Para el DNS NO usamos multiattach (queremos un disco propio
    # que pueda crecer y guardar el journal de Bind). Por eso clonamos
    # el disco como "linked" desde un snapshot.
    SNAP_NAME="base-snapshot"
    if ! VBoxManage snapshot "$HTTPAAS_TEMPLATE_VM" list 2>/dev/null | grep -q "$SNAP_NAME"; then
        VBoxManage snapshot "$HTTPAAS_TEMPLATE_VM" take "$SNAP_NAME" 2>/dev/null || \
            log_warn "No se pudo crear snapshot (¿disco multiattach?). Continuamos sin él."
    fi

    VBoxManage clonevm "$HTTPAAS_TEMPLATE_VM" \
        --snapshot "$SNAP_NAME" \
        --options link \
        --name "$DNS_VM" \
        --register 2>/dev/null || \
        VBoxManage clonevm "$HTTPAAS_TEMPLATE_VM" \
            --name "$DNS_VM" --register

    # Configurar red: host-only en NIC1 + NAT en NIC2 (para apt-get).
    VBoxManage modifyvm "$DNS_VM" \
        --nic1 hostonly --hostonlyadapter1 "$HTTPAAS_NETWORK" \
        --nic2 nat \
        --memory 768 --cpus 1
fi

# -----------------------------------------------------------------
# 2. Inyectar guest properties para que first-boot dé la IP fija.
# -----------------------------------------------------------------
log_step "Inyectando configuración de red para $DNS_VM"
VBoxManage guestproperty set "$DNS_VM" "/HTTPaaS/hostname" "dns"
VBoxManage guestproperty set "$DNS_VM" "/HTTPaaS/fqdn"     "dns.$DOMAIN"
VBoxManage guestproperty set "$DNS_VM" "/HTTPaaS/ip"       "$DNS_IP"
VBoxManage guestproperty set "$DNS_VM" "/HTTPaaS/netmask"  "$HTTPAAS_NETMASK"
VBoxManage guestproperty set "$DNS_VM" "/HTTPaaS/gateway"  "$HTTPAAS_HOST_IP"
# El propio DNS no se resuelve a sí mismo aún. Le decimos al firstboot
# que use 127.0.0.1 como DNS.
VBoxManage guestproperty set "$DNS_VM" "/HTTPaaS/dns"      "127.0.0.1"

# -----------------------------------------------------------------
# 3. Arrancar y esperar SSH.
# -----------------------------------------------------------------
STATE="$(VBoxManage showvminfo "$DNS_VM" --machinereadable | awk -F= '/^VMState=/{print $2}' | tr -d '"')"
if [[ "$STATE" != "running" ]]; then
    log_info "Arrancando $DNS_VM (headless)..."
    VBoxManage startvm "$DNS_VM" --type headless
fi

log_info "Esperando SSH en $DNS_IP:22..."
if ! wait_for_port "$DNS_IP" 22 240; then
    die "No hay SSH en $DNS_IP. Revisa la VM (¿first-boot OK?)"
fi

SSH_OPTS="-i $HTTPAAS_SSH_KEY -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"

# -----------------------------------------------------------------
# 4. Instalar Bind9 y subir configuración.
# -----------------------------------------------------------------
log_step "Instalando Bind9 en $DNS_VM"
ssh $SSH_OPTS "$HTTPAAS_SSH_USER@$DNS_IP" bash -s <<'REMOTE'
set -euo pipefail
if ! command -v named >/dev/null 2>&1; then
    sudo apt-get update -y
    sudo DEBIAN_FRONTEND=noninteractive apt-get install -y bind9 bind9-utils bind9-dnsutils
fi
sudo mkdir -p /etc/bind/zones
sudo chown -R bind:bind /etc/bind/zones
REMOTE

log_info "Subiendo zonas y configuración..."
scp $SSH_OPTS \
    "$CONFIGS_DIR/bind9/named.conf.options" \
    "$CONFIGS_DIR/bind9/named.conf.local" \
    "$HTTPAAS_SSH_USER@$DNS_IP:/tmp/"
scp $SSH_OPTS \
    "$CONFIGS_DIR/bind9/db.cloud.local" \
    "$CONFIGS_DIR/bind9/db.192.168.56" \
    "$HTTPAAS_SSH_USER@$DNS_IP:/tmp/"

ssh $SSH_OPTS "$HTTPAAS_SSH_USER@$DNS_IP" bash -s <<'REMOTE'
set -euo pipefail
sudo install -m 0644 -o root -g bind /tmp/named.conf.options /etc/bind/named.conf.options
sudo install -m 0644 -o root -g bind /tmp/named.conf.local   /etc/bind/named.conf.local
sudo install -m 0644 -o bind -g bind /tmp/db.cloud.local     /etc/bind/zones/db.cloud.local
sudo install -m 0644 -o bind -g bind /tmp/db.192.168.56      /etc/bind/zones/db.192.168.56
REMOTE

# -----------------------------------------------------------------
# 5. Generar clave TSIG si no existe ya.
# -----------------------------------------------------------------
log_step "Generando clave TSIG ($TSIG_NAME)"

KEY_OUT="$REPO_ROOT/webapp/data/dnskey.conf"
mkdir -p "$(dirname "$KEY_OUT")"

if [[ -f "$KEY_OUT" ]]; then
    log_info "Reutilizando clave existente en $KEY_OUT"
else
    ssh $SSH_OPTS "$HTTPAAS_SSH_USER@$DNS_IP" \
        "sudo tsig-keygen -a hmac-sha256 $TSIG_NAME" > "$KEY_OUT"
    chmod 600 "$KEY_OUT"
    log_ok "Clave TSIG escrita en $KEY_OUT"
fi

# La instalamos también en el servidor para que Bind la conozca.
scp $SSH_OPTS "$KEY_OUT" "$HTTPAAS_SSH_USER@$DNS_IP:/tmp/named.conf.keys"
ssh $SSH_OPTS "$HTTPAAS_SSH_USER@$DNS_IP" bash -s <<'REMOTE'
set -euo pipefail
sudo install -m 0640 -o root -g bind /tmp/named.conf.keys /etc/bind/named.conf.keys
sudo named-checkconf
sudo named-checkzone cloud.local.        /etc/bind/zones/db.cloud.local
sudo named-checkzone 56.168.192.in-addr.arpa. /etc/bind/zones/db.192.168.56
sudo systemctl enable --now named
sudo systemctl restart named
sleep 2
sudo systemctl status named --no-pager | head -n 10
REMOTE

# -----------------------------------------------------------------
# 6. Verificar.
# -----------------------------------------------------------------
log_step "Verificando resolución"
sleep 2
if dig +short @"$DNS_IP" "$DOMAIN" SOA | grep -q dns.cloud.local; then
    log_ok "Bind9 responde correctamente al SOA de $DOMAIN"
else
    log_warn "No se obtuvo SOA. Inspecciona con: dig @$DNS_IP $DOMAIN SOA"
fi

# Prueba de update dinámico para confirmar que TSIG funciona.
log_info "Probando nsupdate con TSIG..."
if command -v nsupdate >/dev/null 2>&1; then
    nsupdate -k "$KEY_OUT" <<NSUP
server $DNS_IP
zone $DOMAIN.
update add test.$DOMAIN. 60 A 192.168.56.99
send
NSUP
    if dig +short @"$DNS_IP" "test.$DOMAIN" | grep -q 192.168.56.99; then
        log_ok "Update dinámico TSIG OK"
        # Borramos el registro de prueba para no dejar basura.
        nsupdate -k "$KEY_OUT" <<NSUP2 || true
server $DNS_IP
zone $DOMAIN.
update delete test.$DOMAIN. A
send
NSUP2
    else
        log_warn "El update se envió pero dig no lo confirma. Revisa journalctl -u named en la VM DNS."
    fi
else
    log_warn "nsupdate no instalado en el host (apt install bind9-dnsutils). Salto verificación."
fi

log_ok "Servidor DNS listo: $DNS_VM @ $DNS_IP (zona $DOMAIN)"
log_info "Llave TSIG persistida en: $KEY_OUT"
