package http

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	stdhttp "net/http"
	"runtime/debug"
)

// AuthMiddleware validates incoming HTTP requests using a Bearer token.
// If token is empty, the middleware is a no-op and returns next.
// Requests to /healthz (and /healthz/) bypass authentication.
// Missing or invalid tokens result in HTTP 401 Unauthorized with {"error":"unauthorized"}.
func AuthMiddleware(token string, next stdhttp.Handler) stdhttp.Handler {
	if token == "" {
		return next
	}
	expectedAuth := "Bearer " + token
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/healthz/" {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(authHeader), []byte(expectedAuth)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(stdhttp.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// PanicRecoveryMiddleware recovers from any unhandled panic during HTTP execution,
// logs the stack trace, and writes HTTP 500 Internal Server Error with {"error":"internal server error"}.
func PanicRecoveryMiddleware(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[gentle-mesh] panic recovered in %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(stdhttp.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "internal server error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
