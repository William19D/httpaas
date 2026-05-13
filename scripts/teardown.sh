#!/bin/bash
# =============================================================
# HTTPaaS - teardown.sh
# =============================================================
# Limpieza completa para empezar de cero. PIDE CONFIRMACIÓN.
#
# Acciones:
#  - apaga y borra todas las VMs httpaas-*
#  - elimina la red host-only
#  - quita el servicio systemd httpaas
#  - borra webapp/uploads/* y webapp/data/instances.json
#
# NO borra:
#  - la ISO de Debian descargada (~700 MB)
#  - la llave SSH del proyecto
# =============================================================
set -euo pipefail
source "$(dirname "$0")/common.sh"

echo
log_warn "Esto eliminará TODAS las VMs httpaas-* y la red host-only."
read -rp "Escribe 'borrar' para confirmar: " CONF
if [[ "$CONF" != "borrar" ]]; then
    log_info "Cancelado."
    exit 0
fi

log_step "Servicio systemd"
if systemctl list-unit-files | grep -q httpaas.service; then
    sudo systemctl disable --now httpaas 2>/dev/null || true
    sudo rm -f /etc/systemd/system/httpaas.service
    sudo systemctl daemon-reload
    log_ok "Servicio httpaas removido"
fi

log_step "VMs HTTPaaS"
mapfile -t VMS < <(VBoxManage list vms 2>/dev/null | awk -F\" '/httpaas/ {print $2}')
for vm in "${VMS[@]}"; do
    log_info "Eliminando VM $vm"
    VBoxManage controlvm "$vm" poweroff 2>/dev/null || true
    sleep 1
    VBoxManage unregistervm "$vm" --delete 2>/dev/null || true
done

log_step "Red host-only"
if VBoxManage list hostonlyifs | grep -q "^Name: *${HTTPAAS_NETWORK}$"; then
    VBoxManage hostonlyif remove "$HTTPAAS_NETWORK" 2>/dev/null || \
        log_warn "No se pudo borrar $HTTPAAS_NETWORK (¿hay VMs vivas usándola?)"
fi

log_step "Datos de la webapp"
rm -rf "$WEBAPP_DIR/uploads" "$WEBAPP_DIR/data/instances.json"
rm -f  "$WEBAPP_DIR/httpaas"

log_ok "Teardown completo."
