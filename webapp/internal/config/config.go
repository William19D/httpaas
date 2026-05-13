package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// defaultVBoxManage devuelve la ruta esperada de VBoxManage según el sistema.
func defaultVBoxManage() string {
	if runtime.GOOS == "windows" {
		return `C:\Program Files\Oracle\VirtualBox\VBoxManage.exe`
	}
	return "VBoxManage"
}

// Config contiene toda la configuración de la aplicación.
type Config struct {
	HTTP      HTTPConfig    `json:"http"`
	DNS       DNSConfig     `json:"dns"`
	Network   NetworkConfig `json:"network"`
	VirtualBox VBoxConfig   `json:"virtualbox"`
	SSH       SSHConfig     `json:"ssh"`

	UploadDir   string `json:"upload_dir"`
	DataDir     string `json:"data_dir"`
	TemplateDir string `json:"template_dir"`
	StaticDir   string `json:"static_dir"`
	ScriptsDir  string `json:"scripts_dir"`

	// DemoMode simula el aprovisionamiento sin tocar VirtualBox/Bind9/SSH.
	// Útil para correr la UI en hosts donde no hay VBox (p. ej. Windows).
	DemoMode bool `json:"demo_mode"`
}

type HTTPConfig struct {
	Addr string `json:"addr"`
}

type DNSConfig struct {
	Domain    string `json:"domain"`     // p. ej. "cloud.local."
	ServerIP  string `json:"server_ip"`  // IP de la VM Bind9
	ZoneFile  string `json:"zone_file"`  // ruta del archivo de zona (en la VM DNS)
	TSIGKey   string `json:"tsig_key"`   // ruta al keyfile para nsupdate
	TSIGName  string `json:"tsig_name"`  // nombre de la clave TSIG
}

type NetworkConfig struct {
	HostOnlyNet string `json:"host_only_net"` // p. ej. "vboxnet0"
	Subnet      string `json:"subnet"`        // p. ej. "192.168.56.0/24"
	Gateway     string `json:"gateway"`       // p. ej. "192.168.56.1"
	IPRangeStart string `json:"ip_range_start"` // p. ej. "192.168.56.100"
	IPRangeEnd   string `json:"ip_range_end"`   // p. ej. "192.168.56.200"
}

type VBoxConfig struct {
	ManageBin    string `json:"manage_bin"`    // ruta al binario VBoxManage (Windows usa .exe)
	TemplateVM   string `json:"template_vm"`   // nombre VM plantilla
	TemplateDisk string `json:"template_disk"` // ruta al disco multiconexión
	TemplateIP   string `json:"template_ip"`   // IP fija de la VM plantilla con Apache (host-only)
	VMFolder     string `json:"vm_folder"`     // donde crear VMs nuevas
	MemoryMB     int    `json:"memory_mb"`     // RAM por instancia
	CPUs         int    `json:"cpus"`          // CPUs por instancia

	// VMNameFilter es un filtro opcional (prefijo o substring) para limitar
	// qué VMs aparecen en el dashboard. Vacío = mostrar todas.
	VMNameFilter string `json:"vm_name_filter"`
}

type SSHConfig struct {
	User       string `json:"user"`
	KeyPath    string `json:"key_path"`    // llave privada SSH
	Port       int    `json:"port"`
	TimeoutSec int    `json:"timeout_sec"`
}

// Default devuelve la configuración por defecto.
func Default() *Config {
	home, _ := os.UserHomeDir()
	wd, _ := os.Getwd()
	return &Config{
		HTTP: HTTPConfig{Addr: "0.0.0.0:8080"},
		DNS: DNSConfig{
			Domain:   "cloud.local.",
			ServerIP: "192.168.56.10",
			ZoneFile: "/etc/bind/zones/db.cloud.local",
			TSIGKey:  filepath.Join(wd, "data/dnskey.conf"),
			TSIGName: "httpaas-key",
		},
		Network: NetworkConfig{
			HostOnlyNet:  "vboxnet0",
			Subnet:       "192.168.56.0/24",
			Gateway:      "192.168.56.1",
			IPRangeStart: "192.168.56.100",
			IPRangeEnd:   "192.168.56.200",
		},
		VirtualBox: VBoxConfig{
			ManageBin:    defaultVBoxManage(),
			TemplateVM:   "httpaas-template",
			TemplateDisk: filepath.Join(home, "VirtualBox VMs/httpaas-template/httpaas-template.vdi"),
			TemplateIP:   "192.168.56.20",
			VMFolder:     filepath.Join(home, "VirtualBox VMs"),
			MemoryMB:     768,
			CPUs:         1,
		},
		SSH: SSHConfig{
			User:       "root",
			KeyPath:    `C:\temp\httpaas-dns-key`,
			Port:       22,
			TimeoutSec: 90,
		},
		UploadDir:   filepath.Join(wd, "uploads"),
		DataDir:     filepath.Join(wd, "data"),
		TemplateDir: filepath.Join(wd, "templates"),
		StaticDir:   filepath.Join(wd, "static"),
		ScriptsDir:  filepath.Join(wd, "..", "scripts"),
		DemoMode:    false,
	}
}

// Load carga la configuración desde el archivo apuntado por la variable
// HTTPAAS_CONFIG, o desde ./config.json si la variable no está. Si no
// existe el archivo, escribe la configuración por defecto y la devuelve.
func Load() (*Config, error) {
	path := os.Getenv("HTTPAAS_CONFIG")
	if path == "" {
		path = "config.json"
	}

	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Persistir la configuración por defecto para que el usuario la edite.
			out, _ := json.MarshalIndent(cfg, "", "  ")
			if werr := os.WriteFile(path, out, 0o644); werr != nil {
				return nil, fmt.Errorf("escribiendo config por defecto: %w", werr)
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("leyendo config: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parseando config: %w", err)
	}
	return cfg, nil
}
