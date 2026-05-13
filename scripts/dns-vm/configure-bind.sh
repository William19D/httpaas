#!/bin/sh
# Ejecutado dentro de la VM httpaas-dns (Alpine) por SSH.
# Instala Bind9 y carga la zona cloud.local. con clave TSIG.
set -e

# --- Paquetes ---------------------------------------------------------
apk add --quiet bind bind-tools

# --- Zonas y configuración -------------------------------------------
mkdir -p /etc/bind/zones
chgrp named /etc/bind/zones
chmod 775 /etc/bind/zones

cat >/etc/bind/named.conf <<'NAMED'
acl trusted_clients {
    127.0.0.0/8;
    192.168.56.0/24;
};

options {
    directory "/var/bind";
    pid-file "/var/run/named/named.pid";
    listen-on    { 127.0.0.1; 192.168.56.10; };
    listen-on-v6 { none; };
    allow-query        { trusted_clients; };
    allow-recursion    { trusted_clients; };
    allow-query-cache  { trusted_clients; };
    forwarders { 1.1.1.1; 8.8.8.8; };
    recursion yes;
    dnssec-validation no;
    auth-nxdomain no;
    minimal-responses yes;
};

include "/etc/bind/named.conf.keys";

zone "cloud.local." {
    type master;
    file "/etc/bind/zones/db.cloud.local";
    update-policy { grant httpaas-key zonesub ANY; };
    notify no;
};

zone "56.168.192.in-addr.arpa." {
    type master;
    file "/etc/bind/zones/db.192.168.56";
    update-policy { grant httpaas-key zonesub ANY; };
    notify no;
};
NAMED

cat >/etc/bind/zones/db.cloud.local <<'ZONE'
$TTL    300
@       IN      SOA     dns.cloud.local. admin.cloud.local. (
                            2026051101  ; Serial
                            3600        ; Refresh
                            900         ; Retry
                            604800      ; Expire
                            300         ; Negative TTL
                        )
@               IN      NS      dns.cloud.local.
dns             IN      A       192.168.56.10
api             IN      A       192.168.56.1
ZONE

cat >/etc/bind/zones/db.192.168.56 <<'ZONE'
$TTL    300
@       IN      SOA     dns.cloud.local. admin.cloud.local. (
                            2026051101
                            3600
                            900
                            604800
                            300
                        )
@               IN      NS      dns.cloud.local.
10              IN      PTR     dns.cloud.local.
1               IN      PTR     api.cloud.local.
ZONE

# --- TSIG -------------------------------------------------------------
# Generamos una clave hmac-sha256 llamada "httpaas-key" si no existe.
if [ ! -f /etc/bind/named.conf.keys ]; then
    tsig-keygen -a hmac-sha256 httpaas-key > /etc/bind/named.conf.keys
fi
chmod 0640 /etc/bind/named.conf.keys
chgrp named /etc/bind/named.conf.keys

chown -R named:named /etc/bind/zones
chmod -R g+w /etc/bind/zones

# --- Verificación de sintaxis ----------------------------------------
named-checkconf
named-checkzone cloud.local.                  /etc/bind/zones/db.cloud.local
named-checkzone 56.168.192.in-addr.arpa.      /etc/bind/zones/db.192.168.56

# --- Activar servicio ------------------------------------------------
rc-update add named default
rc-service named restart
sleep 2
rc-service named status
echo
echo "TSIG KEY ↓↓↓ (copia esto al host en webapp/data/dnskey.conf)"
echo "================================================================"
cat /etc/bind/named.conf.keys
echo "================================================================"
