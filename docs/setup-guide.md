# Guía de instalación paso a paso

> Tiempo estimado: **30–45 minutos** (la mayoría es la instalación de Debian).
> Si ya tienes una VM Debian lista, baja a **15 minutos**.

## 0. Preparación del host

Sólo es necesaria la primera vez.

### 0.1 Instalar prerequisitos del sistema

```bash
sudo apt update
sudo apt install -y \
    virtualbox virtualbox-ext-pack \
    golang-go \
    openssh-client bind9-dnsutils sshpass \
    curl unzip python3 python3-pip
```

> Si `apt` no tiene la versión de Go que necesitas, usa `snap install go --classic` o descarga desde golang.org. Mínimo: **1.22**.

### 0.2 Permisos para VirtualBox

```bash
sudo usermod -aG vboxusers $USER
newgrp vboxusers       # actualiza el grupo en la shell actual
groups | grep vboxusers   # debe aparecer
```

### 0.3 Verificación

```bash
cd httpaas-project
./scripts/check-prereqs.sh
```

Salida esperada: todos los checks en verde.

---

## 1. Instalación

### 1.1 La forma fácil

```bash
./install.sh
```

Y déjalo correr. Pasará por las cinco fases que se describen abajo.

### 1.2 Las fases por dentro

#### Fase A — Red host-only (`scripts/setup-host-network.sh`)

Tarda **<5 s**. Crea `vboxnet0` con IP `192.168.56.1` y deshabilita el DHCP server de VirtualBox para esa red (usamos IPs estáticas).

Verificación manual:

```bash
VBoxManage list hostonlyifs | grep -A4 vboxnet0
ip -4 addr show vboxnet0
```

#### Fase B — VM plantilla (`scripts/setup-template-vm.sh`)

**Aquí está la única interacción manual** del proceso. El script:

1. Descarga `debian-13.0.0-amd64-netinst.iso` a `~/.cache/httpaas/` (~700 MB). Si ya existe, lo reusa.
2. Crea la VM `httpaas-template` con 1 GB RAM y un disco vacío de 8 GB.
3. Lanza un servidor HTTP local en `:8000` sirviendo `preseed.cfg`.
4. Arranca la VM en modo **GUI** y te pide que:
   - En el menú de GRUB de Debian, pulses `TAB` con la opción `Install` seleccionada.
   - Añadas al final de la línea: `auto=true url=http://10.0.2.2:8000/preseed.cfg`
   - Pulses `ENTER`.
5. A partir de ahí Debian se instala solo (~10–12 minutos) y se apaga al terminar.
6. Una vez apagada, el script:
   - Genera (si no existe) la llave SSH dedicada `~/.ssh/id_rsa_httpaas`.
   - Vuelve a arrancar la VM con port-forward SSH (`localhost:2222 → 22`).
   - Sube `first-boot.sh`, la unidad systemd `httpaas-firstboot.service`, y el `000-default.conf` de Apache.
   - Instala todo dentro de la VM, habilita los servicios, borra las llaves SSH del host (para que se regeneren al primer boot de cada clon), y la apaga.
   - **Convierte el disco a `multiattach`** con `VBoxManage modifymedium disk ... --type multiattach`.

> ⚠️ **Limitación conocida:** VirtualBox no permite inyectar parámetros al kernel del ISO sin GUI. Por eso el paso 4 requiere abrir la ventana. Es el único click manual de todo el proyecto.

Verificación manual:

```bash
VBoxManage list vms | grep httpaas-template
VBoxManage showmediuminfo "$HOME/VirtualBox VMs/httpaas-template/httpaas-template.vdi" | grep "Type:"
# Type: multiattach
```

#### Fase C — Servidor DNS (`scripts/setup-dns-server.sh`)

Tarda **2–3 minutos**. Pasos:

