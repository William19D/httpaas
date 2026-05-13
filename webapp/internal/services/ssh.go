package services

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/cloud-uq/httpaas/internal/config"
)

// SSHService encapsula operaciones SSH/SCP contra las instancias usando
// los binarios `ssh` y `scp` del sistema. Esta aproximación tiene tres
// ventajas sobre embeber una librería SSH:
//
//  1. Cero dependencias externas en el módulo Go.
//  2. El usuario puede reproducir manualmente cualquier comando que falle.
//  3. Heredamos automáticamente el config de ~/.ssh.
//
// El precio es que asume que `ssh` y `scp` están instalados. Es razonable
// en cualquier máquina anfitriona Linux/macOS, y en Windows con OpenSSH.
type SSHService struct {
	cfg *config.Config
}

func NewSSHService(cfg *config.Config) *SSHService {
	return &SSHService{cfg: cfg}
}

// CheckInstalled valida que ssh y scp estén disponibles.
func (s *SSHService) CheckInstalled() error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh no está instalado en el host")
	}
	if _, err := exec.LookPath("scp"); err != nil {
		return fmt.Errorf("scp no está instalado en el host")
	}
	return nil
}

// commonArgs devuelve los flags comunes para ssh/scp.
//   - StrictHostKeyChecking=no porque las instancias son efímeras.
//   - UserKnownHostsFile=/dev/null evita ensuciar el known_hosts.
//   - LogLevel=ERROR silencia warnings inocuos en stderr.
//   - BatchMode=yes hace que falle rápido si requiere contraseña.
func (s *SSHService) commonArgs() []string {
	return []string{
		"-i", s.cfg.SSH.KeyPath,
		"-p", fmt.Sprintf("%d", s.cfg.SSH.Port),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", fmt.Sprintf("ConnectTimeout=%d", s.cfg.SSH.TimeoutSec),
		"-o", "BatchMode=yes",
	}
}

// WaitForSSH espera a que el servicio SSH conteste con un comando trivial.
func (s *SSHService) WaitForSSH(host string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		_, _, err := s.runOnce(host, "true", 8*time.Second)
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("timeout esperando SSH en %s: %w", host, lastErr)
}

// Run ejecuta un comando remoto en host y devuelve stdout, stderr y error.
func (s *SSHService) Run(host, cmd string) (string, string, error) {
	return s.runOnce(host, cmd, time.Duration(s.cfg.SSH.TimeoutSec)*time.Second)
}

func (s *SSHService) runOnce(host, cmd string, timeout time.Duration) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := s.commonArgs()
	args = append(args, s.cfg.SSH.User+"@"+host, "--", cmd)

	c := exec.CommandContext(ctx, "ssh", args...)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()
	if err != nil {
		return stdout.String(), stderr.String(),
			fmt.Errorf("ssh %s: %w | stderr: %s", host, err, stderr.String())
	}
	return stdout.String(), stderr.String(), nil
}

// UploadFile copia un archivo local a una ruta remota usando scp.
func (s *SSHService) UploadFile(host, localPath, remotePath string) error {
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(s.cfg.SSH.TimeoutSec*4)*time.Second)
	defer cancel()

	// scp usa -P (mayúscula) para el puerto, a diferencia de ssh.
	args := []string{
		"-i", s.cfg.SSH.KeyPath,
		"-P", fmt.Sprintf("%d", s.cfg.SSH.Port),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "BatchMode=yes",
		localPath,
		fmt.Sprintf("%s@%s:%s", s.cfg.SSH.User, host, remotePath),
	}
	c := exec.CommandContext(ctx, "scp", args...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("scp %s -> %s:%s falló: %w | stderr: %s",
			localPath, host, remotePath, err, stderr.String())
	}
	return nil
}

// shellQuote escapa una cadena para uso seguro como argumento sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
