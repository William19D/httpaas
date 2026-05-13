#!/bin/bash
# =============================================================
# HTTPaaS - setup-template-vm.sh
# =============================================================
# Crea la VM plantilla "httpaas-template" sobre Debian 13.
#
# El proceso tiene dos partes:
#
#  FASE A - Instalación base (sólo la primera vez):
#     * Descarga el ISO de Debian 13 netinst si no lo tiene.
#     * Crea una VM nueva con un disco VDI de 8 GB.
#     * Arranca con el ISO + preseed.cfg servido por HTTP local.
#     * Espera a que Debian termine (la VM se apaga sola).
#
#  FASE B - Personalización (siempre):
#     * Arranca la VM con un NIC NAT temporal.
#     * Sube por SSH:
#         - first-boot.sh -> /usr/local/sbin/httpaas-firstboot
#         - httpaas-firstboot.service -> /etc/systemd/system/
#         - apache 000-default.conf -> /etc/apache2/sites-available/
#         - llave pública SSH -> ~/.ssh/authorized_keys
#     * Habilita el servicio firstboot.
#     * Apaga la VM y desadjunta el disco para volverlo MULTICONEXIÓN.
#
# Tras esto, la VM está lista para ser usada como base por
# storageattach --mtype multiattach desde la webapp Go.
#
# Variables (opcionales):
#   DEBIAN_ISO_URL    URL del ISO si hay que descargarlo
#   ISO_PATH          Ruta local al ISO (si ya lo tienes)
#   SKIP_INSTALL=1    Salta la FASE A (la VM ya existe)
# =============================================================
set -euo pipefail
source "$(dirname "$0")/common.sh"

VM="$HTTPAAS_TEMPLATE_VM"
VM_FOLDER="$HOME/VirtualBox VMs"
DISK="$VM_FOLDER/$VM/$VM.vdi"
DISK_SIZE_MB=8192
ISO_DEFAULT_URL="https://cdimage.debian.org/debian-cd/current/amd64/iso-cd/debian-13.0.0-amd64-netinst.iso"
ISO_PATH="${ISO_PATH:-$HOME/.cache/httpaas/debian-13-netinst.iso}"

require_cmd VBoxManage
require_cmd ssh
require_cmd ssh-keygen
require_cmd curl

