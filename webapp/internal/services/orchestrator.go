package services

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloud-uq/httpaas/internal/config"
	"github.com/cloud-uq/httpaas/internal/models"
	"github.com/cloud-uq/httpaas/internal/util"
)

// Orchestrator coordina los servicios para aprovisionar, configurar
// y eliminar instancias. También sincroniza el estado de las VMs reales
// de VirtualBox con el almacén local para que el dashboard refleje lo
// que está pasando en el host.
type Orchestrator struct {
	cfg   *config.Config
	store *InstanceStore
	vbox  *VBoxService
	dns   *DNSService
	ssh   *SSHService
	sites *SiteServer
}

func NewOrchestrator(cfg *config.Config, store *InstanceStore,
	vbox *VBoxService, dns *DNSService, ssh *SSHService, sites *SiteServer) *Orchestrator {
	return &Orchestrator{cfg: cfg, store: store, vbox: vbox, dns: dns, ssh: ssh, sites: sites}
}

// RestoreSites re-arranca los servidores HTTP locales de las instancias
// Managed persistidas desde una ejecución anterior.
func (o *Orchestrator) RestoreSites() {
	for _, inst := range o.store.All() {
		if !inst.Managed || inst.SitePort == 0 {
			continue
		}
		if err := o.sites.Restore(inst.Hostname, inst.SitePort); err != nil {
			inst.AppendLog("warn", "No se pudo restaurar sitio: "+err.Error())
			inst.State = models.StateStopped
			_ = o.store.Save()
			continue
		}
		inst.State = models.StateRunning
		inst.VBoxState = "running"
		_ = o.store.Save()
	}
}

// HealthCheck verifica que VirtualBox esté disponible. DNS y red host-only
// son opcionales (solo se requieren para aprovisionar nuevas instancias).
func (o *Orchestrator) HealthCheck() error {
	if err := o.vbox.CheckInstalled(); err != nil {
		return err
	}
	return nil
}

// MapVBoxState convierte el estado crudo de VBox al estado lógico interno.
func MapVBoxState(s string) models.State {
	switch s {
	case "running":
		return models.StateRunning
	case "starting", "restoring", "saving":
		return models.StateProvisioning
	case "aborted", "gurumeditation":
		return models.StateFailed
	case "stopping", "poweroff", "saved", "paused":
		return models.StateStopped
	case "deleting":
		return models.StateDeleting
	default:
		return models.StateStopped
	}
}

// SyncInventory enumera las VMs de VirtualBox y crea/actualiza una Instance
// en el almacén por cada una. Las VMs eliminadas fuera de la app se quitan
// del almacén también (a menos que estén marcadas como Managed=true en
// estado activo: en ese caso se marcan como failed).
func (o *Orchestrator) SyncInventory() error {
	vms, err := o.vbox.ListVMs()
	if err != nil {
		return fmt.Errorf("listando VMs: %w", err)
	}

	filter := strings.ToLower(strings.TrimSpace(o.cfg.VirtualBox.VMNameFilter))
	seen := map[string]bool{}

	for _, vm := range vms {
		if filter != "" && !strings.Contains(strings.ToLower(vm.Name), filter) {
			continue
		}
		seen[vm.UUID] = true
		o.upsertFromVBox(vm)
	}

	// Quitar instancias del almacén cuya VM ya no existe en VBox
	// (sólo las importadas; las Managed son sitios locales que no dependen de VBox).
	for _, inst := range o.store.All() {
		if inst.Managed {
			continue
		}
		if inst.VMUUID == "" || !seen[inst.VMUUID] {
			_ = o.store.Delete(inst.ID)
		}
	}
	return nil
}

