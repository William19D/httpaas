#!/bin/bash
# Ejecutado por SSH en la VM httpaas-template después del install Debian 13.
# Hace:
#   1. Configura eth0 con IP estática 192.168.56.20/24 (red host-only).
#   2. Habilita Apache + módulos vhost_alias y rewrite.
#   3. Crea un único VirtualHost catch-all que usa mod_vhost_alias para
#      mapear automáticamente cualquier hostname → /var/www/<hostname>/.
#      → Significa que sólo con scp del zip a /var/www/<hostname>/ el sitio
#        queda accesible como http://<hostname>.cloud.local/ sin reload de Apache.
#   4. Habilita el servicio para que arranque al boot.
set -euo pipefail

# 1) Red host-only fija
if ! grep -q "address 192.168.56.20" /etc/network/interfaces; then
  cat >> /etc/network/interfaces <<'NET'

# Host-only adapter (HTTPaaS internal network)
auto enp0s3
iface enp0s3 inet static
    address 192.168.56.20
    netmask 255.255.255.0
NET
  # Para que el cambio aplique sin reboot (la NIC1 hostonly en VBox suele ser enp0s3)
  ip addr add 192.168.56.20/24 dev enp0s3 2>/dev/null || true
  ip link set enp0s3 up 2>/dev/null || true
fi

# 2) Apache + módulos
DEBIAN_FRONTEND=noninteractive apt-get -y install apache2 >/dev/null 2>&1 || true
a2enmod vhost_alias rewrite >/dev/null
# Deshabilitar el default site
a2dissite 000-default >/dev/null 2>&1 || true

# 3) VirtualHost catch-all con mod_vhost_alias
cat > /etc/apache2/sites-available/httpaas.conf <<'APACHE'
# HTTPaaS - VirtualHost dinámico
# Cualquier hostname.cloud.local se mapea automáticamente a /var/www/<hostname>/
<VirtualHost *:80>
    ServerName _httpaas
    ServerAlias *.cloud.local
    UseCanonicalName Off

    # %1 = primer label del Host header (e.g. "blog" para "blog.cloud.local")
    VirtualDocumentRoot /var/www/%1/

    <Directory "/var/www/">
        Options +FollowSymLinks
        AllowOverride None
        Require all granted
    </Directory>

    ErrorLog  ${APACHE_LOG_DIR}/httpaas-error.log
    CustomLog ${APACHE_LOG_DIR}/httpaas-access.log combined
</VirtualHost>

# Default para visitas sin hostname (e.g. directamente por IP)
<VirtualHost *:80>
    ServerName 192.168.56.20
    DocumentRoot /var/www/_default

    <Directory "/var/www/_default">
        Require all granted
    </Directory>
</VirtualHost>
APACHE

# Página default
mkdir -p /var/www/_default
cat > /var/www/_default/index.html <<'INDEX'
<!doctype html>
<html><head><title>HTTPaaS template</title></head>
<body style="font-family:system-ui;padding:40px;background:#0f1216;color:#fff">
<h1>HTTPaaS template — Apache OK</h1>
<p>Esta VM Debian 13 hospeda los sitios provisionados.</p>
<p>Los sitios se sirven en <code>http://&lt;hostname&gt;.cloud.local/</code>.</p>
</body></html>
INDEX

a2ensite httpaas >/dev/null
mkdir -p /var/www
chown -R www-data:www-data /var/www
chmod 755 /var/www

systemctl enable apache2 >/dev/null
systemctl restart apache2

# 4) Verificación
echo "=== apache2 status ==="
systemctl is-active apache2
echo "=== ip addr ==="
ip -4 addr show
echo "=== listen ==="
ss -lntp 2>/dev/null | grep ':80\|:22' || netstat -lntp 2>/dev/null | grep ':80\|:22'
echo "DONE"
