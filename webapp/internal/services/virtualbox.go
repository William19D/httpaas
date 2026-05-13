package services

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloud-uq/httpaas/internal/config"
)

// VBoxService es un wrapper alrededor del CLI VBoxManage. Cada método
// representa una operación sobre VirtualBox que es invocada por el
// orquestador. La idea es aislar todo lo que toca el subproceso aquí.
type VBoxService struct {
	cfg *config.Config
}

func NewVBoxService(cfg *config.Config) *VBoxService {
	return &VBoxService{cfg: cfg}
}

// vboxBin returns the configured VBoxManage path, falling back to PATH lookup.
func (v *VBoxService) vboxBin() string {
	if v.cfg.VirtualBox.ManageBin != "" {
		return v.cfg.VirtualBox.ManageBin
	}
	return "VBoxManage"
}

// run ejecuta VBoxManage con los argumentos dados.
func (v *VBoxService) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, v.vboxBin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf(
			"VBoxManage %s falló: %w | stderr: %s",
			strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

// CheckInstalled valida que VBoxManage esté disponible (en PATH o en la ruta configurada).
func (v *VBoxService) CheckInstalled() error {
	bin := v.vboxBin()
	if filepath.IsAbs(bin) {
		if _, err := os.Stat(bin); err != nil {
			return fmt.Errorf("VBoxManage no encontrado en %s: %w", bin, err)
		}
		return nil
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("VBoxManage no encontrado en PATH; instale VirtualBox")
	}
	return nil
}

// TemplateExists indica si la VM plantilla existe.
func (v *VBoxService) TemplateExists() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := v.run(ctx, "list", "vms")
	return strings.Contains(out, "\""+v.cfg.VirtualBox.TemplateVM+"\"")
}

// HostOnlyExists indica si la red host-only existe.
func (v *VBoxService) HostOnlyExists() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := v.run(ctx, "list", "hostonlyifs")
	return strings.Contains(out, v.cfg.Network.HostOnlyNet)
}

// VMState devuelve el estado actual de la VM (running/poweroff/...).
func (v *VBoxService) VMState(vmName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := v.run(ctx, "showvminfo", vmName, "--machinereadable")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "VMState=") {
			return strings.Trim(strings.TrimPrefix(line, "VMState="), `"`), nil
		}
	}
	return "", fmt.Errorf("VMState no encontrado para %s", vmName)
}

// CreateInstance crea una nueva VM clonando la plantilla y adjuntando el disco
// en modo multiconexión (multiattach). Devuelve cuando la VM está apagada y lista.
//
// El nombre vmName debe ser único. Las MACs se regeneran automáticamente al clonar.
func (v *VBoxService) CreateInstance(vmName, macHex string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Crear la VM con el mismo OS type que la plantilla.
	if _, err := v.run(ctx, "createvm",
		"--name", vmName,
		"--ostype", "Debian_64",
		"--register"); err != nil {
		return err
	}

	// Configurar RAM y CPUs.
	if _, err := v.run(ctx, "modifyvm", vmName,
		"--memory", fmt.Sprintf("%d", v.cfg.VirtualBox.MemoryMB),
		"--cpus", fmt.Sprintf("%d", v.cfg.VirtualBox.CPUs),
		"--vram", "16",
		"--graphicscontroller", "vmsvga",
		"--audio", "none",
		"--usb", "off"); err != nil {
		return err
	}

	// Configurar red: host-only para alcance desde el host y NAT para Internet.
	if _, err := v.run(ctx, "modifyvm", vmName,
		"--nic1", "hostonly",
		"--hostonlyadapter1", v.cfg.Network.HostOnlyNet,
		"--macaddress1", macHex,
		"--nic2", "nat"); err != nil {
		return err
	}

	// Crear un controlador SATA y adjuntar el disco en modo MULTICONEXIÓN.
	// Multiattach permite que múltiples VMs compartan el mismo .vdi inmutable;
	// cada VM mantiene su delta privado. Es la pieza clave del proyecto.
	if _, err := v.run(ctx, "storagectl", vmName,
		"--name", "SATA",
		"--add", "sata",
		"--portcount", "2",
		"--bootable", "on"); err != nil {
		return err
	}

	if _, err := v.run(ctx, "storageattach", vmName,
		"--storagectl", "SATA",
		"--port", "0",
		"--device", "0",
		"--type", "hdd",
		"--mtype", "multiattach",
		"--medium", v.cfg.VirtualBox.TemplateDisk); err != nil {
		return err
	}

	return nil
}

