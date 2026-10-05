package http

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net"
	stdhttp "net/http"
	"net/url"
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

var tailscaleCIDR = func() *net.IPNet {
	_, cidr, _ := net.ParseCIDR("100.64.0.0/10")
	return cidr
}()

// originAllowed reports whether origin matches default mesh CORS rules or extra allowed origins.
func originAllowed(origin string, extra []string) bool {
	if origin == "tauri://localhost" {
		return true
	}

	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}

	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	hostname := strings.ToLower(u.Hostname())
	normOrigin := scheme + "://" + host

	for _, e := range extra {
		if origin == e {
			return true
		}
		eu, err := url.Parse(e)
		if err == nil && eu.Scheme != "" && eu.Host != "" {
			normE := strings.ToLower(eu.Scheme) + "://" + strings.ToLower(eu.Host)
			if normOrigin == normE {
				return true
			}
		}
	}

	if scheme == "http" && (hostname == "localhost" || hostname == "127.0.0.1") {
		return true
	}

	if scheme == "http" || scheme == "https" {
		ip := net.ParseIP(hostname)
		if ip != nil && tailscaleCIDR.Contains(ip) {
			return true
		}
		if strings.HasSuffix(hostname, ".ts.net") {
			return true
		}
	}

	return false
}

// CORSMiddlewareWithOrigins adds Cross-Origin Resource Sharing headers with origin validation.
// When an Origin header is present and allowed, it is echoed with Vary: Origin.
// If disallowed, Access-Control-Allow-Origin is omitted and OPTIONS requests receive 403 Forbidden.
// When no Origin is present, wildcard access ("*") is advertised.
func CORSMiddlewareWithOrigins(extra []string, next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		header := w.Header()
		header.Set("Access-Control-Allow-Methods", corsAllowMethods)
		header.Set("Access-Control-Allow-Headers", corsAllowHeaders)
		header.Set("Access-Control-Expose-Headers", corsExposeHeaders)

		origin := r.Header.Get("Origin")
		if origin != "" {
			if originAllowed(origin, extra) {
				header.Set("Access-Control-Allow-Origin", origin)
				header.Add("Vary", "Origin")
			} else {
				if r.Method == stdhttp.MethodOptions {
					header.Set("Content-Type", "application/json")
					w.WriteHeader(stdhttp.StatusForbidden)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "origin not allowed"})
					return
				}
			}
		} else {
			header.Set("Access-Control-Allow-Origin", "*")
		}

		if r.Method == stdhttp.MethodOptions {
			w.WriteHeader(stdhttp.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// CORSMiddleware adds Cross-Origin Resource Sharing headers to every response and short-circuits
// preflight OPTIONS requests with HTTP 204 No Content. When the request carries an Origin header
// that origin is echoed and marked as varying; otherwise wildcard access is advertised.
func CORSMiddleware(next stdhttp.Handler) stdhttp.Handler {
	return CORSMiddlewareWithOrigins(nil, next)
}

// HostMiddleware validates incoming requests against an allowed host list,
// Tailscale CGNAT IPs (100.64.0.0/10), and MagicDNS hosts (*.ts.net).
// Disallowed hosts receive HTTP 421 Misdirected Request.
func HostMiddleware(allowed []string, next stdhttp.Handler) stdhttp.Handler {
	allowedMap := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		h := strings.TrimSpace(a)
		if stripped, _, err := net.SplitHostPort(h); err == nil {
			h = stripped
		}
		if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
			h = h[1 : len(h)-1]
		}
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			allowedMap[h] = struct{}{}
		}
	}

	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		rawHost := r.Host
		host := rawHost
		if h, _, err := net.SplitHostPort(rawHost); err == nil {
			host = h
		}
		host = strings.TrimSpace(host)
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
		host = strings.ToLower(host)

		allowedHost := false
		if _, ok := allowedMap[host]; ok {
			allowedHost = true
		} else {
			ip := net.ParseIP(host)
			if ip != nil && tailscaleCIDR.Contains(ip) {
				allowedHost = true
			} else if strings.HasSuffix(host, ".ts.net") {
				allowedHost = true
			}
		}

		if !allowedHost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(stdhttp.StatusMisdirectedRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid host"})
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

// MTLSMiddleware validates that incoming HTTPS requests present a verified client certificate.
// The bootstrap routes ("/healthz", "/healthz/", "/v1/mesh/ca", "/v1/certs/enroll") bypass this check.
// If no verified client certificate is present, it responds with HTTP 401 and {"error":"client certificate required"}.
func MTLSMiddleware(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/healthz/" ||
			r.URL.Path == "/v1/mesh/ca" || r.URL.Path == "/v1/certs/enroll" {
			next.ServeHTTP(w, r)
			return
		}

		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(stdhttp.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "client certificate required"})
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
