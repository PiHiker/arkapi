// ArkAPI — Pay-per-call API proxy powered by the Ark protocol.
// This is the main entry point. It connects to MySQL, sets up routes,
// and starts the HTTP server.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/PiHiker/arkapi/internal/bark"
	"github.com/PiHiker/arkapi/internal/config"
	"github.com/PiHiker/arkapi/internal/database"
	"github.com/PiHiker/arkapi/internal/handlers"
	"github.com/PiHiker/arkapi/internal/middleware"
)

func main() {
	// ---- Load configuration ----
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config error: %v", err)
	}

	// ---- Connect to MySQL ----
	log.Println("connecting to MySQL...")
	db, err := database.New(cfg.DSN())
	if err != nil {
		log.Fatalf("FATAL: %v", err)
		os.Exit(1)
	}
	defer db.Close()
	log.Println("MySQL connected")

	// ---- Initialize bark client if in bark mode ----
	var barkClient *bark.Client
	if cfg.PaymentMode == "bark" {
		log.Printf("payment mode: bark (barkd at %s)", cfg.BarkURL)
		barkClient = bark.NewClient(cfg.BarkURL, cfg.BarkToken)

		// Start background payment poller
		bark.StartPaymentPoller(barkClient, db, cfg.SessionTTLHours)
	} else {
		log.Printf("payment mode: test (fake %d sat balance)", cfg.DefaultBalanceSats)
	}

	// ---- Open GeoIP databases ----
	geo := handlers.OpenGeoDBs(cfg.GeoLite2CityPath, cfg.GeoLite2ASNPath)
	defer geo.Close()

	// ---- Create handler with DB access ----
	h := handlers.New(db, cfg, barkClient, geo)

	// Auth middleware config (shared by all protected routes)
	authCfg := &middleware.AuthConfig{
		BarkClient: barkClient,
		TTLHours:   cfg.SessionTTLHours,
	}

	// ---- Set up the HTTP router ----
	mux := http.NewServeMux()

	// --- Public routes (no auth required) ---

	// Create a new session
	mux.Handle("/v1/sessions", middleware.RateLimit(
		cfg.SessionCreateLimit,
		time.Duration(cfg.SessionCreateWindowSeconds)*time.Second,
		http.HandlerFunc(h.CreateSession),
	))

	// Health check — minimal, no internal details
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	// Machine-readable API specification for agents and tooling
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.ServeFile(w, r, "/usr/local/share/arkapi/openapi.json")
	})

	// Public stats for the landing page dashboard
	mux.HandleFunc("/v1/stats", func(w http.ResponseWriter, r *http.Request) {
		if !allowLandingPageStats(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		stats, _ := db.GetStats()
		if stats == nil {
			stats = &database.Stats{EndpointBreakdown: map[string]int64{}}
		}
		breakdownJSON, _ := json.Marshal(stats.EndpointBreakdown)
		hourLabelsJSON, _ := json.Marshal(stats.HourLabels)
		calls24hJSON, _ := json.Marshal(stats.Calls24h)
		sats24hJSON, _ := json.Marshal(stats.Sats24h)
	fmt.Fprintf(w, `{"calls_today":%d,"sats_today":%d,"active_sessions":%d,"endpoint_breakdown":%s,"hour_labels":%s,"calls_24h":%s,"sats_24h":%s}`,
		stats.TotalCalls, stats.TotalSats, stats.ActiveSessions, breakdownJSON, hourLabelsJSON, calls24hJSON, sats24hJSON)
	})

	// Admin overview — access is restricted at Apache to the management IP.
	mux.HandleFunc("/v1/admin/overview", h.AdminOverview)

	// API catalog — lists available endpoints and pricing
	mux.HandleFunc("/v1/catalog", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
  "endpoints": [
    {"path": "/api/dns-lookup",  "method": "POST", "cost_sats": 3,  "description": "Full DNS records as structured JSON"},
    {"path": "/api/whois",       "method": "POST", "cost_sats": 5,  "description": "WHOIS data parsed into clean JSON"},
    {"path": "/api/ssl-check",   "method": "POST", "cost_sats": 5,  "description": "SSL certificate analysis"},
    {"path": "/api/headers",     "method": "POST", "cost_sats": 3,  "description": "HTTP security headers audit with score"},
    {"path": "/api/weather",     "method": "POST", "cost_sats": 3,  "description": "Current weather + 7-day forecast"},
    {"path": "/api/ip-lookup",   "method": "POST", "cost_sats": 3,  "description": "IP geolocation, ISP, and ASN data"},
    {"path": "/api/email-auth-check", "method": "POST", "cost_sats": %d, "description": "SPF, DKIM, and DMARC posture with A-F grade"},
    {"path": "/api/bitcoin-news", "method": "POST", "cost_sats": %d, "description": "Aggregated Bitcoin headlines from free RSS feeds"},
    {"path": "/api/ai-chat", "method": "POST", "cost_sats": %d, "description": "Anonymous AI chat with a 5-per-day token limit"},
    {"path": "/api/ai-translate", "method": "POST", "cost_sats": %d, "description": "Higher-quality AI translation with style control for more natural output"},
    {"path": "/api/translate", "method": "POST", "cost_sats": %d, "description": "Translate text with source-language auto-detection and target language selection"},
    {"path": "/api/axfr-check", "method": "POST", "cost_sats": %d, "description": "Check whether a domain allows DNS zone transfer (AXFR) and return records when exposed"},
    {"path": "/api/image-generate", "method": "POST", "cost_sats": %d, "description": "AI image generation with ArkAPI-managed rendering"},
    {"path": "/api/screenshot", "method": "POST", "cost_sats": %d, "description": "Full-page website screenshot with ArkAPI-hosted download URL"},
    {"path": "/api/qr-generate", "method": "POST", "cost_sats": %d, "description": "Generate QR code PNG from any text or URL"},
    {"path": "/api/bitcoin-address", "method": "POST", "cost_sats": %d, "description": "Validate mainnet Bitcoin addresses and fetch live on-chain balance data"},
    {"path": "/api/cve-search", "method": "POST", "cost_sats": %d, "description": "Search the official NVD feed for CVEs by keyword and return ranked matches"},
    {"path": "/api/prediction-market-search", "method": "POST", "cost_sats": %d, "description": "Search live Polymarket prediction markets and return normalized results"},
    {"path": "/api/cve-lookup", "method": "POST", "cost_sats": %d, "description": "Look up a CVE in the official NVD feed and return severity, CWE, KEV, and references"},
    {"path": "/api/domain-intel", "method": "POST", "cost_sats": %d, "description": "Aggregate DNS, WHOIS, TLS, headers, email auth, parsed security.txt and robots.txt metadata, improved tech fingerprints, HTTP behavior, provider detection, CT-log subdomains, and optional ai_summary"},
    {"path": "/api/domain-check", "method": "POST", "cost_sats": %d, "description": "Check domain name availability via WHOIS lookup"},
    {"path": "/api/btc-price", "method": "GET", "cost_sats": 1, "description": "Live Bitcoin spot price in 10 major fiat currencies with optional currency filtering and a 60-second cache"},
    {"path": "/api/url-to-markdown", "method": "POST", "cost_sats": 5, "description": "Clean Markdown extraction from any public URL"}
  ],
  "session": {
    "create": "POST /v1/sessions",
    "balance": "GET /v1/balance (requires auth)"
  },
  "auth": "Authorization: Bearer ak_your_token"
}`, cfg.EmailAuthCostSats, cfg.BitcoinNewsCostSats, cfg.CloudflareAICostSats, cfg.AITranslateCostSats, cfg.TranslateCostSats, cfg.AXFRCheckCostSats, cfg.ComfyImageCostSats, cfg.ScreenshotCostSats, cfg.QRGenerateCostSats, cfg.BitcoinAddressCostSats, cfg.CVESearchCostSats, cfg.PredictionMarketSearchCostSats, cfg.CVELookupCostSats, cfg.DomainIntelCostSats, cfg.DomainCheckCostSats)
	})

	// --- Protected routes (auth required) ---
	// These are wrapped in the Auth middleware which checks the session token

	// Balance check
	mux.Handle("/v1/balance", middleware.RateLimit(
		cfg.APIRateLimit,
		time.Duration(cfg.APIRateWindowSeconds)*time.Second,
		middleware.AuthWithBark(db, authCfg, http.HandlerFunc(h.BalanceCheck)),
	))

	// API endpoints — each one is authenticated and metered
	wrapAuth := func(h http.HandlerFunc) http.Handler {
		return middleware.RateLimit(
			cfg.APIRateLimit,
			time.Duration(cfg.APIRateWindowSeconds)*time.Second,
			middleware.AuthWithBark(db, authCfg, h),
		)
	}
	wrapExpensive := func(limit int, h http.HandlerFunc) http.Handler {
		return middleware.RateLimit(
			cfg.APIRateLimit,
			time.Duration(cfg.APIRateWindowSeconds)*time.Second,
			middleware.AuthWithBark(
				db,
				authCfg,
				middleware.RateLimitByToken(limit, time.Hour, h),
			),
		)
	}
	wrapDaily := func(limit int, h http.HandlerFunc) http.Handler {
		return middleware.RateLimit(
			cfg.APIRateLimit,
			time.Duration(cfg.APIRateWindowSeconds)*time.Second,
			middleware.AuthWithBark(
				db,
				authCfg,
				middleware.RateLimitByToken(limit, 24*time.Hour, h),
			),
		)
	}
	mux.Handle("/api/dns-lookup", wrapAuth(h.DNSLookup))
	mux.Handle("/api/whois", wrapAuth(h.Whois))
	mux.Handle("/api/ssl-check", wrapAuth(h.SSLCheck))
	mux.Handle("/api/headers", wrapAuth(h.Headers))
	mux.Handle("/api/weather", wrapAuth(h.Weather))
	mux.Handle("/api/ip-lookup", wrapAuth(h.IPLookup))
	mux.Handle("/api/email-auth-check", wrapAuth(h.EmailAuthCheck))
	mux.Handle("/api/bitcoin-news", wrapAuth(h.BitcoinNews))
	mux.Handle("/api/ai-chat", wrapDaily(5, h.AIChat))
	mux.Handle("/api/ai-translate", wrapAuth(h.AITranslate))
	mux.Handle("/api/translate", wrapAuth(h.Translate))
	mux.Handle("/api/axfr-check", wrapAuth(h.AXFRCheck))
	mux.Handle("/api/image-generate", wrapExpensive(5, h.ImageGenerate))
	mux.Handle("/api/screenshot", wrapExpensive(5, h.Screenshot))
	mux.Handle("/api/qr-generate", wrapAuth(h.QRGenerate))
	mux.Handle("/api/bitcoin-address", wrapAuth(h.BitcoinAddressValidate))
	mux.Handle("/api/cve-search", wrapAuth(h.CVESearch))
	mux.Handle("/api/prediction-market-search", wrapAuth(h.PredictionMarketSearch))
	mux.Handle("/api/cve-lookup", wrapAuth(h.CVELookup))
	mux.Handle("/api/domain-intel", wrapAuth(h.DomainIntel))
	mux.Handle("/api/domain-check", wrapAuth(h.DomainCheck))
	mux.Handle("/api/url-to-markdown", wrapAuth(h.URLToMarkdown))
	mux.Handle("/api/btc-price", wrapAuth(h.BTCPrice))
	mux.HandleFunc("/v1/downloads/", h.DownloadImage)

	// ---- Add CORS headers for browser/agent access ----
	handler := corsMiddleware(mux)

	// ---- Start server ----
	addr := cfg.BindHost + ":" + cfg.Port
	log.Printf("ArkAPI starting on %s", addr)
	log.Printf("  POST /v1/sessions     — create a session (no auth)")
	log.Printf("  GET  /v1/catalog      — list all endpoints (no auth)")
	log.Printf("  GET  /v1/balance      — check balance (auth required)")
	log.Printf("  POST /api/dns-lookup  — 3 sats")
	log.Printf("  POST /api/whois       — 5 sats")
	log.Printf("  POST /api/ssl-check   — 5 sats")
	log.Printf("  POST /api/headers     — 3 sats")
	log.Printf("  POST /api/weather     — 3 sats")
	log.Printf("  POST /api/ip-lookup   — 3 sats")
	log.Printf("  POST /api/email-auth-check — %d sats", cfg.EmailAuthCostSats)
	log.Printf("  POST /api/bitcoin-news — %d sats", cfg.BitcoinNewsCostSats)
	log.Printf("  POST /api/ai-chat — %d sats (5/day/token)", cfg.CloudflareAICostSats)
	log.Printf("  POST /api/ai-translate — %d sats", cfg.AITranslateCostSats)
	log.Printf("  POST /api/translate — %d sats", cfg.TranslateCostSats)
	log.Printf("  POST /api/axfr-check — %d sats", cfg.AXFRCheckCostSats)
	log.Printf("  POST /api/image-generate — %d sats", cfg.ComfyImageCostSats)
	log.Printf("  POST /api/screenshot  — %d sats", cfg.ScreenshotCostSats)
	log.Printf("  POST /api/qr-generate — %d sats", cfg.QRGenerateCostSats)
	log.Printf("  POST /api/bitcoin-address — %d sats", cfg.BitcoinAddressCostSats)
	log.Printf("  POST /api/cve-search — %d sats", cfg.CVESearchCostSats)
	log.Printf("  POST /api/prediction-market-search — %d sats", cfg.PredictionMarketSearchCostSats)
	log.Printf("  POST /api/cve-lookup — %d sats", cfg.CVELookupCostSats)
	log.Printf("  POST /api/domain-intel — %d sats (optional ai_summary, security.txt, robots.txt, HTTP behavior, CT logs)", cfg.DomainIntelCostSats)
	log.Printf("  POST /api/domain-check — %d sats", cfg.DomainCheckCostSats)
	log.Printf("  POST /api/url-to-markdown — 5 sats")
	log.Printf("  GET  /api/btc-price       — 1 sat")
	log.Println("ready")

	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("FATAL: server failed: %v", err)
	}
}

// corsMiddleware adds CORS headers so browsers and agents can call the API
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/stats" && !strings.HasPrefix(r.URL.Path, "/v1/admin/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		}

		// Handle preflight requests
		if r.Method == "OPTIONS" {
			if r.URL.Path == "/v1/stats" || strings.HasPrefix(r.URL.Path, "/v1/admin/") {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func allowLandingPageStats(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}

	// Require Sec-Fetch-Site header — only browsers send this, which
	// prevents non-browser tools from spoofing just a Referer header.
	site := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))
	if site == "" || (site != "same-origin" && site != "same-site") {
		return false
	}

	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !isAllowedOrigin(origin) {
		return false
	}

	referer := strings.TrimSpace(r.Header.Get("Referer"))
	if referer == "" {
		return false
	}

	refURL, err := url.Parse(referer)
	if err != nil {
		return false
	}

	return isAllowedOrigin(refURL.Scheme + "://" + refURL.Host)
}

func isAllowedOrigin(origin string) bool {
	allowed := strings.Split(strings.TrimSpace(os.Getenv("ARKAPI_ALLOWED_ORIGINS")), ",")
	for _, candidate := range allowed {
		if strings.TrimSpace(candidate) == origin && origin != "" {
			return true
		}
	}
	switch origin {
	case "https://arkapi.dev", "https://www.arkapi.dev":
		return true
	}
	return false
}
