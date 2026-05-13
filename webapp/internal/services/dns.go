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

// DNSService realiza actualizaciones dinámicas contra el servidor Bind9
// usando la herramienta nsupdate, autenticando con una clave TSIG.
type DNSService struct {
	cfg *config.Config
}

func NewDNSService(cfg *config.Config) *DNSService {
	return &DNSService{cfg: cfg}
}

// CheckInstalled valida que nsupdate y dig estén disponibles.
func (d *DNSService) CheckInstalled() error {
	if _, err := exec.LookPath("nsupdate"); err != nil {
		return fmt.Errorf("nsupdate no está instalado (instale bind9-dnsutils)")
	}
	return nil
}

// AddRecord crea un registro A directo y su PTR inverso para hostname → ip.
// Sustituye cualquier registro previo del mismo nombre/IP.
func (d *DNSService) AddRecord(hostname, ip string) error {
	zone := d.cfg.DNS.Domain // ya termina en "."
	fqdn := hostname + "." + zone

	// Forward A record (zona directa)
	fwd := fmt.Sprintf(`server %s
zone %s
update delete %s A
update add %s 300 A %s
send
`, d.cfg.DNS.ServerIP, zone, fqdn, fqdn, ip)
	if err := d.runNsupdate(fwd); err != nil {
		return err
	}

	// Reverse PTR (zona inversa) — best-effort, no falla la operación
	if revZone, revName, ok := reverseFor(ip); ok {
		rev := fmt.Sprintf(`server %s
zone %s
update delete %s PTR
update add %s 300 PTR %s
send
`, d.cfg.DNS.ServerIP, revZone, revName, revName, fqdn)
		_ = d.runNsupdate(rev)
	}
	return nil
}

// RemoveRecord borra el registro A (y su PTR si conocemos la IP).
func (d *DNSService) RemoveRecord(hostname string) error {
	zone := d.cfg.DNS.Domain
	fqdn := hostname + "." + zone
	cmds := fmt.Sprintf(`server %s
zone %s
update delete %s A
send
`, d.cfg.DNS.ServerIP, zone, fqdn)
	return d.runNsupdate(cmds)
}

// reverseFor calcula la zona inversa /24 y el nombre PTR para una IP IPv4.
// Devuelve ok=false si la IP no es válida o no está en 192.168.56.0/24.
func reverseFor(ip string) (zone, name string, ok bool) {
	var a, b, c, dOct int
	if _, err := fmt.Sscanf(ip, "%d.%d.%d.%d", &a, &b, &c, &dOct); err != nil {
		return "", "", false
	}
	zone = fmt.Sprintf("%d.%d.%d.in-addr.arpa.", c, b, a)
	name = fmt.Sprintf("%d.%s", dOct, zone)
	return zone, name, true
}

// runNsupdate alimenta los comandos a nsupdate por stdin. Si hay una clave TSIG
// configurada, la usa para autenticarse; si no, intenta sin clave (Bind tiene
// que permitir update desde la IP del host).
func (d *DNSService) runNsupdate(commands string) error {
	args := []string{}
	if d.cfg.DNS.TSIGKey != "" {
		args = append(args, "-k", d.cfg.DNS.TSIGKey)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "nsupdate", args...)
	cmd.Stdin = strings.NewReader(commands)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nsupdate falló: %w | stderr: %s", err, stderr.String())
	}
	return nil
}

// Lookup resuelve un hostname contra el servidor DNS y devuelve la IP.
// Útil para verificación.
func (d *DNSService) Lookup(hostname string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fqdn := hostname + "." + d.cfg.DNS.Domain
	cmd := exec.CommandContext(ctx, "dig", "+short",
		"@"+d.cfg.DNS.ServerIP, fqdn, "A")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Ping verifica que el servidor DNS responde.
func (d *DNSService) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dig", "+short", "+timeout=2", "+tries=1",
		"@"+d.cfg.DNS.ServerIP, d.cfg.DNS.Domain, "SOA")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("dig SOA falló contra %s: %w", d.cfg.DNS.ServerIP, err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return fmt.Errorf("servidor DNS no devolvió SOA para %s", d.cfg.DNS.Domain)
	}
	return nil
}
