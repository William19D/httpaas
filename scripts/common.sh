# =============================================================
# HTTPaaS - scripts/common.sh
# =============================================================
# Funciones compartidas por todos los scripts del proyecto.
# Se obtiene la ruta del repo, se definen helpers de log y se
# expone require_cmd para validar dependencias.
# =============================================================
# Este archivo se "source"-ea, no se ejecuta. No debe tener shebang
# ni `set -e` que pueda matar al script padre.

# --- Localización del repo (independiente de dónde se invoque) ---
# shellcheck disable=SC2155
export REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export SCRIPTS_DIR="$REPO_ROOT/scripts"
export CONFIGS_DIR="$REPO_ROOT/configs"
export WEBAPP_DIR="$REPO_ROOT/webapp"

# --- Colores ----------------------------------------------------
if [[ -t 1 ]]; then
    C_RESET=$'\033[0m'
    C_BOLD=$'\033[1m'
    C_RED=$'\033[31m'
    C_GREEN=$'\033[32m'
    C_YELLOW=$'\033[33m'
    C_BLUE=$'\033[34m'
    C_CYAN=$'\033[36m'
else
    C_RESET= C_BOLD= C_RED= C_GREEN= C_YELLOW= C_BLUE= C_CYAN=
fi

log_step()  { echo; echo "${C_BOLD}${C_BLUE}==>${C_RESET} ${C_BOLD}$*${C_RESET}"; }
log_info()  { echo "    $*"; }
log_ok()    { echo "${C_GREEN} ✓${C_RESET} $*"; }
log_warn()  { echo "${C_YELLOW} ! $*${C_RESET}" >&2; }
log_error() { echo "${C_RED} ✗ $*${C_RESET}" >&2; }
die()       { log_error "$*"; exit 1; }

require_cmd() {
    local c="$1"
    command -v "$c" >/dev/null 2>&1 || die "$c no encontrado en PATH (instálalo primero)"
}

# --- Valores por defecto del proyecto ---------------------------
# Pueden sobrescribirse exportando antes de invocar el script.
: "${HTTPAAS_NETWORK:=vboxnet0}"
: "${HTTPAAS_HOST_IP:=192.168.56.1}"
: "${HTTPAAS_NETMASK:=255.255.255.0}"
: "${HTTPAAS_SUBNET:=192.168.56.0/24}"

: "${HTTPAAS_DNS_VM:=httpaas-dns}"
: "${HTTPAAS_DNS_IP:=192.168.56.10}"
: "${HTTPAAS_DOMAIN:=cloud.local}"
: "${HTTPAAS_TSIG_NAME:=httpaas-key}"

: "${HTTPAAS_TEMPLATE_VM:=httpaas-template}"

: "${HTTPAAS_SSH_USER:=cloud}"
: "${HTTPAAS_SSH_KEY:=$HOME/.ssh/id_rsa_httpaas}"

export HTTPAAS_NETWORK HTTPAAS_HOST_IP HTTPAAS_NETMASK HTTPAAS_SUBNET
export HTTPAAS_DNS_VM HTTPAAS_DNS_IP HTTPAAS_DOMAIN HTTPAAS_TSIG_NAME
export HTTPAAS_TEMPLATE_VM HTTPAAS_SSH_USER HTTPAAS_SSH_KEY

# Función para esperar a que un puerto responda.
wait_for_port() {
    local host="$1" port="$2" timeout="${3:-120}"
    local start=$SECONDS
    while ! (echo >"/dev/tcp/$host/$port") 2>/dev/null; do
        if (( SECONDS - start > timeout )); then return 1; fi
        sleep 2
    done
    return 0
}

# Genera o reutiliza el par de llaves SSH dedicado al proyecto.
ensure_ssh_key() {
    if [[ -f "$HTTPAAS_SSH_KEY" ]]; then
        log_info "Llave SSH existente en $HTTPAAS_SSH_KEY"
        return
    fi
    log_info "Generando par de llaves SSH dedicado: $HTTPAAS_SSH_KEY"
    mkdir -p "$(dirname "$HTTPAAS_SSH_KEY")"
    ssh-keygen -t ed25519 -N "" -C "httpaas@$(hostname)" -f "$HTTPAAS_SSH_KEY"
    chmod 600 "$HTTPAAS_SSH_KEY"
}
