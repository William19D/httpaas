package models

import "time"

// State es el estado del ciclo de vida de una instancia.
type State string

const (
	StatePending      State = "pending"
	StateProvisioning State = "provisioning"
	StateRunning      State = "running"
	StateDeploying    State = "deploying"
	StateFailed       State = "failed"
	StateStopped      State = "stopped"
	StateDeleting     State = "deleting"
)

// Instance representa una instancia HTTP aprovisionada (o una VM importada del inventario).
type Instance struct {
	ID          string    `json:"id"`
	Hostname    string    `json:"hostname"`     // nombre corto, p. ej. "site42"
	FQDN        string    `json:"fqdn"`         // hostname + dominio
	IP          string    `json:"ip"`           // IP asignada
	State       State     `json:"state"`        // estado de alto nivel (running/stopped/...)
	VBoxState   string    `json:"vbox_state"`   // estado crudo de VirtualBox (poweroff, saved, paused...)
	URL         string    `json:"url"`          // http://hostname.cloud.local/
	VMName      string    `json:"vm_name"`      // nombre interno VirtualBox
	VMUUID      string    `json:"vm_uuid"`      // UUID en VirtualBox
	OSType      string    `json:"os_type"`      // p. ej. "Debian (64-bit)"
	MemoryMB    int       `json:"memory_mb"`
	CPUs        int       `json:"cpus"`
	Managed     bool      `json:"managed"`      // true = aprovisionada por HTTPaaS; false = sólo inventario
	SitePort    int       `json:"site_port,omitempty"` // puerto local del servidor estático (Managed)
	UploadName  string    `json:"upload_name"`  // nombre del .zip subido
	Owner       string    `json:"owner"`        // identificador opcional del usuario
	Description string    `json:"description"`
	Logs        []LogLine `json:"logs"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastError   string    `json:"last_error,omitempty"`
}

// LogLine es una línea del log de aprovisionamiento por instancia.
type LogLine struct {
	Timestamp time.Time `json:"t"`
	Level     string    `json:"lvl"`
	Message   string    `json:"msg"`
}

// AppendLog agrega una entrada al log de la instancia (manteniendo un tope razonable).
func (i *Instance) AppendLog(level, msg string) {
	i.Logs = append(i.Logs, LogLine{
		Timestamp: time.Now(),
		Level:     level,
		Message:   msg,
	})
	// Mantenemos los últimos 200 mensajes para que no crezca sin límite.
	if len(i.Logs) > 200 {
		i.Logs = i.Logs[len(i.Logs)-200:]
	}
	i.UpdatedAt = time.Now()
}
