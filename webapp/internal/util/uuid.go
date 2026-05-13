// Package util contiene utilidades pequeñas que no merecen un paquete propio.
package util

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewUUID devuelve un UUID v4 (random) en formato canónico
// "8-4-4-4-12". Se implementa con crypto/rand para evitar dependencias.
func NewUUID() string {
	var b [16]byte
	_, err := rand.Read(b[:])
	if err != nil {
		// Si crypto/rand falla algo muy grave pasa; devolvemos un fallback.
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", 0)
	}
	// RFC 4122: poner los bits de versión y variante.
	b[6] = (b[6] & 0x0f) | 0x40 // versión 4
	b[8] = (b[8] & 0x3f) | 0x80 // variante 10
	s := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[0:8], s[8:12], s[12:16], s[16:20], s[20:32])
}
