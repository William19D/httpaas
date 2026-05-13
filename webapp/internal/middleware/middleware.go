package middleware

import (
	"log"
	"net/http"
	"runtime/debug"
	"time"
)

// Logging registra cada petición con método, ruta, código y duración.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingRW{ResponseWriter: w, status: 200}
		next.ServeHTTP(lrw, r)
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, lrw.status, time.Since(start))
	})
}

// Recover captura pánicos para que el servidor no muera por una rama oscura.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("PANIC %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type loggingRW struct {
	http.ResponseWriter
	status int
}

func (l *loggingRW) WriteHeader(s int) {
	l.status = s
	l.ResponseWriter.WriteHeader(s)
}
