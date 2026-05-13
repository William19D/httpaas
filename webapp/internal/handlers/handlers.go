package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloud-uq/httpaas/internal/config"
	"github.com/cloud-uq/httpaas/internal/services"
	"github.com/cloud-uq/httpaas/internal/util"
)

// Handler agrupa dependencias compartidas entre rutas.
type Handler struct {
	cfg          *config.Config
	store        *services.InstanceStore
	orchestrator *services.Orchestrator
	tpl          map[string]*template.Template
}

func New(cfg *config.Config, store *services.InstanceStore,
	orch *services.Orchestrator) *Handler {
	h := &Handler{cfg: cfg, store: store, orchestrator: orch}
	h.loadTemplates()
	return h
}

// loadTemplates parsea cada página junto con el layout en un set independiente.
// Cada página define el bloque "body" que el layout incrusta. Esto evita el
// problema clásico de ParseGlob donde el último define "body" cargado pisa
// a los anteriores.
func (h *Handler) loadTemplates() {
	stateLabels := map[string]string{
		"pending":      "EN COLA",
		"provisioning": "INICIANDO",
		"deploying":    "DESPLEGANDO",
		"running":      "ONLINE",
		"failed":       "ERROR",
		"stopped":      "APAGADA",
		"deleting":     "BORRANDO",
	}
	vboxLabels := map[string]string{
		"running":        "ONLINE",
		"starting":       "INICIANDO",
		"restoring":      "RESTAURANDO",
		"saving":         "GUARDANDO",
		"poweroff":       "APAGADA",
		"saved":          "SUSPENDIDA",
		"paused":         "PAUSADA",
		"stopping":       "DETENIENDO",
		"aborted":        "ABORTADA",
		"gurumeditation": "ABORTADA",
	}
	vboxClasses := map[string]string{
		"running":        "state-running",
		"starting":       "state-provisioning",
		"restoring":      "state-provisioning",
		"saving":         "state-provisioning",
		"stopping":       "state-provisioning",
		"poweroff":       "state-stopped",
		"saved":          "state-stopped",
		"paused":         "state-stopped",
		"aborted":        "state-failed",
		"gurumeditation": "state-failed",
	}
	funcs := template.FuncMap{
		"upper": strings.ToUpper,
		"timeAgo": func(t time.Time) string {
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return fmt.Sprintf("hace %ds", int(d.Seconds()))
			case d < time.Hour:
				return fmt.Sprintf("hace %dm", int(d.Minutes()))
			case d < 24*time.Hour:
				return fmt.Sprintf("hace %dh", int(d.Hours()))
			default:
				return t.Format("2006-01-02 15:04")
			}
		},
		"shorten": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "…"
		},
		"stateClass": func(state string) string {
			return "state-" + state
		},
		"stateLabel": func(state string) string {
			if v, ok := stateLabels[state]; ok {
				return v
			}
			return strings.ToUpper(state)
		},
		"vboxLabel": func(s string) string {
			if v, ok := vboxLabels[s]; ok {
				return v
			}
			if s == "" {
				return "—"
			}
			return strings.ToUpper(s)
		},
		"vboxClass": func(s string) string {
			if v, ok := vboxClasses[s]; ok {
				return v
			}
			return "state-stopped"
		},
	}

	pages := []string{"dashboard.html", "provision.html", "instance.html"}
	h.tpl = make(map[string]*template.Template, len(pages))
	layout := filepath.Join(h.cfg.TemplateDir, "layout.html")
	for _, p := range pages {
		t, err := template.New("layout").Funcs(funcs).ParseFiles(
			layout, filepath.Join(h.cfg.TemplateDir, p))
		if err != nil {
			log.Fatalf("parseando %s: %v", p, err)
		}
		h.tpl[p] = t
	}
}

// render ejecuta una plantilla con un layout base.
func (h *Handler) render(w http.ResponseWriter, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["Domain"] = strings.TrimSuffix(h.cfg.DNS.Domain, ".")
	data["DNSServer"] = h.cfg.DNS.ServerIP
	t, ok := h.tpl[name]
	if !ok {
		http.Error(w, "vista desconocida: "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("template %s: %v", name, err)
	}
}

// Dashboard - GET /
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	instances := h.store.All()
	healthErr := ""
	if err := h.orchestrator.HealthCheck(); err != nil {
		healthErr = err.Error()
	}

	running, failed, total := 0, 0, len(instances)
	for _, inst := range instances {
		if string(inst.State) == "running" {
			running++
		}
		if string(inst.State) == "failed" {
			failed++
		}
	}

	h.render(w, "dashboard.html", map[string]any{
		"Title":     "Dashboard",
		"Instances": instances,
		"HealthErr": healthErr,
		"Stats": map[string]int{
			"total":   total,
			"running": running,
			"failed":  failed,
		},
	})
}

