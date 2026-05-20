package services

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"sync"

	"github.com/cloud-uq/httpaas/internal/models"
)

// InstanceStore es un almacén concurrente de instancias persistido a JSON.
// Para un proyecto académico es suficiente; en producción debería ser SQLite/Postgres.
type InstanceStore struct {
	mu        sync.RWMutex
	path      string
	Instances map[string]*models.Instance `json:"instances"`
}

func NewInstanceStore(path string) (*InstanceStore, error) {
	s := &InstanceStore{
		path:      path,
		Instances: make(map[string]*models.Instance),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *InstanceStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.save()
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, &s.Instances)
}

func (s *InstanceStore) save() error {
	data, err := json.MarshalIndent(s.Instances, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Save persiste los cambios al disco. Llamarlo después de mutaciones.
func (s *InstanceStore) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save()
}

// Put añade o reemplaza la instancia.
func (s *InstanceStore) Put(inst *models.Instance) error {
	s.mu.Lock()
	s.Instances[inst.ID] = inst
	err := s.save()
	s.mu.Unlock()
	return err
}

// Get devuelve la instancia con id dado, o nil.
func (s *InstanceStore) Get(id string) *models.Instance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Instances[id]
}

// Delete elimina la instancia.
func (s *InstanceStore) Delete(id string) error {
	s.mu.Lock()
	delete(s.Instances, id)
	err := s.save()
	s.mu.Unlock()
	return err
}

// All devuelve todas las instancias ordenadas por fecha de creación descendente.
func (s *InstanceStore) All() []*models.Instance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.Instance, 0, len(s.Instances))
	for _, v := range s.Instances {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// UsedIPs devuelve el conjunto de IPs asignadas a instancias web.
// Se usa para evitar colisiones al asignar IPs a instancias de BD.
func (s *InstanceStore) UsedIPs() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	used := make(map[string]bool, len(s.Instances))
	for _, v := range s.Instances {
		if v.IP != "" {
			used[v.IP] = true
		}
	}
	return used
}

// HostnameTaken indica si un hostname (corto) ya está en uso.
func (s *InstanceStore) HostnameTaken(hostname string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.Instances {
		if v.Hostname == hostname {
			return true
		}
	}
	return false
}

// AllocateIP devuelve la primera IP libre dentro del rango [start, end].
// El algoritmo escanea linealmente las IPs ocupadas; suficiente para pocas decenas.
func (s *InstanceStore) AllocateIP(start, end string) (string, error) {
	s.mu.RLock()
	used := make(map[string]bool, len(s.Instances))
	for _, v := range s.Instances {
		if v.IP != "" {
			used[v.IP] = true
		}
	}
	s.mu.RUnlock()

	startIP := net.ParseIP(start).To4()
	endIP := net.ParseIP(end).To4()
	if startIP == nil || endIP == nil {
		return "", fmt.Errorf("rango IP inválido: %s - %s", start, end)
	}
	for ip := dup4(startIP); !greater(ip, endIP); inc(ip) {
		s := ip.String()
		if !used[s] {
			return s, nil
		}
	}
	return "", fmt.Errorf("no hay IPs libres en %s-%s", start, end)
}

func dup4(ip net.IP) net.IP {
	out := make(net.IP, 4)
	copy(out, ip)
	return out
}

func inc(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return
		}
	}
}

func greater(a, b net.IP) bool {
	for i := 0; i < len(a); i++ {
		if a[i] > b[i] {
			return true
		}
		if a[i] < b[i] {
			return false
		}
	}
	return false
}
