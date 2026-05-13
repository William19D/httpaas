# Lecciones aprendidas, problemas encontrados y trabajo futuro

> Este documento corresponde a los entregables (3.b) y (3.c) del enunciado:
> "una sección con los problemas encontrados, propuestas de mejoras y oportunidades de trabajo futuro" y "una sección con los conocimientos relevantes aprendidos en este trabajo".

## 1. Conocimientos aprendidos

### 1.1 Orquestación de infraestructura virtualizada

El proyecto requirió tratar a VirtualBox como una **API**, no como una herramienta interactiva. Toda operación que en la GUI son varios clicks, en la línea de comandos se convierte en una llamada determinista a `VBoxManage`. Aprendimos que:

- Los **nombres** importan: cada VM, controlador, puerto y disco se referencia por nombre, y conviene escogerlos con un esquema predecible (`httpaas-<hostname>-<id-corto>`) para poder enumerar y limpiar.
- El **estado** de una VM (`running`/`poweroff`/`aborted`) se consulta con `showvminfo --machinereadable`, que devuelve `clave=valor` en lugar de texto legible para humanos. Esto sí es parseable.
- VirtualBox tiene un **canal de comunicación** entre host y guest a través de **guest properties** (`VBoxManage guestproperty set` desde el host, `VBoxControl guestproperty get` desde el guest). Es el mecanismo que usamos para inyectar configuración sin necesidad de cloud-init.

### 1.2 Disco multiconexión (multiattach)

Quizá el concepto más interesante del proyecto. Un disco en modo `multiattach`:

- Se adjunta a varias VMs simultáneamente.
- VirtualBox crea un **delta privado** por VM al primer write.
- El base permanece inmutable mientras tenga deltas activos.

Esto es esencialmente **copy-on-write a nivel de disco**, y es la pieza que hace viable provisionar instancias en segundos: en lugar de copiar 8 GB del disco base, sólo se materializa un archivo delta vacío. Es análogo a:

- Los layers de Docker (cada contenedor escribe en un thin layer encima de la imagen).
- Los overlays de OverlayFS.
- Los snapshots COW de ZFS o btrfs.

La limitación clave: **no se puede modificar el base mientras tenga deltas vivos**. En producción esto se suele resolver con un proceso de "actualización" donde se rota a un nuevo base y las VMs antiguas se reciclan.

### 1.3 DNS dinámico autenticado (TSIG)

Bind9 acepta peticiones `nsupdate` y modifica zonas en vivo. La autenticación se hace con **claves simétricas TSIG** (HMAC) que se distribuyen out-of-band entre cliente y servidor. La política `update-policy { grant <keyname> zonesub ANY; }` delimita qué puede hacer cada clave.

Conceptos que se materializaron:

- **Journals**: Bind9 escribe los cambios dinámicos en `.jnl` y los reconcilia con el archivo de zona en `rndc freeze/thaw`.
- **Forwarding**: con `forwarders { 1.1.1.1; 8.8.8.8; }` el servidor delega lo que no es autoritativo. Útil para que las VMs tengan resolución de Internet sin tener que correr un resolver recursivo público.
- **TTL bajos** durante desarrollo (300s) hacen que los cambios se propaguen rápido, pero en producción hay que escoger algo razonable.

### 1.4 Inicialización contextual de VMs

El first-boot script es el patrón "**cloud-init en pobre**":

1. La VM nace sin identidad (hostname genérico, IP por DHCP, etc.).
2. Al arrancar, lee su configuración de un **canal lateral** (en cloud-init es metadata-server; aquí son guest properties).
3. Se reconfigura y reinicia los servicios afectados.

Lo importante es que el script sea **idempotente**: si la misma VM arranca dos veces sin cambios, no debe romper nada. Usamos un flag en `/var/lib/httpaas/applied` con un hash de las inputs.

Otro punto sutil: hay que **limpiar la identidad** del template antes de convertirlo en multiattach, o todas las hijas tendrán el mismo `machine-id` y las mismas llaves SSH:

```bash
sudo rm -f /etc/ssh/ssh_host_*
sudo truncate -s 0 /etc/machine-id
```

### 1.5 Aprovisionamiento asíncrono y polling

