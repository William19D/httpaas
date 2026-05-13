# Troubleshooting

Problemas frecuentes y cómo resolverlos. Organizados por componente.

## Webapp Go

### `VBoxManage no encontrado en PATH`

La webapp no ve VirtualBox. Suele pasar si la corres como servicio bajo un usuario distinto al que instaló VirtualBox.

```bash
which VBoxManage
# /usr/bin/VBoxManage
```

Si está en otro lugar (`/usr/local/bin`, snap, etc.), añade su directorio al `Environment=PATH=` del archivo `httpaas.service`.

### `red host-only vboxnet0 no existe`

```bash
./scripts/setup-host-network.sh
```

### `servidor DNS no responde`

La VM `httpaas-dns` no está corriendo o Bind no está activo.

```bash
VBoxManage list runningvms | grep httpaas-dns
VBoxManage startvm httpaas-dns --type headless
ssh -i ~/.ssh/id_rsa_httpaas cloud@192.168.56.10 'sudo systemctl status named'
```

### `error: open data/dnskey.conf: no such file`

Falta la llave TSIG. Re-ejecuta:

```bash
./scripts/setup-dns-server.sh
```

(detectará la VM existente y sólo regenerará/copiará la clave).

---

## Aprovisionamiento

### La instancia queda en `provisioning` para siempre

Suele ser uno de tres problemas:

1. **First-boot no se ejecutó.** Conéctate por consola serial:

   ```bash
   VBoxManage controlvm <vm> keyboardputscancode 1c   # ENTER
   VBoxManage startvm <vm> --type separate            # abre ventana
   ```

   Login: `cloud` / `cloud`. Mira `/var/log/httpaas-firstboot.log` y `journalctl -u httpaas-firstboot`.

2. **Las guest properties no llegaron.** Verifica:

   ```bash
   VBoxManage guestproperty enumerate <vm>
   # Debe listar /HTTPaaS/hostname, /HTTPaaS/ip, etc.
   ```

   Si no aparecen, mira el log de la webapp para ver si `setGuestProperty` falló.

3. **La VM no tiene `virtualbox-guest-utils`.** El first-boot necesita `VBoxControl`. Si reusaste una VM manualmente:

   ```bash
   sudo apt install virtualbox-guest-utils
   ```

### SSH timeout (`No hay SSH en 192.168.56.X`)

```bash
# Verifica que la VM tiene la IP esperada
ssh-keyscan 192.168.56.X
ping -c 3 192.168.56.X

# Si pinga pero SSH no responde:
VBoxManage controlvm <vm> acpipowerbutton   # apaga limpio
VBoxManage startvm <vm> --type separate     # abre GUI para inspección
```

Causa común: el first-boot puso la IP pero `ssh.service` no se inició. En la plantilla la activamos con `systemctl enable ssh`.

### `Hostname ya está en uso`

Otra instancia tiene el mismo hostname (o no se eliminó bien una anterior).

```bash
# En la webapp
curl http://localhost:8080/api/instances | python3 -m json.tool

# Borra del store (cuidado, no limpia la VM)
rm webapp/data/instances.json
```

### El zip se sube pero el sitio sale 404

El `unzip` se hace contra `/var/www/html`. Si tu zip envuelve los archivos en una carpeta raíz (por ejemplo `misitio/index.html`), el orquestador la "aplana" automáticamente. Pero si tu zip tiene **varias carpetas** en el primer nivel, no sabe cuál elegir.

Estructura recomendada del zip:

```
✅ sitio.zip
   ├── index.html
   ├── assets/
   └── style.css

✅ sitio.zip
   └── misitio/
       ├── index.html
       └── ...

❌ sitio.zip
   ├── frontend/
   ├── backend/
   └── docs/
```

---

## DNS

### `dig` devuelve `SERVFAIL` desde el host

