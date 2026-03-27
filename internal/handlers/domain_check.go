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

// DomainCheckRequest is what the consumer sends
type DomainCheckRequest struct {
	Domain string `json:"domain"`
}

// DomainCheckResponse is the parsed domain availability data
type DomainCheckResponse struct {
	Domain      string   `json:"domain"`
	Available   bool     `json:"available"`
	Registrar   string   `json:"registrar,omitempty"`
	CreatedDate string   `json:"created_date,omitempty"`
	ExpiryDate  string   `json:"expiry_date,omitempty"`
	NameServers []string `json:"name_servers,omitempty"`
}

const domainCheckCacheTTL = 24 * time.Hour

type domainCheckCacheEntry struct {
	response  *DomainCheckResponse
	expiresAt time.Time
}

var domainCheckCache = struct {
	mu    sync.RWMutex
	items map[string]domainCheckCacheEntry
}{
	items: make(map[string]domainCheckCacheEntry),
}

// Patterns that indicate a domain is not registered / available
var availablePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)No match for`),
	regexp.MustCompile(`(?i)NOT FOUND`),
	regexp.MustCompile(`(?i)No Data Found`),
	regexp.MustCompile(`(?i)Domain not found`),
	regexp.MustCompile(`(?i)No entries found`),
	regexp.MustCompile(`(?i)Status:\s*(free|available)`),
}

// DomainCheck handles /api/domain-check
// Cost: configurable (default 3 sats)
func (h *Handler) DomainCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req DomainCheckRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"domain\": \"example.com\"}"})
		return
	}

	if req.Domain == "" || !isValidDomain(req.Domain) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid domain"})
		return
	}

	h.executeHandler(w, r, "/api/domain-check", h.Cfg.DomainCheckCostSats, func() (interface{}, error) {
		return doDomainCheck(req.Domain)
	})
}

func doDomainCheck(domain string) (*DomainCheckResponse, error) {
	if cached := getCachedDomainCheck(domain); cached != nil {
		return cached, nil
	}

	cmd := exec.Command("whois", domain)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// whois may exit non-zero for unregistered domains on some systems;
		// treat that as "available" only if we also got output to inspect.
		if len(output) == 0 {
			return nil, fmt.Errorf("whois lookup failed: %w", err)
		}
	}

	raw := string(output)
	result := &DomainCheckResponse{
		Domain: domain,
	}

	// Check for availability patterns in raw output
	for _, pat := range availablePatterns {
		if pat.MatchString(raw) {
			result.Available = true
			setCachedDomainCheck(domain, result)
			return result, nil
		}
	}

	// Parse key: value pairs from WHOIS output
	lines := strings.Split(raw, "\n")
	nsRegex := regexp.MustCompile(`(?i)name\s*server`)
	parsed := make(map[string]string)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "%") || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if val == "" {
			continue
		}

		keyLower := strings.ToLower(key)
		parsed[key] = val

		switch {
		case strings.Contains(keyLower, "registrar") && !strings.Contains(keyLower, "abuse"):
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
		case nsRegex.MatchString(key):
			result.NameServers = append(result.NameServers, strings.ToLower(val))
		}
	}

	// If we found no registrar and no parsed data, domain is likely available
	if result.Registrar == "" && len(parsed) == 0 {
		result.Available = true
	}

	setCachedDomainCheck(domain, result)
	return result, nil
}

func getCachedDomainCheck(domain string) *DomainCheckResponse {
	domainCheckCache.mu.RLock()
	entry, ok := domainCheckCache.items[domain]
	domainCheckCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	if entry.response.NameServers != nil {
		clone.NameServers = append([]string(nil), entry.response.NameServers...)
	}
	return &clone
}

func setCachedDomainCheck(domain string, response *DomainCheckResponse) {
	clone := *response
	if response.NameServers != nil {
		clone.NameServers = append([]string(nil), response.NameServers...)
	}
	domainCheckCache.mu.Lock()
	domainCheckCache.items[domain] = domainCheckCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(domainCheckCacheTTL),
	}
	domainCheckCache.mu.Unlock()
}