La provisión completa tarda 30-60 segundos. El usuario no se debe quedar mirando un HTTP request bloqueado. El patrón implementado:

1. El handler crea la `Instance` en estado `pending`, la persiste, y devuelve `303 → /` inmediatamente.
2. Lanza una `goroutine` que ejecuta el flujo largo y actualiza el estado en cada paso.
3. El navegador hace polling cada 3 segundos a `/api/instances`.

Esto es el embrión de lo que en producción serían **colas de trabajos** (Celery, Sidekiq, RabbitMQ + workers, etc.). Para una sola máquina con un solo orchestrator, una goroutine basta.

### 1.6 Go sin frameworks

La webapp tiene cero dependencias externas. Solo `net/http`, `html/template`, `os/exec`, `encoding/json` y `crypto/rand` (para el UUID). Lecciones:

- El multiplexer de `net/http` desde Go 1.22 soporta verbos (`POST /provision`) y parámetros (`{id}`). Ya no hace falta `gorilla/mux`.
- Las plantillas con `define`/`template` permiten un layout reutilizable. La trampa: si dos archivos parseados juntos definen `title`, el último gana. Solución: que cada vista defina su propio nombre (`define "dashboard"`) y `layout.html` haga `{{template "body" .}}` con bloque opcional.
- Para SSH, invocar el binario del sistema (`exec.Command("ssh", ...)`) es **más simple y robusto** que pelearse con `golang.org/x/crypto/ssh`, y elimina una dependencia.

### 1.7 Bash defensivo

Los scripts del host orquestan procesos largos, lentos y con muchos modos de fallo. Patrones útiles:

- `set -euo pipefail` siempre.
- Funciones `log_step`, `log_info`, `log_ok`, `log_warn`, `die` para que la salida sea legible.
- Idempotencia: cada script comprueba si su efecto ya está aplicado antes de hacer trabajo.
- Variables de entorno (`SKIP_<FASE>=1`) para permitir reanudar.
- `wait_for_port` con `/dev/tcp` evita depender de `nc`.

## 2. Problemas encontrados

### 2.1 VirtualBox no acepta parámetros de kernel en CLI

VirtualBox **no permite** pasar argumentos al kernel del ISO desde la CLI (al contrario de QEMU con `-append`). Para automatizar la instalación de Debian con preseed haría falta o:

- Reconstruir el ISO con los parámetros embebidos en el grub.cfg (engorroso).
- Usar una segunda imagen "metadata" estilo cloud-init (sólo funciona con imágenes cloud, no con netinst).
- Pulsar TAB y escribir manualmente la URL del preseed.

Optamos por la opción 3 (la única interacción manual del proyecto). En producción usaríamos imágenes pre-construidas (`debian-13-genericcloud-amd64.qcow2` convertida a `.vdi`).

### 2.2 `host-only` y NAT compitiendo por la ruta por defecto

Inicialmente las VMs tenían ambas NICs (`hostonly` + `nat`) con `DHCP=yes` y resultaba aleatorio quién ganaba la ruta por defecto. Si ganaba el NAT, las VMs no podían hablar con el DNS (porque el resolver salía por NAT a Internet).

Solución: en el `.network` del NAT pusimos `RouteMetric=1024` y desactivamos `UseDNS=yes`. Así el gateway efectivo es el del host-only (`192.168.56.1`) y el resolver es Bind9.

### 2.3 systemd-resolved interceptando consultas

Debian 13 puede traer `systemd-resolved` activo y manda todas las queries a su stub local (`127.0.0.53`). Aunque pongamos `nameserver 192.168.56.10` en `/etc/resolv.conf`, resolved lo sobreescribe.

Solución: `systemctl disable --now systemd-resolved` + `chattr +i /etc/resolv.conf` (inmutable).

### 2.4 Multiattach y limpieza de identidad

Si no se borran las llaves SSH del template, **todas las instancias tienen la misma fingerprint**. SSH se queja, las herramientas que reusan conexiones se confunden, y desde el punto de vista de seguridad es inaceptable.

Solución: el script de personalización ejecuta:

```bash
sudo rm -f /etc/ssh/ssh_host_*
sudo dpkg-reconfigure openssh-server
```

antes de apagar la VM por última vez. Cada clon regenera sus llaves al primer arranque.

