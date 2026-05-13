#!/bin/bash
# =============================================================
#                      HTTPaaS - install.sh
# =============================================================
#  HTTP as a Service - despliegue automatizado
#  Computación en la Nube · Universidad del Quindío · 2026-1
#
#  Orquesta todo el setup. Tras correr:
#
#     ./install.sh
#
#  tendrás:
#     - red host-only vboxnet0 (192.168.56.0/24)
#     - VM "httpaas-template" con disco multiconexión
#     - VM "httpaas-dns"      sirviendo cloud.local
#     - webapp Go compilada y corriendo en :8080
#
#  Después abres http://localhost:8080 y aprovisionas instancias.
#
#  Fases (se pueden saltar con SKIP_<FASE>=1):
#     PREREQS      check-prereqs.sh
#     NETWORK      setup-host-network.sh
#     TEMPLATE     setup-template-vm.sh
#     DNS          setup-dns-server.sh
#     WEBAPP       setup-webapp.sh
#     RUN          arranca la webapp
#
#  Ejemplo - reinstalar sólo el DNS:
#     SKIP_PREREQS=1 SKIP_NETWORK=1 SKIP_TEMPLATE=1 SKIP_WEBAPP=1 ./install.sh
# =============================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$REPO_ROOT"
source "$REPO_ROOT/scripts/common.sh"

cat <<'BANNER'

  ╔═══════════════════════════════════════════════════════════╗
  ║                                                           ║
  ║              H T T P a a S    ·   install                 ║
  ║                                                           ║
  ║   HTTP as a Service                                       ║
  ║   Computación en la Nube · UQ · 2026-1                    ║
  ║                                                           ║
  ╚═══════════════════════════════════════════════════════════╝

BANNER

START_TS=$(date +%s)

run_phase() {
    local skip_var="$1"
    local label="$2"
    local script="$3"
    if [[ "${!skip_var:-0}" == "1" ]]; then
        log_warn "Saltando fase $label (${skip_var}=1)"
        return 0
    fi
    log_step "▶  $label"
    bash "$REPO_ROOT/scripts/$script"
    log_ok "Fase $label completa"
}

run_phase SKIP_PREREQS  "Prerrequisitos"        check-prereqs.sh
run_phase SKIP_NETWORK  "Red host-only"         setup-host-network.sh
run_phase SKIP_TEMPLATE "VM plantilla"          setup-template-vm.sh
run_phase SKIP_DNS      "Servidor DNS (Bind9)"  setup-dns-server.sh
run_phase SKIP_WEBAPP   "Webapp Go"             setup-webapp.sh

# -----------------------------------------------------------------
# Resumen final
# -----------------------------------------------------------------
ELAPSED=$(( $(date +%s) - START_TS ))
MIN=$(( ELAPSED / 60 ))
SEC=$(( ELAPSED % 60 ))

cat <<SUMMARY

  ${C_GREEN}${C_BOLD}╔═══════════════════════════════════════════════════════════╗${C_RESET}
  ${C_GREEN}${C_BOLD}║                    Instalación completa                  ║${C_RESET}
  ${C_GREEN}${C_BOLD}╚═══════════════════════════════════════════════════════════╝${C_RESET}

  ${C_BOLD}Tiempo total:${C_RESET}        ${MIN}m ${SEC}s

  ${C_BOLD}Red:${C_RESET}                 $HTTPAAS_NETWORK ($HTTPAAS_SUBNET)
  ${C_BOLD}Host:${C_RESET}                $HTTPAAS_HOST_IP
  ${C_BOLD}DNS:${C_RESET}                 $HTTPAAS_DNS_IP  (zona $HTTPAAS_DOMAIN)
  ${C_BOLD}VM plantilla:${C_RESET}        $HTTPAAS_TEMPLATE_VM
  ${C_BOLD}Llave SSH:${C_RESET}           $HTTPAAS_SSH_KEY
  ${C_BOLD}Llave TSIG:${C_RESET}          $REPO_ROOT/webapp/data/dnskey.conf

  ${C_BOLD}Webapp:${C_RESET}
       binario:        $REPO_ROOT/webapp/httpaas
       configuración:  $REPO_ROOT/webapp/config.json

SUMMARY

if [[ "${SKIP_RUN:-0}" == "1" ]]; then
    log_info "SKIP_RUN=1. No arranco la webapp. Para iniciarla manualmente:"
    log_info "   cd $REPO_ROOT/webapp && ./httpaas"
    exit 0
fi

log_step "Arrancando la webapp en primer plano (CTRL+C para parar)"
log_info "Abre tu navegador en:  http://localhost:8080"
echo
exec "$REPO_ROOT/webapp/httpaas"