// ProvisionForm - GET /provision
func (h *Handler) ProvisionForm(w http.ResponseWriter, r *http.Request) {
	h.render(w, "provision.html", map[string]any{"Title": "Nueva instancia"})
}

// ProvisionSubmit - POST /provision
func (h *Handler) ProvisionSubmit(w http.ResponseWriter, r *http.Request) {
	// Tamaño máximo del upload: 50 MB.
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		http.Error(w, "archivo demasiado grande o formulario inválido: "+err.Error(),
			http.StatusBadRequest)
		return
	}
	hostname := strings.ToLower(strings.TrimSpace(r.FormValue("hostname")))
	desc := strings.TrimSpace(r.FormValue("description"))
	owner := strings.TrimSpace(r.FormValue("owner"))

	file, header, err := r.FormFile("zip")
	if err != nil {
		http.Error(w, "archivo zip requerido", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") {
		http.Error(w, "el archivo debe ser .zip", http.StatusBadRequest)
		return
	}

	// Persistir el zip antes de iniciar el aprovisionamiento.
	tmpName := fmt.Sprintf("%s-%s.zip", time.Now().Format("20060102-150405"), util.NewUUID()[:8])
	dstPath := filepath.Join(h.cfg.UploadDir, tmpName)
	out, err := os.Create(dstPath)
	if err != nil {
		http.Error(w, "no se pudo guardar el archivo", http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		http.Error(w, "error escribiendo zip", http.StatusInternalServerError)
		return
	}
	out.Close()

	inst, err := h.orchestrator.Provision(services.ProvisionRequest{
		Hostname:    hostname,
		Description: desc,
		Owner:       owner,
		ZipPath:     dstPath,
		ZipName:     header.Filename,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/instances/"+inst.ID, http.StatusSeeOther)
}

// DeleteInstance - POST /instances/{id}/delete
func (h *Handler) DeleteInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	go func() {
		if err := h.orchestrator.Delete(id); err != nil {
			log.Printf("Delete %s: %v", id, err)
		}
	}()
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// RestartInstance - POST /instances/{id}/restart
func (h *Handler) RestartInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	go func() {
		if err := h.orchestrator.Restart(id); err != nil {
			log.Printf("Restart %s: %v", id, err)
		}
	}()
	http.Redirect(w, r, "/instances/"+id, http.StatusSeeOther)
}

// StartInstance - POST /instances/{id}/start
func (h *Handler) StartInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	go func() {
		if err := h.orchestrator.StartInstance(id); err != nil {
			log.Printf("Start %s: %v", id, err)
		}
	}()
	http.Redirect(w, r, "/instances/"+id, http.StatusSeeOther)
}

// StopInstance - POST /instances/{id}/stop
func (h *Handler) StopInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	go func() {
		if err := h.orchestrator.StopInstance(id); err != nil {
			log.Printf("Stop %s: %v", id, err)
		}
	}()
	http.Redirect(w, r, "/instances/"+id, http.StatusSeeOther)
}

// InstanceDetail - GET /instances/{id}
func (h *Handler) InstanceDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inst := h.store.Get(id)
	if inst == nil {
		http.NotFound(w, r)
		return
	}
	h.render(w, "instance.html", map[string]any{
		"Title":      inst.Hostname,
		"Instance":   inst,
		"HideTopbar": true,
	})
}

// APIListInstances - GET /api/instances (JSON)
func (h *Handler) APIListInstances(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.store.All())
}

// APIInstanceStatus - GET /api/instances/{id}/status (JSON, para polling del dashboard)
func (h *Handler) APIInstanceStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inst := h.store.Get(id)
	if inst == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(inst)
}

// Health - GET /health
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"status": "ok"}
	if err := h.orchestrator.HealthCheck(); err != nil {
		out["status"] = "degraded"
		out["error"] = err.Error()
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
