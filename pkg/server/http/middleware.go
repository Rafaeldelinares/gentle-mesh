package http

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	stdhttp "net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// corsAllowMethods is the fixed set of HTTP methods advertised to cross-origin clients.
const corsAllowMethods = "GET, POST, PUT, DELETE, OPTIONS, HEAD"

// corsAllowHeaders is the fixed set of request headers allowed on cross-origin requests.
const corsAllowHeaders = "Content-Type, Authorization, Last-Event-ID, X-Requested-With, Accept"

// corsExposeHeaders lists response headers readable by cross-origin clients, including the
// SSE reconnection header and the reverse-proxy buffering hint.
const corsExposeHeaders = "Content-Type, Last-Event-ID, X-Accel-Buffering"

// RateLimiter implements a simple token-bucket rate limiter per IP.
type RateLimiter struct {
	mu       sync.Mutex
	clients  map[string]*clientLimiter
	limit    int           // requests per window
	window   time.Duration // time window
	burst    int           // max burst size
}

type clientLimiter struct {
	tokens    int
	lastReset time.Time
}

// NewRateLimiter creates a rate limiter.
// limit: requests per window
// window: time window (e.g., 1*time.Minute)
// burst: maximum burst size
func NewRateLimiter(limit int, window time.Duration, burst int) *RateLimiter {
	return &RateLimiter{
		clients: make(map[string]*clientLimiter),
		limit:   limit,
		window:  window,
		burst:   burst,
	}
}

// Allow checks if a request from the given IP is allowed.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cl, exists := rl.clients[ip]
	if !exists {
		rl.clients[ip] = &clientLimiter{
			tokens:    rl.burst - 1, // use one token
			lastReset: now,
		}
		return true
	}

	// Reset window if expired
	if now.Sub(cl.lastReset) > rl.window {
		cl.tokens = rl.burst - 1
		cl.lastReset = now
		return true
	}

	// Check if we have tokens
	if cl.tokens > 0 {
		cl.tokens--
		return true
	}

	return false
}

// RateLimitMiddleware creates a rate limiting middleware.
func RateLimitMiddleware(limiter *RateLimiter) func(stdhttp.Handler) stdhttp.Handler {
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			// Get client IP
			ip := r.Header.Get("X-Forwarded-For")
			if ip == "" {
				ip = r.RemoteAddr
				if colon := strings.LastIndex(ip, ":"); colon != -1 {
					ip = ip[:colon]
				}
			}

			if !limiter.Allow(ip) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(stdhttp.StatusTooManyRequests)
				json.NewEncoder(w).Encode(map[string]string{
					"error":       "rate limit exceeded",
					"retry_after": limiter.window.String(),
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// CORSMiddleware adds Cross-Origin Resource Sharing headers to every response and short-circuits
// preflight OPTIONS requests with HTTP 204 No Content. When the request carries an Origin header
// that origin is echoed and marked as varying; otherwise wildcard access is advertised.
func CORSMiddleware(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		header := w.Header()
		if origin := r.Header.Get("Origin"); origin != "" {
			header.Set("Access-Control-Allow-Origin", origin)
			header.Add("Vary", "Origin")
		} else {
			header.Set("Access-Control-Allow-Origin", "*")
		}
		header.Set("Access-Control-Allow-Methods", corsAllowMethods)
		header.Set("Access-Control-Allow-Headers", corsAllowHeaders)
		header.Set("Access-Control-Expose-Headers", corsExposeHeaders)

		if r.Method == stdhttp.MethodOptions {
			w.WriteHeader(stdhttp.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// AuthMiddleware validates incoming HTTP requests using a Bearer token.
// If token is empty, the middleware is a no-op and returns next.
// Requests to /healthz (and /healthz/) bypass authentication.
// CORS preflight OPTIONS requests bypass authentication so browsers can negotiate the request.
// Missing or invalid tokens result in HTTP 401 Unauthorized with {"error":"unauthorized"}.
func AuthMiddleware(token string, next stdhttp.Handler) stdhttp.Handler {
	if token == "" {
		return next
	}
	expectedAuth := "Bearer " + token
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.Method == stdhttp.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

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
