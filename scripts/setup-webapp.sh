#!/bin/bash
# =============================================================
# HTTPaaS - setup-webapp.sh
# =============================================================
# Compila la webapp Go y la deja lista para usar como servicio
# systemd. Pasos:
#  1. Verifica Go >= 1.22.
#  2. go build → webapp/httpaas (binario estático).
#  3. Genera webapp/config.json con valores reales.
#  4. Instala unidad systemd /etc/systemd/system/httpaas.service.
# =============================================================
set -euo pipefail
source "$(dirname "$0")/common.sh"

require_cmd go
require_cmd jq 2>/dev/null || true  # jq es opcional; sólo para mostrar config bonita

# 1. Versión de Go --------------------------------------------------
GO_VER="$(go version | awk '{print $3}' | sed 's/go//')"
GO_MAJOR="$(echo "$GO_VER" | cut -d. -f1)"
GO_MINOR="$(echo "$GO_VER" | cut -d. -f2)"
if (( GO_MAJOR < 1 )) || { (( GO_MAJOR == 1 )) && (( GO_MINOR < 22 )); }; then
    die "Se requiere Go >= 1.22 (tienes $GO_VER)"
fi
log_info "Usando Go $GO_VER"

# 2. Build ----------------------------------------------------------
log_step "Compilando webapp HTTPaaS"
cd "$WEBAPP_DIR"
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o httpaas ./cmd/httpaas
log_ok "Binario en $WEBAPP_DIR/httpaas ($(du -h httpaas | awk '{print $1}'))"

# 3. config.json con rutas correctas --------------------------------
log_step "Generando config.json"
cat > "$WEBAPP_DIR/config.json" <<JSON
{
  "http":       { "addr": "0.0.0.0:8080" },
  "dns": {
    "domain":    "${HTTPAAS_DOMAIN}.",
    "server_ip": "${HTTPAAS_DNS_IP}",
    "zone_file": "/etc/bind/zones/db.${HTTPAAS_DOMAIN}",
    "tsig_key":  "${WEBAPP_DIR}/data/dnskey.conf",
    "tsig_name": "${HTTPAAS_TSIG_NAME}"
  },
  "network": {
    "host_only_net":  "${HTTPAAS_NETWORK}",
    "subnet":         "${HTTPAAS_SUBNET}",
    "gateway":        "${HTTPAAS_HOST_IP}",
    "ip_range_start": "192.168.56.100",
    "ip_range_end":   "192.168.56.200"
  },
  "virtualbox": {
    "template_vm":   "${HTTPAAS_TEMPLATE_VM}",
    "template_disk": "${HOME}/VirtualBox VMs/${HTTPAAS_TEMPLATE_VM}/${HTTPAAS_TEMPLATE_VM}.vdi",
    "vm_folder":     "${HOME}/VirtualBox VMs",
    "memory_mb":     768,
    "cpus":          1
  },
  "ssh": {
    "user":        "${HTTPAAS_SSH_USER}",
    "key_path":    "${HTTPAAS_SSH_KEY}",
    "port":        22,
    "timeout_sec": 90
  },
  "upload_dir":   "${WEBAPP_DIR}/uploads",
  "data_dir":     "${WEBAPP_DIR}/data",
  "template_dir": "${WEBAPP_DIR}/templates",
  "static_dir":   "${WEBAPP_DIR}/static",
  "scripts_dir":  "${SCRIPTS_DIR}"
}
JSON
log_ok "config.json escrito"

mkdir -p "$WEBAPP_DIR/uploads" "$WEBAPP_DIR/data"

# 4. systemd unit ---------------------------------------------------
log_step "Instalando httpaas.service"

SVC_TMP="$(mktemp)"
sed -e "s|__USER__|$USER|g" \
    -e "s|__HOME__|$HOME|g" \
    -e "s|__WORKDIR__|$WEBAPP_DIR|g" \
    "$CONFIGS_DIR/systemd/httpaas.service" > "$SVC_TMP"

if [[ "$(id -u)" -eq 0 ]]; then
    install -m 0644 "$SVC_TMP" /etc/systemd/system/httpaas.service
    systemctl daemon-reload
    log_ok "Unidad instalada como root. Habilita con: systemctl enable --now httpaas"
else
    log_warn "No se está corriendo como root. La unidad systemd se generó en:"
    log_warn "   $SVC_TMP"
    log_warn "Cópiala manualmente:"
    log_warn "   sudo cp $SVC_TMP /etc/systemd/system/httpaas.service"
    log_warn "   sudo systemctl daemon-reload && sudo systemctl enable --now httpaas"
fi

log_ok "Webapp lista."
log_info "Para arrancar en primer plano (desarrollo):"
log_info "   cd $WEBAPP_DIR && ./httpaas"
log_info "Para arrancar como servicio:"
log_info "   sudo systemctl enable --now httpaas && journalctl -u httpaas -f"