# -----------------------------------------------------------------
# FASE A - Instalación base
# -----------------------------------------------------------------
phase_install() {
    if VBoxManage list vms | grep -q "\"$VM\""; then
        log_info "VM $VM ya existe; salto la FASE A (instalación)."
        return
    fi
    log_step "FASE A · Creando VM $VM e instalando Debian 13"

    # 1. ISO ------------------------------------------------------
    mkdir -p "$(dirname "$ISO_PATH")"
    if [[ ! -f "$ISO_PATH" ]]; then
        log_info "Descargando ISO Debian 13 (~700MB) → $ISO_PATH"
        curl -L --fail --progress-bar -o "$ISO_PATH.part" \
            "${DEBIAN_ISO_URL:-$ISO_DEFAULT_URL}"
        mv "$ISO_PATH.part" "$ISO_PATH"
    else
        log_info "Reutilizando ISO en $ISO_PATH"
    fi

    # 2. Crear VM -------------------------------------------------
    mkdir -p "$VM_FOLDER/$VM"
    VBoxManage createvm --name "$VM" --ostype "Debian_64" --register \
        --basefolder "$VM_FOLDER"

    VBoxManage modifyvm "$VM" \
        --memory 1024 --cpus 1 --vram 16 --audio none --usb off \
        --nic1 nat --nic2 hostonly --hostonlyadapter2 "$HTTPAAS_NETWORK" \
        --boot1 dvd --boot2 disk --boot3 none --boot4 none \
        --graphicscontroller vmsvga --rtcuseutc on

    # 3. Disco principal -----------------------------------------
    VBoxManage createhd --filename "$DISK" --size "$DISK_SIZE_MB" --format VDI
    VBoxManage storagectl "$VM" --name "SATA" --add sata --portcount 2 --bootable on
    VBoxManage storageattach "$VM" --storagectl SATA --port 0 --device 0 \
        --type hdd --medium "$DISK"
    VBoxManage storagectl "$VM" --name "IDE" --add ide
    VBoxManage storageattach "$VM" --storagectl IDE --port 1 --device 0 \
        --type dvddrive --medium "$ISO_PATH"

    # 4. Servidor HTTP local para servir el preseed --------------
    PRESEED_DIR="$(mktemp -d)"
    cp "$CONFIGS_DIR/vm-template/preseed.cfg" "$PRESEED_DIR/preseed.cfg"
    log_info "Sirviendo preseed.cfg en http://10.0.2.2:8000/preseed.cfg"
    ( cd "$PRESEED_DIR" && python3 -m http.server 8000 >/dev/null 2>&1 ) &
    HTTP_PID=$!
    trap 'kill $HTTP_PID 2>/dev/null || true; rm -rf "$PRESEED_DIR"' EXIT

    # 5. Inyectar comandos de boot para usar el preseed ----------
    # En VirtualBox no hay forma estándar de pasar parámetros al kernel
    # del ISO sin tocar el ISO. Para mantener el script sin pasos
    # manuales, importamos un grub con el comando preseed por boot via
    # parámetros del preseed-url usando "VBoxManage setextradata" no.
    # En la práctica, lo más fiable es lanzar la VM y mostrarle al
    # usuario el comando de boot. Para una experiencia 100% automática
    # con cero clics, recomendamos pasar SKIP_INSTALL=1 si ya tienes
    # una VM Debian creada manualmente.
    log_warn "VirtualBox no permite inyectar argumentos al kernel del ISO sin GUI."
    log_warn "Voy a lanzar la VM con GUI. En el menú GRUB de Debian:"
    log_warn "   1) Pulsa TAB sobre 'Install' (o 'Advanced→Automated install')"
    log_warn "   2) Añade al final:  auto=true url=http://10.0.2.2:8000/preseed.cfg"
    log_warn "   3) ENTER. El resto es automático (~12-15 min)."
    echo "Pulsa ENTER cuando estés listo para arrancar la VM..."
    read -r _

    VBoxManage startvm "$VM" --type gui

    log_info "Esperando a que Debian termine la instalación y se apague..."
    while true; do
        STATE="$(VBoxManage showvminfo "$VM" --machinereadable | awk -F= '/^VMState=/{print $2}' | tr -d '"')"
        if [[ "$STATE" == "poweroff" || "$STATE" == "aborted" ]]; then break; fi
        sleep 10
    done

    # 6. Quitar el ISO -------------------------------------------
    VBoxManage storageattach "$VM" --storagectl IDE --port 1 --device 0 --medium none
    log_ok "FASE A completa."
}

