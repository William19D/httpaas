# HTTPaaS — HTTP as a Service

A locally-hosted, education-oriented platform that simulates a managed
HTTP server provisioning service in the spirit of public cloud
offerings such as AWS, GCP, or Azure. A user uploads a `.zip` file
containing a static site or web application, and HTTPaaS automatically:

- creates a new VirtualBox virtual machine from a shared base disk,
- assigns the machine a private IP and a DNS name in a local zone
  managed by Bind9,
- deploys the user's content to Apache (or to a built-in fallback
  HTTP server) and exposes a friendly URL such as
  `http://my-site.cloud.local/`.

The project was developed for the *Cloud Computing 2026-1* course at
Universidad del Quindio. The implementation is intentionally lightweight:
the management web application is a single Go binary using only the
standard library, the orchestration relies on `VBoxManage`, `ssh`, and
`nsupdate`, and the entire installation runs from a single shell
script.

---

## Table of contents

1. [Overview](#overview)
2. [Architecture](#architecture)
3. [Features](#features)
4. [Prerequisites](#prerequisites)
5. [Installation](#installation)
6. [Using the platform](#using-the-platform)
7. [How provisioning works](#how-provisioning-works)
8. [DNS zone and TSIG-signed updates](#dns-zone-and-tsig-signed-updates)
9. [Multiattach disks: serving many VMs from one image](#multiattach-disks-serving-many-vms-from-one-image)
10. [Project layout](#project-layout)
11. [Configuration reference](#configuration-reference)
12. [Local fallback and host-side vhost proxy](#local-fallback-and-host-side-vhost-proxy)
13. [Troubleshooting](#troubleshooting)
14. [Tear-down](#tear-down)
15. [Credits](#credits)

---

## Overview

HTTPaaS combines four standard pieces of infrastructure and exposes
them through a single management interface:

- **VirtualBox** hosts the virtual machines.
- **Bind9** runs in a dedicated VM and serves the local DNS zone
  `cloud.local.`.
- **Apache HTTP Server** lives on a template Debian VM whose disk is
  marked as the base for new instances.
- **A Go web application** (this repository) coordinates everything:
  it accepts the user's request, clones a VM, registers the new host
  in DNS, deploys the content, and exposes the running instance in
  the dashboard.

The point of the project is not to replace any production cloud
offering. It is to make every layer of a typical IaaS / PaaS
provisioning pipeline visible and inspectable in less than two
thousand lines of code.

## Architecture

```
+----------------------------------------------------------------+
|                            HOST MACHINE                        |
|                                                                |
|   browser  ----->  httpaas (Go, :8080)  ----->  VBoxManage     |
|                          |                                     |
|                          |  nsupdate + TSIG                    |
|                          |  ssh / scp                          |
|                          v                                     |
|  +------------- vboxnet0 (192.168.56.0/24) ----------------+   |
|  |                                                         |   |
|  |   +-------------+   +-----------------+   +---------+   |   |
|  |   | httpaas-dns |   | httpaas-tpl     |   | extra   |   |   |
|  |   | Bind9       |   | Apache template |   | apps    |   |   |
|  |   | 192.168.56.10|  | 192.168.56.20   |   |  ...    |   |   |
|  |   +-------------+   +--------+--------+   +---------+   |   |
|  |                              |                          |   |
|  |                multiattach disk shared with             |   |
|  |                all instances created later              |   |
|  +---------------------------------------------------------+   |
+----------------------------------------------------------------+
```

A full version of the diagram, including the request flow and the
per-step error handling, is in [`docs/architecture.md`](docs/architecture.md).

## Features

- Single-command installation (`./install.sh`).
- Static-site deployment from an uploaded `.zip` file.
- Automatic IP allocation in a configurable range.
- DNS zone `cloud.local.` with dynamic updates secured by TSIG.
- Per-instance VM cloned from a shared multiattach base disk, so
  each new instance costs around fifty megabytes instead of two
  gigabytes.
- Live status updates in the dashboard (`pending`, `provisioning`,
  `deploying`, `running`, `failed`).
- Optional host-side virtual-host proxy listening on the host-only
  network, so the user sees `http://my-site.cloud.local/` even when
  the Apache VM is unreachable.
- Graceful tear-down: removing an instance unregisters its DNS
  record, powers off the VM, deletes the differencing disk, and
  frees its IP back into the pool.
- A single Go binary with no third-party dependencies.

## Prerequisites

The host machine needs the following:

| Software | Notes |
|---|---|
| Operating system | Linux (recommended Debian / Ubuntu) or Windows 10 / 11 |
| VirtualBox 7+ | With the Oracle VirtualBox Extension Pack |
| Go 1.22 or newer | Only required when building from source |
| SSH client | `ssh`, `scp`, `ssh-keygen` |
| Bind9 tools | `nsupdate` and `dig` (Debian package `bind9-dnsutils`, Windows users can use the Scoop `bind` bucket) |
| Optional | `sshpass` and `unzip` for the helper scripts |

Hardware: about four gigabytes of free memory (one gigabyte for the
DNS VM and roughly seven hundred megabytes per Apache instance) and
fifteen gigabytes of free disk space for the template image and the
Debian installer.

## Installation

The repository contains a master script that performs every step in
the correct order. From the project root run:

```bash
./install.sh
```

The script executes the following phases:

1. `check-prereqs.sh` verifies that all required commands are
   available and that the user belongs to the `vboxusers` group.
2. `setup-host-network.sh` creates the host-only network `vboxnet0`
   with the address `192.168.56.1/24`.
3. `setup-template-vm.sh` downloads the Debian 13 installer, creates
   the `httpaas-template` VM, applies a preseed configuration,
   installs Apache and the guest tools, and finally marks the disk as
   multiattach so it can be shared by every instance created later.
4. `setup-dns-server.sh` clones the template into `httpaas-dns`,
   installs Bind9, generates a TSIG key, loads the zone
   `cloud.local.`, and saves the key in
   `webapp/data/dnskey.conf` so the web application can use it.
5. `setup-webapp.sh` compiles the Go web application and writes a
   `config.json` with the paths discovered during the previous
   phases.

Each phase is idempotent: rerunning `install.sh` after a failure will
detect what is already in place and only run what is missing. The
phases can also be skipped individually with environment variables:

```bash
SKIP_PREREQS=1 SKIP_NETWORK=1 SKIP_TEMPLATE=1 ./install.sh
SKIP_DNS=1 SKIP_TEMPLATE=1 ./install.sh
SKIP_RUN=1 ./install.sh
```

The template VM is the only step that asks for human interaction. It
opens the VirtualBox GUI, displays the Debian boot menu, and asks the
operator to press `Tab` on the `Install` entry and append the URL of
the preseed file that the script is already serving. The script
prints the exact line on screen. Skip this step with
`SKIP_INSTALL=1 ./scripts/setup-template-vm.sh` when a suitable Debian
13 VM already exists.

### Manual / Windows installation

The installation scripts target Linux hosts. Windows users can still
run HTTPaaS by performing the equivalent steps manually:

1. Install Go, VirtualBox, OpenSSH, and the Bind tools
   (`scoop install go virtualbox openssh bind`).
2. Add a host-only adapter with the address `192.168.56.1/24` via
   VirtualBox.
3. Build the web application:
   ```powershell
   cd webapp
   go build -o httpaas.exe ./cmd/httpaas
   ```
4. Create or import a Debian or Alpine VM that will serve DNS. The
   `scripts/dns-vm/` directory contains an Alpine answer file
   (`answers.alpine`) and a configuration script
   (`configure-bind.sh`) that prepare Bind9 once the VM has SSH up.
5. Run the application:
   ```powershell
   ./httpaas.exe
   ```

Adjust `webapp/config.json` so the paths match the local machine; the
default values target a Linux home directory.

## Using the platform

Once the application is running, open
[http://localhost:8080](http://localhost:8080). The dashboard shows
all known instances and the actions available on each one.

The typical flow is:

1. Click **Provision new instance**.
2. Fill in:
   - Hostname (lowercase, dash-separated, used as the subdomain).
   - Owner and description (optional).
   - A `.zip` file containing the site. Files should be at the root
     of the zip (an `index.html` at the top level).
3. Submit. The instance moves through
   `pending` -> `provisioning` -> `deploying` -> `running` (or
   `failed`).
4. When the state is `running`, open the URL displayed in the
   dashboard. If the host-side vhost is active, this will be
   `http://hostname.cloud.local/`. Otherwise the dashboard offers a
   localhost port fallback such as `http://localhost:9100/`.

To make the browser resolve `*.cloud.local`, point the operating
system at the DNS VM:

- **Linux**: `sudo systemd-resolve --interface=vboxnet0
  --set-dns=192.168.56.10 --set-domain=cloud.local`
- **Windows (PowerShell as administrator)**:
  `Add-DnsClientNrptRule -Namespace ".cloud.local" -NameServers
  "192.168.56.10"`
- **Manual fallback**: add the relevant hostnames to the system
  hosts file (`/etc/hosts` or
  `C:\Windows\System32\drivers\etc\hosts`).

An example site is included in `examples/sample-site.zip`.

## How provisioning works

A provisioning request from the dashboard runs through the following
steps inside the orchestrator
(`webapp/internal/services/orchestrator.go`):

1. **Persist the upload**. The uploaded file is saved to
   `webapp/uploads/` with a timestamped name and validated as a zip
   archive of at most fifty megabytes.
2. **Allocate an IP**. The next free address in the configured range
   (`192.168.56.100`–`192.168.56.200` by default) is assigned to the
   instance.
3. **Clone the template VM**. A new VM is created from the
   multiattach base disk. Identity-sensitive files (machine ID, SSH
   host keys) are regenerated on first boot.
4. **Inject configuration**. Guest properties are written under
   `/HTTPaaS/...` so the in-VM `first-boot` script can pick up the
   hostname, IP, DNS server, and gateway.
5. **Start the VM and wait**. The orchestrator polls the SSH port
   with a timeout, then waits for the guest to set its hostname and
   network.
6. **Deploy the zip to the local fallback HTTP server** at
   `127.0.0.1:91xx`. This is also what allows the dashboard to keep
   serving a deployed site even when the Apache VM is unavailable.
7. **Try to deploy on the Apache template** via `scp` and `ssh`. If
   it succeeds, the DNS record will point to the Apache VM. If it
   fails (or the template is not running), the DNS record points to
   the host so that the local fallback handles the traffic.
8. **Register the DNS record** by sending a TSIG-signed `nsupdate`
   command to Bind9. The record TTL is 300 seconds.
9. **Mark the instance as `running`** and update the dashboard.

When the user removes an instance the operations run in the opposite
order: DNS record removed, VM powered off, differencing disk
deleted, IP returned to the pool, store entry deleted.

## DNS zone and TSIG-signed updates

The DNS VM serves a zone called `cloud.local.` with two parts:

- **Forward zone** (`db.cloud.local`): contains the static records
  `dns.cloud.local A 192.168.56.10` and
  `api.cloud.local A 192.168.56.1`.
- **Reverse zone** (`db.192.168.56`): contains PTR entries for the
  same addresses.

Dynamic updates are authenticated with a TSIG key called
`httpaas-key`. The key is generated inside the DNS VM during setup
(`tsig-keygen -a hmac-sha256 httpaas-key`) and written to the host at
`webapp/data/dnskey.conf`. The web application invokes `nsupdate -k`
with that key file every time it adds or removes a record.

To test the zone manually:

```bash
dig +short @192.168.56.10 dns.cloud.local
nsupdate -k webapp/data/dnskey.conf <<EOF
server 192.168.56.10
zone cloud.local.
update add temp.cloud.local. 60 A 192.168.56.99
send
EOF
dig +short @192.168.56.10 temp.cloud.local
```

## Multiattach disks: serving many VMs from one image

A standard Debian 13 installation occupies roughly two gigabytes of
disk. Cloning one hundred such VMs would require two hundred
gigabytes, which is impractical on a typical laptop.

VirtualBox supports a *multiattach* mode in which a base disk is
mounted read-only by many VMs. Each VM also receives its own
differencing disk that stores only the modifications relative to the
base. The technique is the same idea behind container images and
copy-on-write file systems such as ZFS or Btrfs.

Each additional instance occupies approximately fifty megabytes
instead of two gigabytes. One hundred instances fit in less than
seven gigabytes.

The base disk has to be cleaned before being marked as multiattach
so that every clone receives a unique identity. The
`setup-template-vm.sh` script removes the machine ID, the SSH host
keys, and any DHCP leases before powering off the template VM for
the last time.

## Project layout

```
httpaas-project/
+-- install.sh                  master setup script
+-- scripts/                    host-side automation
|   +-- common.sh
|   +-- check-prereqs.sh
|   +-- setup-host-network.sh
|   +-- setup-template-vm.sh
|   +-- setup-dns-server.sh
|   +-- setup-webapp.sh
|   +-- teardown.sh
|   +-- dns-vm/                 files copied to the DNS VM
+-- configs/                    template files
|   +-- bind9/                  named.conf snippets and zone files
|   +-- apache/                 vhost configuration
|   +-- vm-template/            first-boot script and preseed.cfg
|   +-- systemd/                httpaas.service unit
+-- webapp/                     management application
|   +-- cmd/httpaas/main.go     entry point
|   +-- internal/
|   |   +-- config/             JSON configuration loader
|   |   +-- handlers/           HTTP handlers
|   |   +-- middleware/         logging + panic recovery
|   |   +-- models/             Instance and State types
|   |   +-- services/           vbox, dns, ssh, orchestrator, store, siteserver
|   |   +-- util/               UUID helper
|   +-- templates/              layout, dashboard, provision form, detail view
|   +-- static/                 CSS and a few JS helpers
+-- mockup/                     dashboard mockup approved by the instructor
+-- docs/                       extended documentation
+-- examples/                   sample static site for testing
```

## Configuration reference

The application reads `webapp/config.json` (or the path indicated by
the environment variable `HTTPAAS_CONFIG`). If the file does not
exist, the application creates one with sensible defaults. The most
relevant fields are:

| Section | Field | Description |
|---|---|---|
| `http` | `addr` | Address the dashboard listens on. Default `0.0.0.0:8080`. |
| `dns` | `domain` | Zone served by Bind9. Default `cloud.local.`. |
| `dns` | `server_ip` | Address of the DNS VM. Default `192.168.56.10`. |
| `dns` | `tsig_key` | Path to the TSIG key file used by `nsupdate`. |
| `dns` | `tsig_name` | Name of the TSIG key. Must match the one in Bind9. |
| `network` | `host_only_net` | Name of the VirtualBox host-only adapter. |
| `network` | `subnet` | Subnet used by the platform. |
| `network` | `gateway` | Host address inside the host-only network. |
| `network` | `ip_range_start` / `ip_range_end` | Range of addresses available to instances. |
| `virtualbox` | `manage_bin` | Path to `VBoxManage` (`.exe` on Windows). |
| `virtualbox` | `template_vm` | Name of the template VM. |
| `virtualbox` | `template_disk` | Path to the multiattach `.vdi`. |
| `virtualbox` | `template_ip` | Static IP of the running Apache template VM. |
| `virtualbox` | `memory_mb` / `cpus` | Resources per instance. |
| `ssh` | `user` | User on the Apache instances. |
| `ssh` | `key_path` | Private key the application uses to connect. |

## Local fallback and host-side vhost proxy

Two design decisions help HTTPaaS run on machines that cannot host a
full set of Apache VMs at once:

1. **Local fallback.** Every provisioning request always deploys the
   uploaded content to a Go HTTP server bound to `127.0.0.1` on a
   port allocated from the range `9100`–`9899`. This guarantees a
   working URL even when the Apache VM cannot be reached.
2. **Virtual-host proxy.** At start-up the `SiteServer` also tries to
   bind to `192.168.56.1:80`. When it succeeds, every site is
   reachable as `http://hostname.cloud.local/` with no port suffix:
   the listener inspects the `Host` header and serves the
   corresponding directory. When the bind fails (port already in use
   or insufficient privileges), the dashboard falls back to the
   `localhost:91xx` URLs.

The orchestrator records the DNS entry against the host gateway IP
when the vhost is active and against the Apache template IP when the
real Apache deployment succeeds, so the user always reaches the
correct backend through `cloud.local`.

## Troubleshooting

A list of the issues that came up during development and their fixes
lives in [`docs/troubleshooting.md`](docs/troubleshooting.md). The
most common ones are:

- **`nsupdate` times out**. The DNS VM is up but the host-only
  network is filtered. Verify that the adapter has the address
  `192.168.56.1` and that
  `dig +short @192.168.56.10 cloud.local SOA` responds.
- **`scp` to the Apache VM is refused**. The SSH key in
  `~/.ssh/id_rsa_httpaas` was not authorised inside the VM. Reinstall
  the template VM or run `ssh-copy-id` manually.
- **The new instance never reaches `running`**. The first-boot script
  inside the cloned VM is waiting for the static IP. Open the VM
  console and run `dmesg | tail` to inspect the cloud-init style
  output that the script writes to `/dev/console`.
- **The browser cannot resolve `*.cloud.local`**. The operating
  system is not aware of the local DNS server. See the
  [Using the platform](#using-the-platform) section for the per-OS
  configuration line.

## Tear-down

To remove every artefact created by HTTPaaS run:

```bash
./scripts/teardown.sh
```

The script powers off every managed VM, deletes the differencing
disks, removes the multiattach base disk and the host-only adapter,
and unregisters the optional systemd service. It always asks for
confirmation before destructive actions.

## Credits

Project developed for *Cloud Computing 2026-1* at Universidad del
Quindio. Instructors: Carlos Eduardo Gomez Montoya, Juan Sebastian
Salazar Osorio.

Technologies used: VirtualBox, Bind9, Apache HTTP Server, Debian 13,
Alpine Linux 3.21, and Go 1.22.

Source released under the MIT license.
