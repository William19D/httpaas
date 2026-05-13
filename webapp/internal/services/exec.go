package services

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

// vboxBinPath se inicializa al arrancar la app con la ruta de VBoxManage.
// Permite que helpers fuera de VBoxService la usen sin pasar el config.
var vboxBinPath atomic.Value // string

// SetVBoxBin guarda la ruta absoluta del binario VBoxManage para los helpers.
func SetVBoxBin(p string) {
	if p == "" {
		p = "VBoxManage"
	}
	vboxBinPath.Store(p)
}

func getVBoxBin() string {
	if v, ok := vboxBinPath.Load().(string); ok && v != "" {
		return v
	}
	return "VBoxManage"
}

// execVBox ejecuta VBoxManage como helper para llamadas desde el orquestador
// que no quieren depender del struct VBoxService.
func execVBox(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, getVBoxBin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf(
			"VBoxManage %s: %w | stderr: %s",
			strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}