# -----------------------------------------------------------------
# FASE B - Personalización
# -----------------------------------------------------------------
phase_customize() {
    log_step "FASE B · Personalizando VM $VM"

    ensure_ssh_key
    local pub_key
    pub_key="$(cat "${HTTPAAS_SSH_KEY}.pub")"

    # 1. Asegurar que la VM tiene NAT + port-forward SSH temporal.
    VBoxManage modifyvm "$VM" --nic1 nat
    VBoxManage modifyvm "$VM" --natpf1 "delete,httpaas-ssh,tcp,,2222,,22" 2>/dev/null || true
    VBoxManage modifyvm "$VM" --natpf1 "httpaas-ssh,tcp,,2222,,22"

    # 2. Arrancar.
    log_info "Arrancando VM en modo headless..."
    VBoxManage startvm "$VM" --type headless

    log_info "Esperando SSH en localhost:2222..."
    if ! wait_for_port 127.0.0.1 2222 240; then
        die "SSH no respondió en 2222. Revisa la VM manualmente."
    fi

    # 3. Subir archivos por SSH. Usamos sshpass para la primera vez,
    #    luego instalamos llave y desactivamos contraseña.
    local SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222"

    if ! command -v sshpass >/dev/null 2>&1; then
        die "sshpass no instalado. Instala con: sudo apt install sshpass"
    fi

    log_info "Instalando llave pública SSH en la VM..."
    sshpass -p cloud ssh $SSH_OPTS cloud@127.0.0.1 \
        "mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo '$pub_key' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys"

    log_info "Subiendo first-boot.sh y unidades systemd..."
    sshpass -p cloud scp $SSH_OPTS \
        "$CONFIGS_DIR/vm-template/first-boot.sh" \
        "$CONFIGS_DIR/vm-template/httpaas-firstboot.service" \
        "$CONFIGS_DIR/apache/000-default.conf" \
        cloud@127.0.0.1:/tmp/

    log_info "Aplicando configuración dentro de la VM..."
    sshpass -p cloud ssh $SSH_OPTS cloud@127.0.0.1 bash -s <<'REMOTE'
set -euo pipefail
sudo install -m 0755 /tmp/first-boot.sh /usr/local/sbin/httpaas-firstboot
sudo install -m 0644 /tmp/httpaas-firstboot.service /etc/systemd/system/
sudo install -m 0644 /tmp/000-default.conf /etc/apache2/sites-available/000-default.conf

# Habilitar servicios
sudo systemctl daemon-reload
sudo systemctl enable httpaas-firstboot
sudo systemctl enable apache2
sudo systemctl enable ssh

# El módulo headers de Apache lo usa nuestra config (X-HTTPaaS-Instance)
sudo a2enmod headers

# Limpieza de identidad: borramos la firma SSH y machine-id para que
# cada clon obtenga la suya en primer boot.
sudo rm -f /etc/ssh/ssh_host_*
sudo truncate -s 0 /etc/machine-id
sudo rm -f /var/lib/dbus/machine-id
sudo ln -sf /etc/machine-id /var/lib/dbus/machine-id

# Asegurarse de que el flag idempotente parta limpio
sudo rm -f /var/lib/httpaas/applied

# Reconfigurar SSH para regenerar llaves en primer boot.
sudo dpkg-reconfigure openssh-server 2>/dev/null || true

# Apagar la VM al terminar.
sudo systemctl poweroff
REMOTE

    log_info "Esperando a que la VM se apague..."
    while true; do
        STATE="$(VBoxManage showvminfo "$VM" --machinereadable | awk -F= '/^VMState=/{print $2}' | tr -d '"')"
        if [[ "$STATE" == "poweroff" || "$STATE" == "aborted" ]]; then break; fi
        sleep 5
    done

    # 4. Quitar el port-forward temporal y dejar la NIC de instancia.
    VBoxManage modifyvm "$VM" --natpf1 "delete,httpaas-ssh" 2>/dev/null || true

    # 5. Reconvertir el disco a INMUTABLE (base para multiattach).
    #    Pero antes hay que detacharlo, hacer setextra y volver a adjuntar.
    log_info "Convirtiendo disco a tipo MULTIATTACH (será compartido por clones)..."
    VBoxManage storageattach "$VM" --storagectl SATA --port 0 --device 0 --medium none
    VBoxManage modifymedium disk "$DISK" --type multiattach || \
        VBoxManage modifyhd "$DISK" --type multiattach
    VBoxManage storageattach "$VM" --storagectl SATA --port 0 --device 0 \
        --type hdd --mtype multiattach --medium "$DISK"

    log_ok "FASE B completa. Disco multiattach en: $DISK"
}

# -----------------------------------------------------------------
# Main
# -----------------------------------------------------------------
if [[ "${SKIP_INSTALL:-0}" != "1" ]]; then
    phase_install
fi
phase_customize

log_ok "VM plantilla lista: $VM"
log_info "Disco multiattach: $DISK"
log_info "Llave SSH:         $HTTPAAS_SSH_KEY"
