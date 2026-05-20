package models

import (
	"fmt"
	"time"
)

// DBEngine identifica el motor de base de datos de la instancia.
type DBEngine string

const (
	DBEngineMariaDB    DBEngine = "mariadb"
	DBEnginePostgreSQL DBEngine = "postgresql"
)

// DBInstance representa una instancia de base de datos aprovisionada sobre VirtualBox.
// Cada instancia tiene su propia VM clonada desde la plantilla correspondiente.
type DBInstance struct {
	ID        string   `json:"id"`
	VMName    string   `json:"vm_name"`   // nombre en VirtualBox
	VMUUID    string   `json:"vm_uuid"`   // UUID en VirtualBox
	IP        string   `json:"ip"`        // IP host-only asignada
	State     State    `json:"state"`     // estado lógico (running/stopped/...)
	VBoxState string   `json:"vbox_state"` // estado crudo de VirtualBox

	// Datos de la base de datos
	DBName     string   `json:"db_name"`   // nombre de la base de datos
	DBUser     string   `json:"db_user"`   // usuario administrador de la BD
	DBPassword string   `json:"db_password"` // contraseña generada aleatoriamente
	DBEngine   DBEngine `json:"db_engine"` // mariadb | postgresql
	DBPort     int      `json:"db_port"`   // 3306 (MariaDB) o 5432 (PostgreSQL)
	SQLFile    string   `json:"sql_file"`  // nombre del archivo .sql ejecutado al crear

	// Diagnóstico y ciclo de vida
	Logs      []LogLine `json:"logs"`
	LastError string    `json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AppendLog agrega una entrada al log (máx. 200 líneas).
func (d *DBInstance) AppendLog(level, msg string) {
	d.Logs = append(d.Logs, LogLine{
		Timestamp: time.Now(),
		Level:     level,
		Message:   msg,
	})
	if len(d.Logs) > 200 {
		d.Logs = d.Logs[len(d.Logs)-200:]
	}
	d.UpdatedAt = time.Now()
}

// EngineLabel devuelve el nombre legible del motor.
func (d *DBInstance) EngineLabel() string {
	switch d.DBEngine {
	case DBEngineMariaDB:
		return "MariaDB"
	case DBEnginePostgreSQL:
		return "PostgreSQL"
	default:
		return string(d.DBEngine)
	}
}

// CLICommand devuelve el comando de conexión para usar en la terminal.
func (d *DBInstance) CLICommand() string {
	switch d.DBEngine {
	case DBEngineMariaDB:
		return fmt.Sprintf("mariadb -h %s -u %s -p'%s' %s", d.IP, d.DBUser, d.DBPassword, d.DBName)
	default: // postgresql
		return fmt.Sprintf("psql -h %s -U %s -d %s", d.IP, d.DBUser, d.DBName)
	}
}

// JDBCUrl devuelve la URL JDBC para DBeaver/clientes Java.
func (d *DBInstance) JDBCUrl() string {
	switch d.DBEngine {
	case DBEngineMariaDB:
		return fmt.Sprintf("jdbc:mariadb://%s:%d/%s", d.IP, d.DBPort, d.DBName)
	default: // postgresql
		return fmt.Sprintf("jdbc:postgresql://%s:%d/%s", d.IP, d.DBPort, d.DBName)
	}
}