1. Toma snapshot de la plantilla y clona como `httpaas-dns` (link clone).
2. Configura la red para que la VM tenga IP fija `192.168.56.10`.
3. Inyecta guest properties con la IP fija → el first-boot la aplica.
4. Arranca, espera SSH.
5. `apt-get install bind9` dentro de la VM.
6. Sube los archivos de `configs/bind9/` a `/etc/bind/` con permisos correctos.
7. Genera la **clave TSIG** con `tsig-keygen -a hmac-sha256 httpaas-key`. La copia tanto a `/etc/bind/named.conf.keys` (dentro de la VM) como a `webapp/data/dnskey.conf` (en el host, para que la webapp la use).
8. Valida con `named-checkconf` y `named-checkzone`.
9. Hace una prueba `dig SOA cloud.local @192.168.56.10` y un `nsupdate` de un registro temporal para confirmar que TSIG funciona.

Verificación manual:

```bash
dig +short @192.168.56.10 cloud.local SOA
# debe devolver algo como: dns.cloud.local. admin.cloud.local. 2026051001 ...

# Crear un registro
nsupdate -k webapp/data/dnskey.conf <<EOF
server 192.168.56.10
zone cloud.local.
update add probar.cloud.local. 300 A 192.168.56.99
send
EOF

# Comprobar
dig +short @192.168.56.10 probar.cloud.local
# 192.168.56.99
```

#### Fase D — Webapp Go (`scripts/setup-webapp.sh`)

Tarda **5–10 s**. Sin sorpresas:

1. `go build` produce `webapp/httpaas` (~10 MB, estático).
2. Genera `webapp/config.json` con las rutas reales del entorno.
3. Crea las carpetas `webapp/uploads` y `webapp/data` si no existen.
4. Genera la unidad `httpaas.service` sustituyendo `__USER__`, `__HOME__` y `__WORKDIR__`. La copia a `/etc/systemd/system/` sólo si tiene permisos de root; si no, te dice cómo hacerlo manualmente.

#### Fase E — Arranque

`install.sh` termina lanzando `./webapp/httpaas` en primer plano. Verás:

```
2026/05/11 21:34:01 HTTPaaS - HTTP as a Service
2026/05/11 21:34:01   listening on 0.0.0.0:8080
2026/05/11 21:34:01   dns server   192.168.56.10 (cloud.local.)
2026/05/11 21:34:01   template VM  httpaas-template
2026/05/11 21:34:01   ip range     192.168.56.100 - 192.168.56.200
```

Abre `http://localhost:8080`.

---

## 2. Primera instancia

1. **DNS del navegador**: para que `*.cloud.local` se resuelva, lo más fácil es:

   ```bash
   sudo resolvectl dns vboxnet0 192.168.56.10
   sudo resolvectl domain vboxnet0 cloud.local
   ```

   o alternativamente añadir entradas en `/etc/hosts` cuando aprovisiones cada instancia.

2. En el dashboard, **Aprovisionar nueva instancia**. Usa el `examples/sample-site.zip` que viene en el repo.

3. El estado pasará por `pending → provisioning → deploying → running` en aproximadamente **30–45 s** la primera vez (luego es más rápido porque el ISO ya está cacheado y la VM plantilla queda en disco).

4. Cuando esté `running`, haz clic en el enlace. Deberías ver el sitio del zip.

---

## 3. Diagnóstico durante la instalación

```bash
# Logs de la webapp (si la corres en primer plano, salen ahí)
journalctl -u httpaas -f         # o si la pusiste como servicio

# Estado de las VMs
VBoxManage list runningvms

# Consola serial de una VM (útil cuando first-boot falla)
VBoxManage debugvm <vm-name> dumpvmcore --filename /tmp/dump

# SSH manual a una instancia
ssh -i ~/.ssh/id_rsa_httpaas cloud@192.168.56.100

# Bind9 logs (dentro de la VM DNS)
ssh -i ~/.ssh/id_rsa_httpaas cloud@192.168.56.10 'sudo journalctl -u named -f'

# Probar resolución de un host nuevo
dig +short @192.168.56.10 blog.cloud.local
```

---

## 4. Desinstalación

```bash
./scripts/teardown.sh
```

Te pedirá escribir `borrar` para confirmar. Quitará VMs, red host-only y servicio systemd. **No** borra la ISO de Debian (~700 MB) ni tu llave SSH.

Para realmente empezar de cero:

```bash
rm -rf ~/.cache/httpaas
rm -f ~/.ssh/id_rsa_httpaas{,.pub}
```