```bash
# 1. ¿Bind responde?
nc -vz 192.168.56.10 53

# 2. ¿La zona está cargada?
ssh -i ~/.ssh/id_rsa_httpaas cloud@192.168.56.10 \
    'sudo rndc status; sudo rndc zonestatus cloud.local.'

# 3. ¿Hay errores de sintaxis?
ssh -i ~/.ssh/id_rsa_httpaas cloud@192.168.56.10 \
    'sudo named-checkconf; sudo journalctl -u named --no-pager | tail -50'
```

### `nsupdate failed: REFUSED`

La clave TSIG no coincide entre el host (`webapp/data/dnskey.conf`) y la VM (`/etc/bind/named.conf.keys`).

```bash
diff <(cat webapp/data/dnskey.conf) \
     <(ssh -i ~/.ssh/id_rsa_httpaas cloud@192.168.56.10 'sudo cat /etc/bind/named.conf.keys')
# Si difieren:
./scripts/setup-dns-server.sh
```

### El navegador no resuelve `*.cloud.local`

Tu DNS de sistema apunta a otro lado. Soluciones:

```bash
# Opción 1: añadir el DNS sólo para vboxnet0 (recomendado)
sudo resolvectl dns vboxnet0 192.168.56.10
sudo resolvectl domain vboxnet0 cloud.local

# Opción 2: /etc/hosts por instancia
echo "192.168.56.100  miapp.cloud.local" | sudo tee -a /etc/hosts

# Opción 3: dnsmasq local que reenvíe *.cloud.local a Bind9
# (más elegante pero requiere instalación adicional)
```

---

## VirtualBox

### `VBoxManage: error: Could not create the host-only network interface`

VirtualBox kernel modules no están cargados.

```bash
sudo /sbin/vboxconfig            # recompila los módulos
lsmod | grep vbox                # debes ver vboxdrv, vboxnetadp, vboxnetflt
```

Si secure boot está activo: o lo desactivas en la BIOS, o firmas los módulos.

### `Could not start the machine ... VERR_VMX_NO_VMX`

Virtualización por hardware no habilitada. Reinicia, entra a la BIOS y activa **Intel VT-x** / **AMD-V**.

### Disco multiattach se "rompe" tras editar el disco base

Multiattach **NO permite editar el disco base** mientras hay deltas activos. Si lo modificaste a mano:

```bash
# Apaga TODAS las instancias
VBoxManage list runningvms | awk -F\" '/httpaas/{print $2}' | \
    xargs -I{} VBoxManage controlvm {} poweroff

# Borra todas las VMs hijas (deltas)
VBoxManage list vms | awk -F\" '/httpaas-(?!template|dns)/{print $2}' | \
    xargs -I{} VBoxManage unregistervm {} --delete

# Ahora puedes tocar el base. Si se quedó "in use", quítale el flag:
VBoxManage closemedium disk <path-al-vdi>
```

---

## Apache

### El sitio sale, pero los CSS/JS no cargan

Apache no encuentra los archivos. Comprueba:

```bash
ssh -i ~/.ssh/id_rsa_httpaas cloud@192.168.56.100 'ls /var/www/html'
```

Si están en una subcarpeta (`/var/www/html/misitio/...`), el orquestador debería haberlos aplanado. Si no lo hizo, fue porque el zip tenía múltiples carpetas raíz. Aplánalo a mano:

```bash
ssh -i ~/.ssh/id_rsa_httpaas cloud@192.168.56.100 \
    'cd /var/www/html && sudo mv misitio/* . && sudo rmdir misitio'
```

### Apache da 403 Forbidden

Permisos de archivos. Lo arregla:

```bash
ssh -i ~/.ssh/id_rsa_httpaas cloud@192.168.56.100 \
    'sudo chown -R www-data:www-data /var/www/html'
```

---

## Reset rápido

Si todo es un caos:

```bash
./scripts/teardown.sh                    # borra VMs + red + servicio
rm -rf webapp/data webapp/uploads        # estado de la app
rm -f ~/.ssh/id_rsa_httpaas{,.pub}       # llaves SSH (opcional)
./install.sh                             # vuelve a empezar
```
