// Package config holds all configuration for ArkAPI.
// Values are loaded from environment variables with code defaults where appropriate.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all app configuration
type Config struct {
	// Server
	Port     string
	BindHost string

	// MySQL connection
	DBUser     string
	DBPassword string
	DBHost     string
	DBPort     string
	DBName     string

	// Session defaults
	DefaultBalanceSats int64 // Starting balance for test sessions
	SessionTTLHours    int   // Hours before a session expires from inactivity

	// Basic abuse controls
	SessionCreateLimit         int
	SessionCreateWindowSeconds int
	APIRateLimit               int
	APIRateWindowSeconds       int

	// ComfyUI image generation
	ComfyBaseURL             string
	ComfyModelName           string
	ComfyMaxWaitSeconds      int
	ComfyImageCostSats       int
	ScreenshotCostSats       int
	QRGenerateCostSats       int
	BitcoinAddressCostSats   int
	CVELookupCostSats        int
	CVESearchCostSats        int
	PredictionMarketSearchCostSats int
	EmailAuthCostSats        int
	BitcoinNewsCostSats      int
	TranslateCostSats        int
	AITranslateCostSats      int
	AXFRCheckCostSats        int
	ScreenshotMaxWaitSeconds int
	ScreenshotServiceURL     string
	ScreenshotServiceToken   string
	TranslateServiceURL      string
	CloudflareAIAccountID    string
	CloudflareAIToken        string
	CloudflareAIModel        string
	CloudflareAICostSats     int
	CloudflareAITimeoutSeconds int
	PublicBaseURL            string
	ImageDownloadTTLSeconds  int

	DomainIntelCostSats    int
	DomainCheckCostSats    int

	// GeoIP databases (MMDB files, such as DB-IP Lite)
	GeoLite2CityPath string
	GeoLite2ASNPath  string

	// Bark integration
	PaymentMode string // "test" or "bark"
	BarkURL     string // barkd REST API base URL
	BarkToken   string // barkd auth token
}

