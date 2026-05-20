// HTTPaaS - HTTP as a Service
// Aplicación de gestión que orquesta el aprovisionamiento de instancias
// Apache sobre VirtualBox con registro automático en Bind9.
//
// Universidad del Quindío - Computación en la Nube 2026-1
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cloud-uq/httpaas/internal/config"
	"github.com/cloud-uq/httpaas/internal/handlers"
	"github.com/cloud-uq/httpaas/internal/middleware"
	"github.com/cloud-uq/httpaas/internal/services"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("==========================================")
	log.Println("  HTTPaaS - HTTP as a Service")
	log.Println("  Computación en la Nube 2026-1")
	log.Println("==========================================")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Error cargando configuración: %v", err)
	}
	log.Printf("Configuración cargada: dominio=%s, red=%s",
		cfg.DNS.Domain, cfg.Network.HostOnlyNet)

	// Crear directorios requeridos
	for _, dir := range []string{cfg.UploadDir, cfg.DataDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("No se pudo crear %s: %v", dir, err)
		}
	}

	// Inicializar servicios
	store, err := services.NewInstanceStore(cfg.DataDir + "/instances.json")
	if err != nil {
		log.Fatalf("Error inicializando almacén: %v", err)
	}

	dbStore, err := services.NewDBInstanceStore(cfg.DataDir + "/db_instances.json")
	if err != nil {
		log.Fatalf("Error inicializando almacén de BD: %v", err)
	}

	services.SetVBoxBin(cfg.VirtualBox.ManageBin)
	vbox := services.NewVBoxService(cfg)
	dns := services.NewDNSService(cfg)
	ssh := services.NewSSHService(cfg)
	sites, err := services.NewSiteServer(cfg.DataDir+"/sites", 9100, 9899,
		cfg.Network.Gateway+":80", cfg.DNS.Domain)
	if err != nil {
		log.Fatalf("Error inicializando SiteServer: %v", err)
	}

	orchestrator := services.NewOrchestrator(cfg, store, vbox, dns, ssh, sites)
	orchestrator.RestoreSites()

	dbOrch := services.NewDBOrchestrator(cfg, dbStore, store, vbox, dns, ssh)

	// Verificar prerrequisitos al arrancar
	if err := orchestrator.HealthCheck(); err != nil {
		log.Printf("ADVERTENCIA: Verificación de salud falló: %v", err)
		log.Println("La aplicación arrancará igualmente. Revise la configuración.")
	}

	// Sincronizar inventario inicial y luego cada 5 segundos.
	if err := orchestrator.SyncInventory(); err != nil {
		log.Printf("Sync inventario inicial: %v", err)
	} else {
		log.Printf("Inventario sincronizado: %d VMs", len(store.All()))
	}
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for range t.C {
			if err := orchestrator.SyncInventory(); err != nil {
				log.Printf("Sync inventario: %v", err)
			}
		}
	}()

	// Sincronizar estado de instancias de BD cada 5 segundos.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for range t.C {
			dbOrch.SyncVBoxState()
		}
	}()

	// Configurar handlers HTTP
	h := handlers.New(cfg, store, orchestrator)
	h.SetDBServices(dbStore, dbOrch)
	mux := http.NewServeMux()

	// Archivos estáticos
	mux.Handle("/static/", http.StripPrefix("/static/",
		http.FileServer(http.Dir(cfg.StaticDir))))

	// Rutas de la aplicación
	mux.HandleFunc("GET /{$}", h.Dashboard)
	mux.HandleFunc("GET /provision", h.ProvisionForm)
	mux.HandleFunc("POST /provision", h.ProvisionSubmit)
	mux.HandleFunc("POST /instances/{id}/delete", h.DeleteInstance)
	mux.HandleFunc("POST /instances/{id}/restart", h.RestartInstance)
	mux.HandleFunc("POST /instances/{id}/start", h.StartInstance)
	mux.HandleFunc("POST /instances/{id}/stop", h.StopInstance)
	mux.HandleFunc("GET /instances/{id}", h.InstanceDetail)
	mux.HandleFunc("GET /api/instances", h.APIListInstances)
	mux.HandleFunc("GET /api/instances/{id}/status", h.APIInstanceStatus)

	// Rutas DBaaS
	mux.HandleFunc("GET /dbaas", h.DBaaSDashboard)
	mux.HandleFunc("POST /dbaas", h.DBaaSProvision)
	mux.HandleFunc("GET /dbaas/{id}", h.DBaaSDetail)
	mux.HandleFunc("POST /dbaas/{id}/delete", h.DBaaSDelete)
	mux.HandleFunc("GET /api/dbaas", h.APIListDBInstances)
	mux.HandleFunc("GET /api/dbaas/{id}/status", h.APIDBInstanceStatus)

	mux.HandleFunc("GET /health", h.Health)

	// Middleware: logging + recover de pánicos
	handler := middleware.Logging(middleware.Recover(mux))

	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Shutdown limpio con SIGINT/SIGTERM
	go func() {
		log.Printf("Servidor escuchando en http://%s", cfg.HTTP.Addr)
		log.Printf("Dashboard: http://%s/", cfg.HTTP.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Error del servidor: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("Apagando servidor...")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Error al apagar: %v", err)
	}
	log.Println("Adios.")
}