// StartVM arranca la VM en modo headless (sin GUI).
func (v *VBoxService) StartVM(vmName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := v.run(ctx, "startvm", vmName, "--type", "headless")
	return err
}

// PowerOffVM apaga forzosamente la VM.
func (v *VBoxService) PowerOffVM(vmName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, _ := v.VMState(vmName)
	if state == "poweroff" || state == "aborted" {
		return nil
	}
	_, err := v.run(ctx, "controlvm", vmName, "poweroff")
	return err
}

// UnregisterVM desregistra y borra todos los archivos asociados a la VM.
func (v *VBoxService) UnregisterVM(vmName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := v.run(ctx, "unregistervm", vmName, "--delete")
	return err
}

// WaitForVMRunning espera hasta que la VM esté en estado running.
func (v *VBoxService) WaitForVMRunning(vmName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, err := v.VMState(vmName)
		if err == nil && state == "running" {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout esperando running para %s", vmName)
}

// VBoxVM resume una VM detectada por `VBoxManage list vms`.
type VBoxVM struct {
	Name   string
	UUID   string
	State  string // raw VBox state: running, poweroff, saved, paused, aborted, ...
	OSType string
	MemMB  int
	CPUs   int
	IP     string // IP detectada (host-only o NAT guest-property), si la hay
}

// ListVMs enumera todas las VMs registradas y devuelve un resumen por cada una.
func (v *VBoxService) ListVMs() ([]VBoxVM, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := v.run(ctx, "list", "vms")
	if err != nil {
		return nil, err
	}
	var vms []VBoxVM
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		// Formato:  "nombre" {uuid}
		end := strings.LastIndex(line, "\"")
		if !strings.HasPrefix(line, "\"") || end <= 0 {
			continue
		}
		name := line[1:end]
		uuid := strings.Trim(strings.TrimSpace(line[end+1:]), "{}")
		info, err := v.VMInfo(name)
		if err != nil {
			// no rompemos por una VM; continuamos
			vms = append(vms, VBoxVM{Name: name, UUID: uuid, State: "unknown"})
			continue
		}
		info.Name = name
		info.UUID = uuid
		vms = append(vms, info)
	}
	return vms, nil
}

// VMInfo devuelve los datos relevantes de una VM (state, ostype, mem, cpus, ip).
func (v *VBoxService) VMInfo(name string) (VBoxVM, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := v.run(ctx, "showvminfo", name, "--machinereadable")
	if err != nil {
		return VBoxVM{}, err
	}
	info := VBoxVM{Name: name}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		k, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, "\"")
		switch k {
		case "VMState":
			info.State = val
		case "ostype":
			info.OSType = val
		case "memory":
			fmt.Sscanf(val, "%d", &info.MemMB)
		case "cpus":
			fmt.Sscanf(val, "%d", &info.CPUs)
		}
	}
	// Intentamos detectar IP: primero guest-property estándar, luego propiedades del orquestador.
	if ip := v.guestProperty(name, "/VirtualBox/GuestInfo/Net/0/V4/IP"); ip != "" {
		info.IP = ip
	} else if ip := v.guestProperty(name, "/HTTPaaS/ip"); ip != "" {
		info.IP = ip
	}
	return info, nil
}

// guestProperty lee una propiedad de invitado, devolviendo "" si no existe.
func (v *VBoxService) guestProperty(name, key string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	out, err := v.run(ctx, "guestproperty", "get", name, key)
	if err != nil {
		return ""
	}
	// "Value: 192.168.1.10" o "No value set!"
	out = strings.TrimSpace(strings.TrimRight(out, "\r"))
	if !strings.HasPrefix(out, "Value:") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(out, "Value:"))
}

// StartVMByName arranca una VM por nombre (alias semántico de StartVM).
func (v *VBoxService) StartVMByName(name string) error {
	return v.StartVM(name)
}

// AcpiShutdown intenta un apagado limpio vía ACPI antes de poweroff.
func (v *VBoxService) AcpiShutdown(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := v.run(ctx, "controlvm", name, "acpipowerbutton")
	return err
}

// GenerateMAC produce una dirección MAC aleatoria, formateada para VBoxManage
// (sin separadores). La dejamos determinística por nombre para depuración: se
// puede reemplazar por algo aleatorio si se desea.
func GenerateMAC(seed string) string {
	// 080027 es el OUI de Oracle; el resto se deriva del seed para reproducibilidad.
	h := uint32(0)
	for _, c := range seed {
		h = h*131 + uint32(c)
	}
	return fmt.Sprintf("080027%06X", h&0xFFFFFF)
}
