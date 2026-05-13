# Arquitectura

## Visión general

HTTPaaS es una IaaS/PaaS local en miniatura. Hay tres capas:

1. **Infraestructura** — VirtualBox proporciona red host-only y VMs.
2. **Plano de control** — la webapp Go orquesta todo, hablando con VirtualBox por `VBoxManage`, con Bind9 por `nsupdate` y con cada VM por `ssh`/`scp`.
3. **Plano de datos** — las VMs Apache sirven el contenido subido por el usuario, accesibles vía hostnames resueltos por Bind9.

## Componentes

### Plano de control: webapp Go (`webapp/`)

```
cmd/httpaas/main.go         arranque, lectura de config, inyección de dependencias
internal/config             struct Config + Default() + Load() desde JSON
internal/models/instance.go modelo Instance, estados, log entries
internal/services
    store.go                persistencia JSON con mutex; AllocateIP del rango
    virtualbox.go           wrapper de VBoxManage; CreateInstance hace storageattach --mtype multiattach
    dns.go                  llama a nsupdate con -k TSIG_KEY; AddRecord / RemoveRecord / Ping
    ssh.go                  invoca binarios ssh/scp del sistema; WaitForSSH, Run, UploadFile
    orchestrator.go         Provision/Delete/Restart; coordina vbox+dns+ssh; estados
    exec.go                 helper execVBox compartido
internal/handlers           rutas HTTP, plantillas, multipart, JSON
internal/middleware         logger, recover, CORS
internal/util/uuid.go       UUID v4 propio (sin dependencia externa)
templates/                  Go templates con layout + 3 vistas
static/                     CSS terminal/brutalist + JS de polling
```

Decisiones clave:

- **Sin dependencias externas** (`go.mod` no tiene `require`). Toda la lógica de UUID, SSH y DNS usa la stdlib o binarios del sistema. Beneficio: `go build` funciona en cualquier máquina con Go ≥ 1.22 sin acceso a `proxy.golang.org`.
- **Estado en JSON con mutex** (`webapp/data/instances.json`). Es un proyecto académico, no necesita SQLite. Cargar/guardar es O(número de instancias).
- **Estados explícitos**: `pending → provisioning → deploying → running` (o `failed`, `stopped`, `deleting`). El frontend hace polling cada 3 s a `/api/instances` para refrescar el dashboard.
- **IPs deterministas**: `AllocateIP` recorre el rango configurado y devuelve la primera libre. Los rangos se persisten en el store para evitar carreras.

### Plano de datos: VM plantilla

`httpaas-template` es una VM Debian 13 CLI con:

- `apache2` con `mod_headers` activado.
- `openssh-server` autorizando la llave pública del host.
- `virtualbox-guest-utils` (para `VBoxControl`).
- Servicio systemd `httpaas-firstboot.service` que en cada boot ejecuta `/usr/local/sbin/httpaas-firstboot`.

El **first-boot** lee guest properties que la webapp inyecta antes de arrancar:

| Propiedad | Significado |
|---|---|
| `/HTTPaaS/hostname` | `web01`, `blog`, etc. |
| `/HTTPaaS/fqdn` | `web01.cloud.local` |
| `/HTTPaaS/ip` | `192.168.56.123` |
| `/HTTPaaS/netmask` | `255.255.255.0` |
| `/HTTPaaS/gateway` | `192.168.56.1` |
| `/HTTPaaS/dns` | `192.168.56.10` |

Con eso configura `/etc/hostname`, `/etc/hosts`, una `.network` para systemd-networkd con IP estática en `enp0s3`, y reescribe `ServerName` de Apache. El script es idempotente (flag en `/var/lib/httpaas/applied`).

#### Disco multiconexión

El disco `httpaas-template.vdi` se marca como **multiattach** después de la instalación inicial. Cada VM hija (instancia) lo monta con:

```
VBoxManage storageattach <vm> --storagectl SATA --port 0 --device 0 \
    --type hdd --mtype multiattach --medium <ruta.vdi>
```

VirtualBox crea un **delta privado** por VM al adjuntar. Las escrituras de cada instancia no afectan a las demás ni al disco base. Es el equivalente a un copy-on-write a nivel de disco y es lo que hace viable provisionar instancias en segundos sin clonar gigabytes.

### Servidor DNS

`httpaas-dns` es una clonación de la plantilla con Bind9 encima:

