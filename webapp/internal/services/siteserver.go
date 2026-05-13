package services

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Site representa un sitio estático desplegado por la plataforma.
type Site struct {
	Hostname string
	Dir      string
	Port     int

	server *http.Server
}

// SiteServer aloja múltiples sitios estáticos, uno por puerto local.
// Cada "instancia HTTP" aprovisionada por la plataforma se sirve como
// uno de estos sitios, lo que permite tener un flujo de aprovisionamiento
// real (genera IP, despliega zip, publica URL) sin necesidad de VirtualBox.
type SiteServer struct {
	mu       sync.Mutex
	sites    map[string]*Site // por hostname
	rootDir  string
	portFrom int
	portTo   int

	// vhost: cuando está bindeado (típicamente 192.168.56.1:80), enruta
	// http://<hostname>.<domain>/ al directorio del sitio correspondiente,
	// permitiendo que el DNS local cloud.local sea la URL real visible.
	vhostBound bool
	domain     string // p. ej. "cloud.local" sin punto final
}

// NewSiteServer crea el servidor de sitios. Si vhostAddr no está vacío, intenta
// bindear un listener compartido que enruta por Host header a *.domain.
func NewSiteServer(rootDir string, portFrom, portTo int, vhostAddr, domain string) (*SiteServer, error) {
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, err
	}
	s := &SiteServer{
		sites:    map[string]*Site{},
		rootDir:  rootDir,
		portFrom: portFrom,
		portTo:   portTo,
		domain:   strings.TrimSuffix(strings.TrimSuffix(domain, "."), ""),
	}
	if vhostAddr != "" {
		ln, err := net.Listen("tcp", vhostAddr)
		if err != nil {
			log.Printf("vhost desactivado: no se pudo bindear %s: %v", vhostAddr, err)
		} else {
			srv := &http.Server{
				Handler:           http.HandlerFunc(s.vhostDispatch),
				ReadHeaderTimeout: 5 * time.Second,
			}
			go func() {
				if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
					log.Printf("vhost caído: %v", err)
				}
			}()
			s.vhostBound = true
			log.Printf("vhost activo en %s para *.%s", vhostAddr, s.domain)
		}
	}
	return s, nil
}

// VHostBound indica si el listener vhost compartido pudo bindearse.
func (s *SiteServer) VHostBound() bool { return s.vhostBound }

// vhostDispatch enruta por Host header al sitio correspondiente.
// Acepta tanto el FQDN ("blog.cloud.local") como el hostname corto ("blog").
func (s *SiteServer) vhostDispatch(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	short := host
	if s.domain != "" && strings.HasSuffix(host, "."+s.domain) {
		short = strings.TrimSuffix(host, "."+s.domain)
	}
	s.mu.Lock()
	site := s.sites[short]
	s.mu.Unlock()
	if site == nil {
		http.Error(w, fmt.Sprintf("sitio %q no encontrado", short), http.StatusNotFound)
		return
	}
	noCache(http.FileServer(http.Dir(site.Dir))).ServeHTTP(w, r)
}

// Dir devuelve el directorio donde se extrae el zip de un hostname.
func (s *SiteServer) Dir(hostname string) string {
	return filepath.Join(s.rootDir, hostname)
}

// Deploy extrae el zip, elige un puerto libre y levanta un servidor estático.
// Si el hostname ya tiene un sitio, lo reemplaza (re-deploy).
func (s *SiteServer) Deploy(hostname, zipPath string) (*Site, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Reemplazo limpio si existía
	if old, ok := s.sites[hostname]; ok {
		_ = old.server.Close()
		delete(s.sites, hostname)
	}

	dir := s.Dir(hostname)
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("limpiando dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := extractZip(zipPath, dir); err != nil {
		return nil, fmt.Errorf("descomprimiendo: %w", err)
	}
	flattenSingleRoot(dir)

	port, err := s.allocPortLocked()
	if err != nil {
		return nil, err
	}
	site := &Site{Hostname: hostname, Dir: dir, Port: port}
	site.server = &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           noCache(http.FileServer(http.Dir(dir))),
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.sites[hostname] = site

	go func() {
		if err := site.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("siteserver %s:%d caído: %v", hostname, port, err)
		}
	}()
	return site, nil
}

// Restore re-arranca un sitio que ya tenía contenido y puerto asignados
// (usado al iniciar la app, para que las instancias persistidas sigan vivas).
func (s *SiteServer) Restore(hostname string, port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.Dir(hostname)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("sin contenido en %s: %w", dir, err)
	}
	site := &Site{Hostname: hostname, Dir: dir, Port: port}
	site.server = &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           noCache(http.FileServer(http.Dir(dir))),
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.sites[hostname] = site
	go func() {
		if err := site.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("siteserver(restore) %s:%d caído: %v", hostname, port, err)
		}
	}()
	return nil
}

// Stop apaga el servidor del sitio y elimina su carpeta.
func (s *SiteServer) Stop(hostname string) error {
	s.mu.Lock()
	site, ok := s.sites[hostname]
	if ok {
		delete(s.sites, hostname)
	}
	s.mu.Unlock()
	if ok && site.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = site.server.Shutdown(ctx)
	}
	return os.RemoveAll(s.Dir(hostname))
}

// Get devuelve el sitio asociado a un hostname si existe.
func (s *SiteServer) Get(hostname string) *Site {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sites[hostname]
}

// allocPortLocked encuentra un puerto libre en [portFrom, portTo]. Asume lock tomado.
func (s *SiteServer) allocPortLocked() (int, error) {
	used := map[int]bool{}
	for _, st := range s.sites {
		used[st.Port] = true
	}
	for p := s.portFrom; p <= s.portTo; p++ {
		if used[p] {
			continue
		}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue // ocupado por otro proceso del sistema
		}
		_ = l.Close()
		return p, nil
	}
	return 0, fmt.Errorf("no hay puertos libres entre %d y %d", s.portFrom, s.portTo)
}

// noCache evita que el navegador cachee respuestas (útil al re-desplegar).
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// extractZip extrae zipPath dentro de dst, prevención básica de zip-slip.
func extractZip(zipPath, dst string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	dstClean := filepath.Clean(dst) + string(os.PathSeparator)
	for _, f := range r.File {
		// Normalizamos separadores (los zips pueden traer / en Windows)
		name := strings.ReplaceAll(f.Name, "\\", "/")
		target := filepath.Join(dst, filepath.FromSlash(name))
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), dstClean) &&
			filepath.Clean(target) != filepath.Clean(dst) {
			continue // zip-slip: ignorar entradas que escapan del directorio destino
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		in, err := f.Open()
		if err != nil {
			_ = out.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		_ = in.Close()
		_ = out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

// flattenSingleRoot mueve archivos un nivel arriba si el zip contenía una sola carpeta raíz.
func flattenSingleRoot(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return
	}
	inner := filepath.Join(dir, entries[0].Name())
	innerEntries, err := os.ReadDir(inner)
	if err != nil {
		return
	}
	for _, e := range innerEntries {
		_ = os.Rename(filepath.Join(inner, e.Name()), filepath.Join(dir, e.Name()))
	}
	_ = os.Remove(inner)
}
