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

// DBInstanceStore es un almacén concurrente de instancias de bases de datos,
// persistido como JSON independiente del store de instancias web.
type DBInstanceStore struct {
	mu        sync.RWMutex
	path      string
	Instances map[string]*models.DBInstance `json:"instances"`
}

// NewDBInstanceStore carga (o crea) el archivo JSON de instancias de BD.
func NewDBInstanceStore(path string) (*DBInstanceStore, error) {
	s := &DBInstanceStore{
		path:      path,
		Instances: make(map[string]*models.DBInstance),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *DBInstanceStore) load() error {
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

func (s *DBInstanceStore) save() error {
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

// Save persiste los cambios. Llamar después de cualquier mutación.
func (s *DBInstanceStore) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save()
}

// Put añade o reemplaza una instancia de BD.
func (s *DBInstanceStore) Put(inst *models.DBInstance) error {
	s.mu.Lock()
	s.Instances[inst.ID] = inst
	err := s.save()
	s.mu.Unlock()
	return err
}

// Get devuelve la instancia con el id dado, o nil.
func (s *DBInstanceStore) Get(id string) *models.DBInstance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Instances[id]
}

// Delete elimina la instancia.
func (s *DBInstanceStore) Delete(id string) error {
	s.mu.Lock()
	delete(s.Instances, id)
	err := s.save()
	s.mu.Unlock()
	return err
}

// All devuelve todas las instancias de BD ordenadas por fecha de creación desc.
func (s *DBInstanceStore) All() []*models.DBInstance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.DBInstance, 0, len(s.Instances))
	for _, v := range s.Instances {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// UsedIPs devuelve el conjunto de IPs actualmente asignadas a instancias de BD.
// Se usa junto con InstanceStore.UsedIPs para evitar colisiones en el pool.
func (s *DBInstanceStore) UsedIPs() map[string]bool {
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

// AllocateIP devuelve la primera IP libre en [start, end] excluyendo las IPs
// ya usadas tanto en instancias web (extraUsed) como en instancias de BD.
func (s *DBInstanceStore) AllocateIP(start, end string, extraUsed map[string]bool) (string, error) {
	s.mu.RLock()
	used := make(map[string]bool)
	for k, v := range extraUsed {
		used[k] = v
	}
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
		candidate := ip.String()
		if !used[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no hay IPs libres en %s-%s", start, end)
}
