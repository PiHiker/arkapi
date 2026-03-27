package handlers

import (
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// WhoisRequest is what the consumer sends
type WhoisRequest struct {
	Domain string `json:"domain"`
}

// WhoisResponse is the parsed WHOIS data
type WhoisResponse struct {
	Domain      string            `json:"domain"`
	Registrar   string            `json:"registrar,omitempty"`
	CreatedDate string            `json:"created_date,omitempty"`
	ExpiryDate  string            `json:"expiry_date,omitempty"`
	UpdatedDate string            `json:"updated_date,omitempty"`
	NameServers []string          `json:"name_servers,omitempty"`
	Status      []string          `json:"status,omitempty"`
	Parsed      map[string]string `json:"parsed"`
}

const whoisCacheTTL = 24 * time.Hour

type whoisCacheEntry struct {
	response  *WhoisResponse
	expiresAt time.Time
}

var whoisCache = struct {
	mu    sync.RWMutex
	items map[string]whoisCacheEntry
}{
	items: make(map[string]whoisCacheEntry),
}

// Whois handles /api/whois
// Cost: 5 sats
func (h *Handler) Whois(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req WhoisRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"domain\": \"example.com\"}"})
		return
	}

	if req.Domain == "" || !isValidDomain(req.Domain) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid domain"})
		return
	}

	h.executeHandler(w, r, "/api/whois", 5, func() (interface{}, error) {
		return doWhois(req.Domain)
	})
}

func doWhois(domain string) (*WhoisResponse, error) {
	if cached := getCachedWhois(domain); cached != nil {
		return cached, nil
	}

	cmd := exec.Command("whois", domain)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("whois lookup failed: %w", err)
	}

	raw := string(output)
	result := &WhoisResponse{
		Domain: domain,
		Parsed: make(map[string]string),
	}

	// Parse key: value pairs from WHOIS output
	lines := strings.Split(raw, "\n")
	nsRegex := regexp.MustCompile(`(?i)name\s*server`)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "%") || strings.HasPrefix(line, "#") {
			continue
		}

		// Split on first colon
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if val == "" {
			continue
		}
		if shouldSkipWhoisField(key, val) {
			continue
		}

		keyLower := strings.ToLower(key)

		// Store in parsed map
		result.Parsed[key] = val

		// Extract well-known fields
		switch {
		case strings.Contains(keyLower, "registrar") &&
			!strings.Contains(keyLower, "abuse") &&
			!strings.Contains(keyLower, "whois") &&
			!strings.Contains(keyLower, "url") &&
			!strings.Contains(keyLower, "iana"):
			if result.Registrar == "" {
				result.Registrar = val
			}
		case strings.Contains(keyLower, "creation") || strings.Contains(keyLower, "created"):
			if result.CreatedDate == "" {
				result.CreatedDate = val
			}
		case strings.Contains(keyLower, "expir"):
			if result.ExpiryDate == "" {
				result.ExpiryDate = val
			}
		case strings.Contains(keyLower, "updated") || strings.Contains(keyLower, "modified"):
			if result.UpdatedDate == "" {
				result.UpdatedDate = val
			}
		case nsRegex.MatchString(key):
			result.NameServers = append(result.NameServers, strings.ToLower(val))
		case strings.Contains(keyLower, "status"):
			result.Status = append(result.Status, val)
		}
	}

	if result.Registrar == "" && len(result.Parsed) == 0 {
		return nil, fmt.Errorf("no WHOIS data found for %s", domain)
	}

	setCachedWhois(domain, result)
	return result, nil
}

func getCachedWhois(domain string) *WhoisResponse {
	whoisCache.mu.RLock()
	entry, ok := whoisCache.items[domain]
	whoisCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	if entry.response.NameServers != nil {
		clone.NameServers = append([]string(nil), entry.response.NameServers...)
	}
	if entry.response.Status != nil {
		clone.Status = append([]string(nil), entry.response.Status...)
	}
	if entry.response.Parsed != nil {
		clone.Parsed = make(map[string]string, len(entry.response.Parsed))
		for k, v := range entry.response.Parsed {
			clone.Parsed[k] = v
		}
	}
	return &clone
}

func shouldSkipWhoisField(key, val string) bool {
	keyLower := strings.ToLower(strings.TrimSpace(key))
	valLower := strings.ToLower(strings.TrimSpace(val))

	skipKeyPhrases := []string{
		"terms of use",
		"last update of whois",
		"last update of whois database",
		"notice",
		"for more information on whois status codes",
		"url of the icann whois data problem reporting system",
		"url of the icann whois inaccuracy complaint form",
		"by the following terms of use",
		"under the terms and conditions at https",
		"register your domain name at https",
		"register your domain at https",
	}
	for _, phrase := range skipKeyPhrases {
		if strings.Contains(keyLower, phrase) {
			return true
		}
	}

	skipValuePhrases := []string{
		"access to whois information is provided to assist persons",
		"you agree that you will use this data only for lawful purposes",
		"queries to the whois services are throttled",
		"identity digital inc. and registry operator reserve the right",
		"under the terms and conditions at https",
		"the whois service is not a replacement for standard epp commands",
		"whois is not considered authoritative for registered domain objects",
		"abuse of the whois system through data mining is mitigated",
		"the registrar of record identified in this output may have an rdds service",
		"access to non-public data may be provided, upon request",
		"allow, enable, or otherwise support the transmission of mass",
	}
	for _, phrase := range skipValuePhrases {
		if strings.Contains(valLower, phrase) {
			return true
		}
	}

	if keyLower == "to" && strings.Contains(valLower, "allow, enable") {
		return true
	}

	return false
}

func setCachedWhois(domain string, response *WhoisResponse) {
	clone := *response
	if response.NameServers != nil {
		clone.NameServers = append([]string(nil), response.NameServers...)
	}
	if response.Status != nil {
		clone.Status = append([]string(nil), response.Status...)
	}
	if response.Parsed != nil {
		clone.Parsed = make(map[string]string, len(response.Parsed))
		for k, v := range response.Parsed {
			clone.Parsed[k] = v
		}
	}
	whoisCache.mu.Lock()
	whoisCache.items[domain] = whoisCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(whoisCacheTTL),
	}
	whoisCache.mu.Unlock()
}
