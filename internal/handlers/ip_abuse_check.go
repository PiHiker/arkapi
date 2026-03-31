package handlers

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type IPAbuseCheckRequest struct {
	IP         string `json:"ip"`
	MaxAgeDays int    `json:"max_age_days,omitempty"`
}

type IPAbuseCheckResponse struct {
	IP                   string     `json:"ip"`
	AbuseConfidenceScore int        `json:"abuse_confidence_score"`
	CountryCode          string     `json:"country_code,omitempty"`
	UsageType            string     `json:"usage_type,omitempty"`
	ISP                  string     `json:"isp,omitempty"`
	Domain               string     `json:"domain,omitempty"`
	TotalReports         int        `json:"total_reports"`
	NumDistinctUsers     int        `json:"num_distinct_users"`
	LastReportedAt       *time.Time `json:"last_reported_at,omitempty"`
	IsWhitelisted        bool       `json:"is_whitelisted"`
	Source               string     `json:"source"`
	MaxAgeDays           int        `json:"max_age_days"`
}

type abuseIPDBCheckResponse struct {
	Data struct {
		IPAddress           string `json:"ipAddress"`
		IsPublic            bool   `json:"isPublic"`
		IPVersion           int    `json:"ipVersion"`
		IsWhitelisted       bool   `json:"isWhitelisted"`
		AbuseConfidenceScore int   `json:"abuseConfidenceScore"`
		CountryCode         string `json:"countryCode"`
		UsageType           string `json:"usageType"`
		ISP                 string `json:"isp"`
		Domain              string `json:"domain"`
		TotalReports        int    `json:"totalReports"`
		NumDistinctUsers    int    `json:"numDistinctUsers"`
		LastReportedAt      string `json:"lastReportedAt"`
	} `json:"data"`
}

const ipAbuseCheckCacheTTL = 12 * time.Hour

type ipAbuseCacheEntry struct {
	response  *IPAbuseCheckResponse
	expiresAt time.Time
}

var ipAbuseCheckCache = struct {
	mu    sync.RWMutex
	items map[string]ipAbuseCacheEntry
}{
	items: make(map[string]ipAbuseCacheEntry),
}

func (h *Handler) IPAbuseCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req IPAbuseCheckRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"ip\": \"8.8.8.8\", \"max_age_days\": 30}"})
		return
	}

	req.IP = strings.TrimSpace(req.IP)
	if req.IP == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "ip is required"})
		return
	}

	parsed := net.ParseIP(req.IP)
	if parsed == nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid IP address"})
		return
	}
	if !isPublicRoutableIP(parsed) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "use a public IPv4 or IPv6 address"})
		return
	}

	if req.MaxAgeDays == 0 {
		req.MaxAgeDays = 30
	}
	if req.MaxAgeDays < 1 || req.MaxAgeDays > 365 {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "max_age_days must be between 1 and 365"})
		return
	}

	h.executeHandler(w, r, "/api/ip-abuse-check", h.Cfg.IPAbuseCheckCostSats, func() (interface{}, error) {
		return h.doIPAbuseCheck(req.IP, req.MaxAgeDays)
	})
}

func (h *Handler) doIPAbuseCheck(ip string, maxAgeDays int) (*IPAbuseCheckResponse, error) {
	if strings.TrimSpace(h.Cfg.AbuseIPDBAPIKey) == "" {
		return nil, fmt.Errorf("abuseipdb integration not configured")
	}

	cacheKey := fmt.Sprintf("%s|%d", ip, maxAgeDays)
	if cached := getCachedIPAbuseCheck(cacheKey); cached != nil {
		return cached, nil
	}

	req, err := http.NewRequest(http.MethodGet, "https://api.abuseipdb.com/api/v2/check", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build AbuseIPDB request: %w", err)
	}

	query := req.URL.Query()
	query.Set("ipAddress", ip)
	query.Set("maxAgeInDays", fmt.Sprintf("%d", maxAgeDays))
	req.URL.RawQuery = query.Encode()
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Key", h.Cfg.AbuseIPDBAPIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query AbuseIPDB: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("abuse provider rate limit reached")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("abuse provider returned status %d", resp.StatusCode)
	}

	var upstream abuseIPDBCheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&upstream); err != nil {
		return nil, fmt.Errorf("failed to decode abuse response: %w", err)
	}

	result := &IPAbuseCheckResponse{
		IP:                   ip,
		AbuseConfidenceScore: upstream.Data.AbuseConfidenceScore,
		CountryCode:          upstream.Data.CountryCode,
		UsageType:            upstream.Data.UsageType,
		ISP:                  upstream.Data.ISP,
		Domain:               upstream.Data.Domain,
		TotalReports:         upstream.Data.TotalReports,
		NumDistinctUsers:     upstream.Data.NumDistinctUsers,
		IsWhitelisted:        upstream.Data.IsWhitelisted,
		Source:               "AbuseIPDB",
		MaxAgeDays:           maxAgeDays,
	}
	if upstream.Data.LastReportedAt != "" {
		if ts, err := time.Parse(time.RFC3339, upstream.Data.LastReportedAt); err == nil {
			ts = ts.UTC()
			result.LastReportedAt = &ts
		}
	}

	setCachedIPAbuseCheck(cacheKey, result)
	return result, nil
}

func isPublicRoutableIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	return true
}

func getCachedIPAbuseCheck(key string) *IPAbuseCheckResponse {
	ipAbuseCheckCache.mu.RLock()
	entry, ok := ipAbuseCheckCache.items[key]
	ipAbuseCheckCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	if entry.response.LastReportedAt != nil {
		ts := *entry.response.LastReportedAt
		clone.LastReportedAt = &ts
	}
	return &clone
}

func setCachedIPAbuseCheck(key string, response *IPAbuseCheckResponse) {
	clone := *response
	if response.LastReportedAt != nil {
		ts := *response.LastReportedAt
		clone.LastReportedAt = &ts
	}
	ipAbuseCheckCache.mu.Lock()
	ipAbuseCheckCache.items[key] = ipAbuseCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(ipAbuseCheckCacheTTL),
	}
	pruneTTLCacheEntries(ipAbuseCheckCache.items, maxHandlerCacheEntries, func(entry ipAbuseCacheEntry) time.Time {
		return entry.expiresAt
	})
	ipAbuseCheckCache.mu.Unlock()
}