// Load reads config from environment variables with sensible defaults.
func Load() Config {
	return Config{
		Port:                       getEnv("ARKAPI_PORT", "8080"),
		BindHost:                   getEnv("ARKAPI_BIND_HOST", "0.0.0.0"),
		DBUser:                     getEnv("ARKAPI_DB_USER", "arkapi"),
		DBPassword:                 getEnv("ARKAPI_DB_PASS", "CHANGE_THIS_PASSWORD"),
		DBHost:                     getEnv("ARKAPI_DB_HOST", "localhost"),
		DBPort:                     getEnv("ARKAPI_DB_PORT", "3306"),
		DBName:                     getEnv("ARKAPI_DB_NAME", "arkapi"),
		DefaultBalanceSats:         getEnvInt64("ARKAPI_DEFAULT_BALANCE_SATS", 10000),
		SessionTTLHours:            getEnvInt("ARKAPI_SESSION_TTL_HOURS", 24),
		SessionCreateLimit:         getEnvInt("ARKAPI_SESSION_CREATE_LIMIT", 10),
		SessionCreateWindowSeconds: getEnvInt("ARKAPI_SESSION_CREATE_WINDOW_SECONDS", 3600),
		APIRateLimit:               getEnvInt("ARKAPI_API_RATE_LIMIT", 60),
		APIRateWindowSeconds:       getEnvInt("ARKAPI_API_RATE_WINDOW_SECONDS", 60),
		ComfyBaseURL:               getEnv("ARKAPI_COMFY_BASE_URL", "http://127.0.0.1:8188"),
		ComfyModelName:             getEnv("ARKAPI_COMFY_MODEL_NAME", "dreamshaper_8.safetensors"),
		ComfyMaxWaitSeconds:        getEnvInt("ARKAPI_COMFY_MAX_WAIT_SECONDS", 90),
		ComfyImageCostSats:         getEnvInt("ARKAPI_COMFY_IMAGE_COST_SATS", 25),
		ScreenshotCostSats:         getEnvInt("ARKAPI_SCREENSHOT_COST_SATS", 15),
		QRGenerateCostSats:         getEnvInt("ARKAPI_QR_GENERATE_COST_SATS", 2),
		BitcoinAddressCostSats:     getEnvInt("ARKAPI_BITCOIN_ADDRESS_COST_SATS", 3),
		CVELookupCostSats:          getEnvInt("ARKAPI_CVE_LOOKUP_COST_SATS", 3),
		CVESearchCostSats:          getEnvInt("ARKAPI_CVE_SEARCH_COST_SATS", 4),
		PredictionMarketSearchCostSats: getEnvInt("ARKAPI_PREDICTION_MARKET_SEARCH_COST_SATS", 4),
		EmailAuthCostSats:          getEnvInt("ARKAPI_EMAIL_AUTH_COST_SATS", 4),
		BitcoinNewsCostSats:        getEnvInt("ARKAPI_BITCOIN_NEWS_COST_SATS", 2),
		TranslateCostSats:          getEnvInt("ARKAPI_TRANSLATE_COST_SATS", 3),
		AITranslateCostSats:        getEnvInt("ARKAPI_AI_TRANSLATE_COST_SATS", 25),
		AXFRCheckCostSats:          getEnvInt("ARKAPI_AXFR_CHECK_COST_SATS", 12),
		DomainCheckCostSats:        getEnvInt("ARKAPI_DOMAIN_CHECK_COST_SATS", 3),
		ScreenshotMaxWaitSeconds:   getEnvInt("ARKAPI_SCREENSHOT_MAX_WAIT_SECONDS", 15),
		ScreenshotServiceURL:       getEnv("ARKAPI_SCREENSHOT_SERVICE_URL", "http://127.0.0.1:9010/render"),
		ScreenshotServiceToken:     getEnv("ARKAPI_SCREENSHOT_SERVICE_TOKEN", "change-me-screenshot-token"),
		TranslateServiceURL:        getEnv("ARKAPI_TRANSLATE_SERVICE_URL", "http://127.0.0.1:5001/translate"),
		CloudflareAIAccountID:      getEnv("ARKAPI_CLOUDFLARE_AI_ACCOUNT_ID", ""),
		CloudflareAIToken:          getEnv("ARKAPI_CLOUDFLARE_AI_TOKEN", ""),
		CloudflareAIModel:          getEnv("ARKAPI_CLOUDFLARE_AI_MODEL", "@cf/meta/llama-3-8b-instruct"),
		CloudflareAICostSats:       getEnvInt("ARKAPI_CLOUDFLARE_AI_COST_SATS", 100),
		CloudflareAITimeoutSeconds: getEnvInt("ARKAPI_CLOUDFLARE_AI_TIMEOUT_SECONDS", 45),
		PublicBaseURL:              getEnv("ARKAPI_PUBLIC_BASE_URL", "https://arkapi.dev"),
		ImageDownloadTTLSeconds:    getEnvInt("ARKAPI_IMAGE_DOWNLOAD_TTL_SECONDS", 600),
		DomainIntelCostSats:        getEnvInt("ARKAPI_DOMAIN_INTEL_COST_SATS", 25),
		GeoLite2CityPath:           getEnv("ARKAPI_GEOLITE2_CITY_PATH", "/geoip/GeoLite2-City.mmdb"),
		GeoLite2ASNPath:            getEnv("ARKAPI_GEOLITE2_ASN_PATH", "/geoip/GeoLite2-ASN.mmdb"),
		PaymentMode:                getEnv("ARKAPI_PAYMENT_MODE", "test"),
		BarkURL:                    getEnv("ARKAPI_BARK_URL", "http://bark:3000"),
		BarkToken:                  getEnv("ARKAPI_BARK_TOKEN", ""),
	}
}

// Validate checks that no insecure placeholder defaults are still in use.
func (c Config) Validate() error {
	if c.DBPassword == "CHANGE_THIS_PASSWORD" {
		return fmt.Errorf("ARKAPI_DB_PASS is still the default — set it to a real password")
	}
	if c.ScreenshotServiceToken == "change-me-screenshot-token" {
		return fmt.Errorf("ARKAPI_SCREENSHOT_SERVICE_TOKEN is still the default — set a real token")
	}
	return nil
}

// DSN returns the MySQL connection string
func (c Config) DSN() string {
	return c.DBUser + ":" + c.DBPassword + "@tcp(" + c.DBHost + ":" + c.DBPort + ")/" + c.DBName + "?parseTime=true"
}

// getEnv reads an env var or returns a default
func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val, ok := os.LookupEnv(key); ok {
		if parsed, err := strconv.Atoi(val); err == nil {
			return parsed
		}
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if val, ok := os.LookupEnv(key); ok {
		if parsed, err := strconv.ParseInt(val, 10, 64); err == nil {
			return parsed
		}
	}
	return fallback
}