- Zona `cloud.local.` con SOA dummy y un solo NS (`dns.cloud.local`).
- Zona inversa `56.168.192.in-addr.arpa.`.
- **Update dinámico autenticado por TSIG** (`hmac-sha256`). La política `grant httpaas-key zonesub ANY` permite que cualquier petición firmada con la clave `httpaas-key` modifique cualquier subnombre.

La clave la genera `setup-dns-server.sh` con `tsig-keygen`, la deja en `/etc/bind/named.conf.keys` dentro de la VM y la copia al host (`webapp/data/dnskey.conf`) para que `nsupdate -k` la use al actualizar.

### Red

Una sola red host-only `vboxnet0` (`192.168.56.0/24`). El host se asigna `.1`, el DNS `.10`, las instancias `.100–.200`. No usamos el DHCP de VirtualBox para evitar carreras entre asignaciones; la webapp asigna IPs explícitamente y las inyecta como guest properties.

Cada VM tiene dos NICs:

- `nic1` = `hostonly` → tráfico con el host y con otras VMs.
- `nic2` = `nat` → acceso a Internet (para `apt-get install` durante el setup del DNS). El first-boot baja la métrica de la NAT para que el gateway por defecto sea el del host-only.

## Flujo de aprovisionamiento

```
 usuario              webapp Go              VBoxManage         Bind9          VM nueva
   │                     │                       │               │                │
   │ POST /provision     │                       │               │                │
   │ (form + zip)        │                       │               │                │
   │────────────────────►│                       │               │                │
   │                     │ AllocateIP            │               │                │
   │                     │ Put(Instance)         │               │                │
   │ 303 → dashboard     │                       │               │                │
   │◄────────────────────│                       │               │                │
   │                     │                       │               │                │
   │  ┌───────── goroutine: runProvision ──────┐                 │                │
   │  │                  │                       │               │                │
   │  │                  │ createvm/modifyvm     │               │                │
   │  │                  │ storageattach mtach   │               │                │
   │  │                  │──────────────────────►│               │                │
   │  │                  │ guestproperty set *6  │               │                │
   │  │                  │──────────────────────►│               │                │
   │  │                  │ startvm headless      │               │                │
   │  │                  │──────────────────────►│ ─── boot ───► │                │
   │  │                  │                       │               │ first-boot lee │
   │  │                  │                       │               │ props, set IP, │
   │  │                  │                       │               │ inicia apache, │
   │  │                  │                       │               │ sshd.          │
   │  │                  │ TCP 22 abierto?       │               │                │
   │  │                  │◄────────────────────────────────────────────────────── │
   │  │                  │ nsupdate AddRecord    │               │                │
   │  │                  │─────────────────────────────────────► │                │
   │  │                  │ scp zip → /tmp        │               │                │
   │  │                  │────────────────────────────────────────────────────────►│
   │  │                  │ ssh "unzip /var/www;  │               │                │
   │  │                  │   chown www-data;     │               │                │
   │  │                  │   reload apache2"     │               │                │
   │  │                  │────────────────────────────────────────────────────────►│
   │  │                  │ state = running       │               │                │
   │  └───────────────────────────────────────┘                  │                │
   │                                                             │                │
   │ GET http://<host>.cloud.local/ ────► (resuelve vía Bind9) ─►│                │
   │                                                             │ apache sirve   │
   │◄─────────────────────────────────────────────────────────────────────────────│
```

## Trade-offs

| Decisión | Por qué | Alternativa |
|---|---|---|
| Guest properties para configurar la VM | Sin dependencias, funciona offline, no hace falta cloud-init | cloud-init (más estándar, más pesado) |
| `multiattach` para el disco | Provisionado en segundos, casi cero espacio extra | Clonar disco completo (más simple, mucho más lento y pesado) |
| TSIG + nsupdate | Estándar DNS dinámico; reutilizable en producción | API HTTP custom encima de Bind (no estándar) |
| JSON file store | Cero infra adicional | SQLite/Postgres |
| systemd-networkd | Configuración declarativa, ya viene en Debian 13 | ifupdown clásico |
| sin frameworks Go | `net/http` y `html/template` bastan para lo que hace la app | gin/echo/fiber |
| Polling cada 3s | Simple, no requiere websockets | SSE o WebSockets |

## Trabajo futuro

- WebSockets/SSE para reemplazar el polling.
- HTTPS con certificados auto-firmados por instancia.
- Cuotas y TTL (autoeliminación tras N horas).
- Métricas con Prometheus + Grafana.
- Sustituir multiattach por linked clones con snapshots para mayor aislamiento.
- Despliegue de imágenes de contenedor en vez de zips estáticos.
