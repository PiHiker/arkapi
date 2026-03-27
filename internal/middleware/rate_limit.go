package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type rateEntry struct {
	count int
	reset time.Time
}

var (
	rateMu sync.Mutex
	rates  = map[string]*rateEntry{}
)

// Cloudflare IPv4 and IPv6 ranges (https://www.cloudflare.com/ips/)
var cloudflareRanges []*net.IPNet

func init() {
	cidrs := []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22",
		"103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
		"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22",
		"198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
		"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32",
		"2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29",
		"2c0f:f248::/32",
	}
	for _, cidr := range cidrs {
		_, n, err := net.ParseCIDR(cidr)
		if err == nil {
			cloudflareRanges = append(cloudflareRanges, n)
		}
	}
}

func isCloudflareIP(ip net.IP) bool {
	for _, n := range cloudflareRanges {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func RateLimit(limit int, window time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || limit <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		key := r.URL.Path + "|" + clientIP(r)
		now := time.Now()

		rateMu.Lock()
		entry, ok := rates[key]
		if !ok || now.After(entry.reset) {
			entry = &rateEntry{count: 0, reset: now.Add(window)}
			rates[key] = entry
		}
		entry.count++
		remaining := limit - entry.count
		resetAt := entry.reset
		blocked := entry.count > limit
		rateMu.Unlock()

		w.Header().Set("X-RateLimit-Limit", itoa(limit))
		if remaining < 0 {
			remaining = 0
		}
		w.Header().Set("X-RateLimit-Remaining", itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", resetAt.UTC().Format(time.RFC3339))

		if blocked {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error":   "Too Many Requests",
				"message": "rate limit exceeded",
				"code":    http.StatusTooManyRequests,
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

func RateLimitByToken(limit int, window time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || limit <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		token := GetToken(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}

		key := "token|" + r.URL.Path + "|" + token
		now := time.Now()

		rateMu.Lock()
		entry, ok := rates[key]
		if !ok || now.After(entry.reset) {
			entry = &rateEntry{count: 0, reset: now.Add(window)}
			rates[key] = entry
		}
		entry.count++
		remaining := limit - entry.count
		resetAt := entry.reset
		blocked := entry.count > limit
		rateMu.Unlock()

		w.Header().Set("X-Token-RateLimit-Limit", itoa(limit))
		if remaining < 0 {
			remaining = 0
		}
		w.Header().Set("X-Token-RateLimit-Remaining", itoa(remaining))
		w.Header().Set("X-Token-RateLimit-Reset", resetAt.UTC().Format(time.RFC3339))

		if blocked {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error":   "Too Many Requests",
				"message": "per-token rate limit exceeded",
				"code":    http.StatusTooManyRequests,
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	// Extract the direct remote address first
	var remoteIP net.IP
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		remoteIP = net.ParseIP(host)
	} else {
		remoteIP = net.ParseIP(r.RemoteAddr)
	}

	// Only trust CF-Connecting-IP when the request actually comes from Cloudflare
	if remoteIP != nil && isCloudflareIP(remoteIP) {
		if cfip := net.ParseIP(r.Header.Get("CF-Connecting-IP")); cfip != nil {
			return cfip.String()
		}
	}

	if remoteIP != nil {
		return remoteIP.String()
	}
	return r.RemoteAddr
}

func init() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			rateMu.Lock()
			for key, entry := range rates {
				if now.After(entry.reset) {
					delete(rates, key)
				}
			}
			rateMu.Unlock()
		}
	}()
}

func itoa(v int) string {
	return strconv.Itoa(v)
}