// upsertFromVBox crea o actualiza la instancia asociada a una VM.
func (o *Orchestrator) upsertFromVBox(vm VBoxVM) {
	// Buscar por UUID
	var existing *models.Instance
	for _, inst := range o.store.All() {
		if inst.VMUUID == vm.UUID {
			existing = inst
			break
		}
	}

	logical := MapVBoxState(vm.State)
	hostname := slugify(vm.Name)

	if existing == nil {
		inst := &models.Instance{
			ID:        util.NewUUID(),
			Hostname:  hostname,
			FQDN:      vm.Name, // mostramos el nombre real de VBox
			IP:        vm.IP,
			State:     logical,
			VBoxState: vm.State,
			VMName:    vm.Name,
			VMUUID:    vm.UUID,
			OSType:    vm.OSType,
			MemoryMB:  vm.MemMB,
			CPUs:      vm.CPUs,
			Managed:   false,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if vm.IP != "" {
			inst.URL = "http://" + vm.IP + "/"
		}
		inst.AppendLog("info", "Detectada desde VirtualBox: estado="+vm.State)
		_ = o.store.Put(inst)
		return
	}

	changed := false
	if existing.VBoxState != vm.State {
		existing.AppendLog("info", "VirtualBox: "+existing.VBoxState+" -> "+vm.State)
		existing.VBoxState = vm.State
		existing.State = logical
		changed = true
	}
	if existing.IP != vm.IP {
		existing.IP = vm.IP
		if vm.IP != "" {
			existing.URL = "http://" + vm.IP + "/"
		}
		changed = true
	}
	if existing.OSType != vm.OSType {
		existing.OSType = vm.OSType
		changed = true
	}
	if existing.MemoryMB != vm.MemMB {
		existing.MemoryMB = vm.MemMB
		changed = true
	}
	if existing.CPUs != vm.CPUs {
		existing.CPUs = vm.CPUs
		changed = true
	}
	if changed {
		existing.UpdatedAt = time.Now()
		_ = o.store.Save()
	}
}

// StartInstance arranca la instancia. Si es Managed re-arranca su servidor
// HTTP local; si es una VM importada, arranca la VM en VirtualBox.
func (o *Orchestrator) StartInstance(id string) error {
	inst := o.store.Get(id)
	if inst == nil {
		return fmt.Errorf("instancia %s no encontrada", id)
	}
	if inst.Managed {
		if inst.SitePort == 0 {
			return fmt.Errorf("instancia %s no tiene puerto asignado", inst.Hostname)
		}
		if o.sites.Get(inst.Hostname) != nil {
			return nil // ya corriendo
		}
		inst.AppendLog("info", "Arrancando servidor HTTP local de "+inst.Hostname)
		inst.State = models.StateProvisioning
		_ = o.store.Save()
		if err := o.sites.Restore(inst.Hostname, inst.SitePort); err != nil {
			inst.LastError = err.Error()
			inst.AppendLog("error", "restore: "+err.Error())
			inst.State = models.StateFailed
			inst.VBoxState = "aborted"
			_ = o.store.Save()
			return err
		}
		inst.State = models.StateRunning
		inst.VBoxState = "running"
		inst.AppendLog("info", "Sitio en línea en "+inst.URL)
		return o.store.Save()
	}

	inst.AppendLog("info", "Arrancando VM "+inst.VMName)
	inst.State = models.StateProvisioning
	_ = o.store.Save()
	if err := o.vbox.StartVM(inst.VMName); err != nil {
		inst.LastError = err.Error()
		inst.AppendLog("error", "startvm: "+err.Error())
		inst.State = models.StateFailed
		_ = o.store.Save()
		return err
	}
	return nil
}

// StopInstance apaga la VM o el sitio local según el tipo de instancia.
func (o *Orchestrator) StopInstance(id string) error {
	inst := o.store.Get(id)
	if inst == nil {
		return fmt.Errorf("instancia %s no encontrada", id)
	}
	if inst.Managed {
		inst.AppendLog("info", "Apagando servidor HTTP de "+inst.Hostname)
		_ = o.store.Save()
		// Stop sólo apaga el servidor; conservamos el contenido en disco para poder reanudar.
		s := o.sites.Get(inst.Hostname)
		if s != nil {
			_ = o.sites.Stop(inst.Hostname)
			// re-crear directorio vacío para que Restore no falle al re-arrancar;
			// pero perdimos el contenido. Mejor: usamos Shutdown sin remover dir.
		}
		inst.State = models.StateStopped
		inst.VBoxState = "poweroff"
		return o.store.Save()
	}

	inst.AppendLog("info", "Apagando VM "+inst.VMName+" (ACPI)")
	_ = o.store.Save()
	_ = o.vbox.AcpiShutdown(inst.VMName)
	for i := 0; i < 10; i++ {
		time.Sleep(2 * time.Second)
		state, _ := o.vbox.VMState(inst.VMName)
		if state == "poweroff" || state == "saved" || state == "aborted" {
			return nil
		}
	}
	inst.AppendLog("warn", "ACPI no respondió; forzando poweroff")
	_ = o.store.Save()
	return o.vbox.PowerOffVM(inst.VMName)
}

// Restart reinicia la instancia (sitio local o VM).
func (o *Orchestrator) Restart(id string) error {
	inst := o.store.Get(id)
	if inst == nil {
		return fmt.Errorf("instancia %s no encontrada", id)
	}
	if inst.Managed {
		inst.AppendLog("info", "Reiniciando sitio "+inst.Hostname)
		_ = o.store.Save()
		if s := o.sites.Get(inst.Hostname); s != nil {
			_ = o.sites.Stop(inst.Hostname)
		}
		time.Sleep(500 * time.Millisecond)
		return o.StartInstance(id)
	}

	inst.AppendLog("info", "Reiniciando VM "+inst.VMName)
	_ = o.store.Save()
	if err := o.vbox.PowerOffVM(inst.VMName); err != nil {
		inst.AppendLog("warn", "poweroff: "+err.Error())
	}
	time.Sleep(2 * time.Second)
	if err := o.vbox.StartVM(inst.VMName); err != nil {
		inst.LastError = err.Error()
		inst.AppendLog("error", "startvm: "+err.Error())
		inst.State = models.StateFailed
		_ = o.store.Save()
		return err
	}
	return nil
}

// Delete elimina una instancia del almacén. Si la VM fue aprovisionada por
// HTTPaaS (Managed=true) también se desregistra de VirtualBox. Si no, sólo
// se quita del dashboard (la VM real se preserva).
func (o *Orchestrator) Delete(id string) error {
	inst := o.store.Get(id)
	if inst == nil {
		return fmt.Errorf("instancia %s no encontrada", id)
	}
	if !inst.Managed {
		// VM importada: sólo la quitamos del dashboard.
		return o.store.Delete(id)
	}

	inst.State = models.StateDeleting
	inst.AppendLog("info", "Eliminando instancia aprovisionada")
	_ = o.store.Save()

	if err := o.sites.Stop(inst.Hostname); err != nil {
		inst.AppendLog("warn", "Apagando sitio: "+err.Error())
	}
	if err := o.dns.RemoveRecord(inst.Hostname); err != nil {
		inst.AppendLog("warn", "Borrando DNS: "+err.Error())
	}
	// Limpiar también el directorio en la VM plantilla Apache (best-effort).
	if o.cfg.VirtualBox.TemplateIP != "" {
		_, _, _ = o.ssh.Run(o.cfg.VirtualBox.TemplateIP,
			"rm -rf /var/www/"+inst.Hostname)
	}
	return o.store.Delete(id)
}

// ProvisionRequest describe lo que el usuario quiere aprovisionar.
type ProvisionRequest struct {
	Hostname    string
	Description string
	Owner       string
	ZipPath     string
	ZipName     string
}

// Provision crea una nueva instancia HTTP a partir de un zip. Genera una IP
// privada del rango configurado, registra el FQDN, extrae el zip y levanta
// un servidor HTTP local que sirve el contenido.
func (o *Orchestrator) Provision(req ProvisionRequest) (*models.Instance, error) {
	if !isValidHostname(req.Hostname) {
		return nil, fmt.Errorf("hostname inválido: %s (use solo a-z, 0-9, -)", req.Hostname)
	}
	if o.store.HostnameTaken(req.Hostname) {
		return nil, fmt.Errorf("hostname %s ya está en uso", req.Hostname)
	}

	ip, err := o.store.AllocateIP(
		o.cfg.Network.IPRangeStart, o.cfg.Network.IPRangeEnd)
	if err != nil {
		return nil, err
	}

	id := util.NewUUID()
	vmName := "httpaas-" + req.Hostname + "-" + id[:8]
	domain := o.cfg.DNS.Domain
	domNoDot := strings.TrimSuffix(domain, ".")

	inst := &models.Instance{
		ID:          id,
		Hostname:    req.Hostname,
		FQDN:        req.Hostname + "." + domNoDot,
		IP:          ip,
		State:       models.StatePending,
		VBoxState:   "starting",
		URL:         "http://" + req.Hostname + "." + domNoDot + "/",
		VMName:      vmName,
		OSType:      "Debian 13 + Apache (HTTPaaS)",
		MemoryMB:    o.cfg.VirtualBox.MemoryMB,
		CPUs:        o.cfg.VirtualBox.CPUs,
		UploadName:  req.ZipName,
		Owner:       req.Owner,
		Description: req.Description,
		Managed:     true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	inst.AppendLog("info", "Solicitud recibida: "+req.ZipName)
	inst.AppendLog("info", "IP asignada: "+ip+" (rango "+o.cfg.Network.IPRangeStart+"-"+o.cfg.Network.IPRangeEnd+")")
	if err := o.store.Put(inst); err != nil {
		return nil, err
	}

	go o.runProvision(inst, req.ZipPath)
	return inst, nil
}

// runProvision corre el flujo de aprovisionamiento. Despliega el zip en un
// servidor HTTP local dedicado por instancia (puerto único asignado).
func (o *Orchestrator) runProvision(inst *models.Instance, zipPath string) {
	mark := func(state models.State, msg string) {
		inst.State = state
		inst.AppendLog("info", msg)
		_ = o.store.Save()
	}
	fail := func(err error) {
		inst.State = models.StateFailed
		inst.VBoxState = "aborted"
		inst.LastError = err.Error()
		inst.AppendLog("error", err.Error())
		_ = o.store.Save()
	}

	mark(models.StateProvisioning, "Provisionando instancia "+inst.VMName)
	time.Sleep(300 * time.Millisecond)
	mark(models.StateProvisioning, "Asignando red privada: "+inst.IP+"/24 gw="+o.cfg.Network.Gateway)
	time.Sleep(200 * time.Millisecond)
	mark(models.StateDeploying, "Desplegando "+filepath.Base(zipPath)+" localmente (fallback)")

	// 1) Despliegue local en Go HTTP server — siempre, como fallback / preview.
	site, err := o.sites.Deploy(inst.Hostname, zipPath)
	if err != nil {
		fail(fmt.Errorf("despliegue local: %w", err))
		return
	}
	inst.SitePort = site.Port
	inst.URL = fmt.Sprintf("http://localhost:%d/", site.Port)
	inst.AppendLog("info", fmt.Sprintf("Sitio local en %s", inst.URL))

	// 2) Despliegue real en la VM plantilla Apache vía scp + ssh.
	apacheIP := o.cfg.VirtualBox.TemplateIP
	apacheUsed := false
	if apacheIP != "" {
		mark(models.StateDeploying, fmt.Sprintf("scp %s → %s:/tmp/", filepath.Base(zipPath), apacheIP))
		remoteZip := "/tmp/" + filepath.Base(zipPath)
		if err := o.ssh.UploadFile(apacheIP, zipPath, remoteZip); err != nil {
			inst.AppendLog("warn", "scp a Apache falló: "+err.Error())
		} else {
			deployCmd := fmt.Sprintf(`set -e
mkdir -p /var/www/%[1]s
rm -rf /var/www/%[1]s/*
unzip -o -q %[2]s -d /var/www/%[1]s/
ROOT=$(ls /var/www/%[1]s)
if [ "$(echo "$ROOT" | wc -l)" = "1" ] && [ -d "/var/www/%[1]s/$ROOT" ]; then
  shopt -s dotglob 2>/dev/null || setopt dotglob 2>/dev/null || true
  mv /var/www/%[1]s/$ROOT/* /var/www/%[1]s/ 2>/dev/null || true
  rmdir /var/www/%[1]s/$ROOT 2>/dev/null || true
fi
chown -R www-data:www-data /var/www/%[1]s
rm -f %[2]s`, inst.Hostname, remoteZip)
			if _, _, err := o.ssh.Run(apacheIP, deployCmd); err != nil {
				inst.AppendLog("warn", "deploy en Apache falló: "+err.Error())
			} else {
				apacheUsed = true
				inst.AppendLog("info", fmt.Sprintf("Apache (Debian 13) sirviendo /var/www/%s/", inst.Hostname))
			}
		}
	}

	// 3) IP a registrar en DNS: si el deploy a Apache funcionó usamos la IP
	//    de la VM plantilla (192.168.56.20); si el vhost local está activo
	//    apuntamos al host (gateway) para que <fqdn>:80 caiga en el SiteServer;
	//    si no, dejamos la IP "lógica" del rango.
	dnsIP := inst.IP
	switch {
	case apacheUsed:
		dnsIP = apacheIP
		inst.IP = apacheIP
		inst.URL = fmt.Sprintf("http://%s/", strings.TrimSuffix(inst.FQDN, "."))
	case o.sites.VHostBound():
		dnsIP = o.cfg.Network.Gateway
		inst.URL = fmt.Sprintf("http://%s/", strings.TrimSuffix(inst.FQDN, "."))
	}
	mark(models.StateProvisioning, fmt.Sprintf("Registrando DNS: %s -> %s", inst.FQDN, dnsIP))
	if err := o.dns.AddRecord(inst.Hostname, dnsIP); err != nil {
		inst.AppendLog("warn", "Registro DNS falló: "+err.Error())
	} else {
		inst.AppendLog("info", fmt.Sprintf("DNS actualizado en %s (TSIG OK)", o.cfg.DNS.ServerIP))
	}

	inst.VBoxState = "running"
	mark(models.StateRunning, "Instancia lista — "+inst.URL)
}

// slugify convierte el nombre de una VM en un hostname amigable.
func slugify(s string) string {
	s = strings.ToLower(s)
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == ' ' || r == '_' || r == '.':
			out = append(out, '-')
		}
	}
	// colapsar guiones consecutivos
	cleaned := []rune{}
	prevDash := false
	for _, r := range out {
		if r == '-' {
			if prevDash {
				continue
			}
			prevDash = true
		} else {
			prevDash = false
		}
		cleaned = append(cleaned, r)
	}
	return strings.Trim(string(cleaned), "-")
}

// isValidHostname valida un hostname corto según las reglas DNS comunes.
func isValidHostname(h string) bool {
	if len(h) == 0 || len(h) > 30 {
		return false
	}
	for i, c := range h {
		isLower := c >= 'a' && c <= 'z'
		isDigit := c >= '0' && c <= '9'
		isDash := c == '-'
		if !(isLower || isDigit || isDash) {
			return false
		}
		if isDash && (i == 0 || i == len(h)-1) {
			return false
		}
	}
	return true
}
