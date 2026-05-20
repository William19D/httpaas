package services

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloud-uq/httpaas/internal/config"
	"github.com/cloud-uq/httpaas/internal/models"
	"github.com/cloud-uq/httpaas/internal/util"
)

// DBOrchestrator coordina el ciclo de vida de las instancias de bases de datos:
// aprovisionamiento (clonar VM, configurar red, arrancar, crear BD vía SSH),
// eliminación y consulta de estado.
type DBOrchestrator struct {
	cfg     *config.Config
	store   *DBInstanceStore
	main    *InstanceStore // para compartir el pool de IPs con instancias web
	vbox    *VBoxService
	dns     *DNSService
	ssh     *SSHService
}

// NewDBOrchestrator construye el orquestador de BD.
func NewDBOrchestrator(
	cfg *config.Config,
	store *DBInstanceStore,
	main *InstanceStore,
	vbox *VBoxService,
	dns *DNSService,
	ssh *SSHService,
) *DBOrchestrator {
	return &DBOrchestrator{cfg: cfg, store: store, main: main, vbox: vbox, dns: dns, ssh: ssh}
}

// DBProvisionRequest describe los datos que el usuario envía para crear una BD.
type DBProvisionRequest struct {
	DBName   string
	DBUser   string
	DBEngine models.DBEngine
	SQLPath  string // ruta local del .sql subido (vacía si no se adjuntó)
	SQLName  string // nombre original del archivo .sql
}

