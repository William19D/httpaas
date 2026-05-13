#!/bin/bash
# =============================================================
# HTTPaaS - check-prereqs.sh
# =============================================================
# Verifica que el host tenga todo lo necesario antes de continuar.
# Si falta algo crítico, ofrece el comando para instalarlo.
# =============================================================
set -euo pipefail
source "$(dirname "$0")/common.sh"

OK=1

check() {
    local name="$1"
    local cmd="$2"
    local install_hint="$3"
    if command -v "$cmd" >/dev/null 2>&1; then
        local ver
        ver="$("$cmd" --version 2>&1 | head -n1 || true)"
        log_ok "$name → $ver"
    else
        log_error "$name no encontrado. Instala con:  $install_hint"
        OK=0
    fi
}

log_step "Verificando prerrequisitos del host"

check "VirtualBox"  VBoxManage "sudo apt install virtualbox virtualbox-ext-pack"
check "Go"          go         "sudo apt install golang-go  (o snap install go --classic)"
check "ssh"         ssh        "sudo apt install openssh-client"
check "scp"         scp        "sudo apt install openssh-client"
check "ssh-keygen"  ssh-keygen "sudo apt install openssh-client"
check "sshpass"     sshpass    "sudo apt install sshpass"
check "dig"         dig        "sudo apt install bind9-dnsutils"
check "nsupdate"    nsupdate   "sudo apt install bind9-dnsutils"
check "curl"        curl       "sudo apt install curl"
check "unzip"       unzip      "sudo apt install unzip"
check "python3"     python3    "sudo apt install python3"

# Detectar grupo vboxusers
if id -nG | tr ' ' '\n' | grep -qx vboxusers; then
    log_ok "Usuario está en grupo vboxusers"
else
    log_warn "Usuario NO está en grupo vboxusers. Añade con:"
    log_warn "   sudo usermod -aG vboxusers $USER && newgrp vboxusers"
fi

# Verificar versión de Go
if command -v go >/dev/null 2>&1; then
    GO_VER="$(go version | awk '{print $3}' | sed 's/go//')"
    GO_MAJOR="$(echo "$GO_VER" | cut -d. -f1)"
    GO_MINOR="$(echo "$GO_VER" | cut -d. -f2)"
    if (( GO_MAJOR < 1 )) || { (( GO_MAJOR == 1 )) && (( GO_MINOR < 22 )); }; then
        log_warn "Go $GO_VER es < 1.22. La webapp puede no compilar."
        OK=0
    fi
fi

# Memoria mínima recomendada
TOTAL_MB="$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)"
if (( TOTAL_MB < 4096 )); then
    log_warn "Sólo $TOTAL_MB MB de RAM. Recomendado >= 4 GB (1 GB DNS + 768 MB por instancia)."
fi

if (( OK == 1 )); then
    log_ok "Todos los prerrequisitos satisfechos."
else
    die "Faltan dependencias. Instala las marcadas arriba y vuelve a ejecutar."
fi