### 2.5 Tiempo de espera de SSH variable

En máquinas más lentas, el first-boot puede tardar 60-90 segundos. El timeout inicial de 60 s era insuficiente y la webapp marcaba la instancia como fallida cuando en realidad estaba arrancando bien. Subimos `WaitForSSH` a 4 minutos.

### 2.6 La carpeta única dentro del zip

Los usuarios suben zips creados con "Comprimir carpeta..." y el zip resultante envuelve los archivos en una carpeta. Si Apache busca `/var/www/html/index.html` pero el zip dejó todo en `/var/www/html/misitio/index.html`, sale 404.

Solución: lógica en el orquestador que detecta "una sola entrada raíz y es directorio" y la aplana automáticamente.

### 2.7 Compilación sin acceso a `proxy.golang.org`

En entornos con red restringida, `go build` falla al descargar dependencias. La solución fue **eliminar todas las dependencias externas** y reescribir lo necesario:

- UUID v4 propio con `crypto/rand` (~30 líneas en `internal/util/uuid.go`).
- SSH usando el binario del sistema en lugar de `golang.org/x/crypto/ssh`.

El binario resultante es más pequeño (~10 MB), más rápido de compilar, y portable a cualquier máquina con Go 1.22.

## 3. Trabajo futuro

### 3.1 Mejoras incrementales

- **WebSockets/Server-Sent Events** en lugar de polling para actualizar el dashboard en tiempo real.
- **Cuotas y TTL**: cada instancia expira tras N horas y se elimina automáticamente. Útil para uso compartido.
- **Métricas**: Prometheus + Grafana, con exporters de Apache y de la propia webapp.
- **Logs centralizados**: las instancias podrían empujar `/var/log/apache2/access.log` por syslog a una VM "log-collector".
- **HTTPS por instancia** con certificados auto-firmados o un CA local.
- **Persistencia más sólida**: cambiar JSON por SQLite (sin servicio adicional, una sola línea con `database/sql`).

### 3.2 Cambios estructurales

- **Sustituir multiattach por linked clones con snapshots**: más aislamiento (cada instancia puede recibir parches del base independientemente) y permite actualizar el base sin romper las hijas vivas.
- **Containers en vez de VMs**: en una iteración futura, sería trivial reemplazar las VMs Apache por contenedores Docker o Podman, manteniendo la misma webapp Go como plano de control. Ganaría ~30x en velocidad de aprovisionamiento.
- **API REST pura**: separar el frontend (que podría ser un SPA en Vue/React) del backend (sólo JSON). Permitiría que clientes externos automaticen aprovisionamiento.
- **Multi-host**: usar `vagrant-libvirt` o talkar a `libvirt` por gRPC para distribuir las VMs entre varios hipervisores.

### 3.3 Producción real

Si esto fuera un servicio en serio, la lista incluiría:

- **Aislamiento de tenants**: hoy todas las VMs comparten la misma red. En producción, una VLAN por tenant.
- **Autenticación**: la webapp es completamente abierta. OIDC con Keycloak sería el siguiente paso.
- **Backups del estado**: si se rompe `webapp/data/instances.json`, perdemos todo el registro.
- **Failover del DNS**: hoy hay un solo Bind9. Un secundario sería mandatorio.
- **Gestión del ciclo de vida del template**: hoy actualizar la imagen base es manual y requiere apagar todas las hijas. Un proceso "rolling update" automatizado.

## 4. Reflexión final

Lo más valioso del proyecto no es haber escrito una webapp Go ni configurado Bind9, sino haber tenido que **integrar piezas heterogéneas** (un hipervisor, un servidor DNS, un servidor HTTP, un orquestador propio) y hacer que cooperen sin intervención manual. Esa es la esencia del diseño de plataformas en la nube: cada componente expone una API (CLI, RPC, configuración declarativa) y el plano de control las articula.

El resultado es un sistema modesto pero **realmente funcional**: si el usuario sube un zip, en menos de un minuto tiene una página servida en un dominio resoluble por DNS, con su propio servidor Apache, en su propia VM, con su propia configuración. Es una micro-replica funcional de lo que hacen AWS Elastic Beanstalk, Heroku o Vercel a escala planetaria.