// Provision inicia el aprovisionamiento de una nueva instancia de BD de forma asíncrona.
// Devuelve la instancia inmediatamente (en estado pending) para que el handler pueda
// redirigir al usuario sin esperar los ~5 minutos del boot completo.
func (o *DBOrchestrator) Provision(req DBProvisionRequest) (*models.DBInstance, error) {
	if req.DBName == "" || req.DBUser == "" {
		return nil, fmt.Errorf("nombre de BD y usuario son obligatorios")
	}
	if req.DBEngine != models.DBEngineMariaDB && req.DBEngine != models.DBEnginePostgreSQL {
		return nil, fmt.Errorf("motor no válido: %s (use mariadb o postgresql)", req.DBEngine)
	}

	password, err := generatePassword(16)
	if err != nil {
		return nil, fmt.Errorf("generando contraseña: %w", err)
	}

	// Asignar IP del pool compartido (excluye IPs web + BD ya existentes).
	ip, err := o.store.AllocateIP(
		o.cfg.Network.IPRangeStart,
		o.cfg.Network.IPRangeEnd,
		o.main.UsedIPs(),
	)
	if err != nil {
		return nil, err
	}

	id := util.NewUUID()
	vmName := fmt.Sprintf("httpaas-db-%s-%s", req.DBName, id[:8])
	port := dbPort(req.DBEngine)

	inst := &models.DBInstance{
		ID:         id,
		VMName:     vmName,
		IP:         ip,
		State:      models.StatePending,
		VBoxState:  "starting",
		DBName:     req.DBName,
		DBUser:     req.DBUser,
		DBPassword: password,
		DBEngine:   req.DBEngine,
		DBPort:     port,
		SQLFile:    req.SQLName,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	inst.AppendLog("info", fmt.Sprintf("Solicitud: BD=%s usuario=%s motor=%s",
		req.DBName, req.DBUser, inst.EngineLabel()))
	inst.AppendLog("info", "IP asignada: "+ip)

	if err := o.store.Put(inst); err != nil {
		return nil, err
	}

	go o.runProvision(inst, req.SQLPath)
	return inst, nil
}

// runProvision ejecuta el flujo completo de aprovisionamiento en una goroutine.
// Si cualquier paso falla, marca la instancia como StateFailed y registra el error.
func (o *DBOrchestrator) runProvision(inst *models.DBInstance, sqlPath string) {
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

	// 1. Seleccionar disco plantilla según el motor.
	templateDisk := o.templateDisk(inst.DBEngine)
	if templateDisk == "" {
		fail(fmt.Errorf("disco plantilla para %s no configurado en config.json", inst.EngineLabel()))
		return
	}

	mark(models.StateProvisioning, fmt.Sprintf("Creando VM %s (motor: %s)", inst.VMName, inst.EngineLabel()))

	// 2. Crear la VM con el disco multiconexión de la plantilla correcta.
	mac := GenerateMAC(inst.ID)
	if err := o.vbox.CreateDBInstance(inst.VMName, mac, templateDisk); err != nil {
		fail(fmt.Errorf("crear VM: %w", err))
		return
	}
	inst.AppendLog("info", "VM creada con disco multiconexión: "+filepath.Base(templateDisk))

	// 3. Inyectar guest properties para el script first-boot.
	domain := strings.TrimSuffix(o.cfg.DNS.Domain, ".")
	props := map[string]string{
		"/HTTPaaS/hostname": inst.VMName,
		"/HTTPaaS/fqdn":     inst.VMName + "." + domain,
		"/HTTPaaS/ip":       inst.IP,
		"/HTTPaaS/netmask":  "255.255.255.0",
		"/HTTPaaS/gateway":  o.cfg.Network.Gateway,
		"/HTTPaaS/dns":      o.cfg.DNS.ServerIP,
		"/HTTPaaS/engine":   string(inst.DBEngine),
	}
	for k, v := range props {
		if err := o.vbox.SetGuestProperty(inst.VMName, k, v); err != nil {
			inst.AppendLog("warn", fmt.Sprintf("guest property %s: %v", k, err))
		}
	}
	inst.AppendLog("info", fmt.Sprintf("Guest properties inyectadas (IP=%s, GW=%s)",
		inst.IP, o.cfg.Network.Gateway))

	// 4. Arrancar la VM en modo headless.
	mark(models.StateProvisioning, "Arrancando VM en modo headless…")
	if err := o.vbox.StartVM(inst.VMName); err != nil {
		fail(fmt.Errorf("arrancar VM: %w", err))
		return
	}

	// 5. Esperar a que VirtualBox reporte estado "running".
	if err := o.vbox.WaitForVMRunning(inst.VMName, 2*time.Minute); err != nil {
		fail(fmt.Errorf("esperando running: %w", err))
		return
	}
	inst.AppendLog("info", "VM en estado running")

	// 6. Esperar a que el SSH esté disponible (el first-boot puede tardar hasta 5 min).
	mark(models.StateDeploying, "Esperando SSH (first-boot puede tardar ~3-5 min)…")
	if err := o.ssh.WaitForSSH(inst.IP, 8*time.Minute); err != nil {
		fail(fmt.Errorf("esperar SSH: %w", err))
		return
	}
	inst.AppendLog("info", "SSH disponible en "+inst.IP)

	// 7. Configurar la base de datos, el usuario y los privilegios.
	mark(models.StateDeploying, fmt.Sprintf("Creando base de datos %q y usuario %q…", inst.DBName, inst.DBUser))
	if err := o.setupDatabase(inst); err != nil {
		fail(fmt.Errorf("configurar BD: %w", err))
		return
	}
	inst.AppendLog("info", fmt.Sprintf("BD %q y usuario %q creados con privilegios totales",
		inst.DBName, inst.DBUser))

	// 8. Ejecutar el archivo .sql del usuario (si se adjuntó).
	if sqlPath != "" {
		mark(models.StateDeploying, "Subiendo y ejecutando archivo SQL…")
		if err := o.runSQLFile(inst, sqlPath); err != nil {
			// No es fatal: la instancia queda en pie con la BD vacía.
			inst.AppendLog("warn", "Error ejecutando SQL: "+err.Error())
		} else {
			inst.AppendLog("info", "Archivo SQL ejecutado correctamente")
		}
	}

	// 9. Registrar la instancia en DNS.
	mark(models.StateDeploying, fmt.Sprintf("Registrando DNS: %s → %s", inst.VMName, inst.IP))
	if err := o.dns.AddRecord(inst.VMName, inst.IP); err != nil {
		inst.AppendLog("warn", "DNS: "+err.Error())
	} else {
		inst.AppendLog("info", "Registro DNS creado")
	}

	// Obtener UUID de la VM una vez aprovisionada.
	if info, err := o.vbox.VMInfo(inst.VMName); err == nil {
		inst.VMUUID = info.UUID
	}

	inst.VBoxState = "running"
	mark(models.StateRunning, fmt.Sprintf("Instancia lista — %s:%d (usuario: %s)",
		inst.IP, inst.DBPort, inst.DBUser))
}

// setupDatabase crea la BD, el usuario y otorga todos los privilegios vía SSH.
func (o *DBOrchestrator) setupDatabase(inst *models.DBInstance) error {
	var cmds []string
	switch inst.DBEngine {
	case models.DBEngineMariaDB:
		// Nota: los nombres de BD y usuario ya están validados (solo a-z, 0-9, _),
		// por lo que no es necesario entrecomillar con backticks en el SQL.
		cmds = []string{
			fmt.Sprintf("mysql -u root -e \"CREATE DATABASE IF NOT EXISTS %s;\"",
				inst.DBName),
			fmt.Sprintf("mysql -u root -e \"CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s';\"",
				inst.DBUser, inst.DBPassword),
			fmt.Sprintf("mysql -u root -e \"GRANT ALL PRIVILEGES ON %s.* TO '%s'@'%%'; FLUSH PRIVILEGES;\"",
				inst.DBName, inst.DBUser),
		}
	case models.DBEnginePostgreSQL:
		cmds = []string{
			fmt.Sprintf(`psql -U postgres -c "CREATE DATABASE \"%s\";" || true`, inst.DBName),
			fmt.Sprintf(`psql -U postgres -c "CREATE USER \"%s\" WITH PASSWORD '%s';" || true`,
				inst.DBUser, inst.DBPassword),
			fmt.Sprintf(`psql -U postgres -c "GRANT ALL PRIVILEGES ON DATABASE \"%s\" TO \"%s\";"`,
				inst.DBName, inst.DBUser),
			// PostgreSQL 15+: también hay que otorgar schema public
			fmt.Sprintf(`psql -U postgres -d "%s" -c "GRANT ALL ON SCHEMA public TO \"%s\";" || true`,
				inst.DBName, inst.DBUser),
		}
	default:
		return fmt.Errorf("motor desconocido: %s", inst.DBEngine)
	}

	for _, cmd := range cmds {
		if _, stderr, err := o.ssh.Run(inst.IP, cmd); err != nil {
			return fmt.Errorf("comando BD falló (%s): %w | stderr: %s", cmd, err, stderr)
		}
	}
	return nil
}

// runSQLFile sube el archivo .sql a la VM y lo ejecuta contra la BD creada.
func (o *DBOrchestrator) runSQLFile(inst *models.DBInstance, sqlPath string) error {
	remotePath := "/tmp/init_" + inst.ID[:8] + ".sql"

	if err := o.ssh.UploadFile(inst.IP, sqlPath, remotePath); err != nil {
		return fmt.Errorf("scp .sql: %w", err)
	}

	var execCmd string
	switch inst.DBEngine {
	case models.DBEngineMariaDB:
		execCmd = fmt.Sprintf("mysql -u root %s < %s && rm -f %s",
			inst.DBName, remotePath, remotePath)
	case models.DBEnginePostgreSQL:
		execCmd = fmt.Sprintf("psql -U postgres -d %s < %s && rm -f %s",
			inst.DBName, remotePath, remotePath)
	}

	if _, stderr, err := o.ssh.Run(inst.IP, execCmd); err != nil {
		return fmt.Errorf("ejecutar SQL: %w | stderr: %s", err, stderr)
	}
	return nil
}

// Delete apaga y elimina la VM de una instancia de BD. También borra el registro DNS.
func (o *DBOrchestrator) Delete(id string) error {
	inst := o.store.Get(id)
	if inst == nil {
		return fmt.Errorf("instancia %s no encontrada", id)
	}

	inst.State = models.StateDeleting
	inst.AppendLog("info", "Eliminando instancia de BD")
	_ = o.store.Save()

	// Apagar la VM si está corriendo.
	if state, _ := o.vbox.VMState(inst.VMName); state == "running" {
		inst.AppendLog("info", "Apagando VM…")
		_ = o.vbox.AcpiShutdown(inst.VMName)
		time.Sleep(5 * time.Second)
		_ = o.vbox.PowerOffVM(inst.VMName)
	}

	// Desregistrar y eliminar archivos de la VM.
	if err := o.vbox.UnregisterVM(inst.VMName); err != nil {
		inst.AppendLog("warn", "UnregisterVM: "+err.Error())
	} else {
		inst.AppendLog("info", "VM eliminada de VirtualBox")
	}

	// Eliminar registro DNS.
	if err := o.dns.RemoveRecord(inst.VMName); err != nil {
		inst.AppendLog("warn", "DNS remove: "+err.Error())
	}

	return o.store.Delete(id)
}

// SyncVBoxState actualiza el VBoxState de todas las instancias de BD consultando VirtualBox.
// Se llama periódicamente desde main para mantener el dashboard actualizado.
func (o *DBOrchestrator) SyncVBoxState() {
	for _, inst := range o.store.All() {
		if inst.State == models.StateDeleting || inst.State == models.StatePending {
			continue
		}
		state, err := o.vbox.VMState(inst.VMName)
		if err != nil {
			continue
		}
		if inst.VBoxState != state {
			inst.VBoxState = state
			inst.State = MapVBoxState(state)
			inst.UpdatedAt = time.Now()
			_ = o.store.Save()
		}
	}
}

// --- Helpers ----------------------------------------------------------------

// templateDisk devuelve la ruta del disco multiconexión según el motor.
func (o *DBOrchestrator) templateDisk(engine models.DBEngine) string {
	switch engine {
	case models.DBEngineMariaDB:
		return o.cfg.VirtualBox.MariaDBTemplateDisk
	case models.DBEnginePostgreSQL:
		return o.cfg.VirtualBox.PostgreSQLTemplateDisk
	}
	return ""
}

// dbPort devuelve el puerto estándar del motor.
func dbPort(engine models.DBEngine) int {
	switch engine {
	case models.DBEngineMariaDB:
		return 3306
	case models.DBEnginePostgreSQL:
		return 5432
	}
	return 0
}

// generatePassword produce una contraseña hexadecimal aleatoria de n bytes (2n caracteres).
func generatePassword(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
