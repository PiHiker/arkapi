package handlers

import (
	"encoding/json"
	"fmt"
	"io"
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
const rdapBootstrapTTL = 24 * time.Hour
const ianaRDAPBootstrapURL = "https://data.iana.org/rdap/dns.json"

type whoisCacheEntry struct {
	response  *WhoisResponse
	expiresAt time.Time
}

type rdapBootstrapCacheEntry struct {
	servers   map[string]string
	expiresAt time.Time
}

type rdapBootstrapResponse struct {
	Services [][][]string `json:"services"`
}

type rdapDomainResponse struct {
	LDHName string   `json:"ldhName"`
	Status  []string `json:"status"`
	Events  []struct {
		EventAction string `json:"eventAction"`
		EventDate   string `json:"eventDate"`
	} `json:"events"`
	Nameservers []struct {
		LDHName string `json:"ldhName"`
	} `json:"nameservers"`
	Entities []struct {
		Roles      []string    `json:"roles"`
		Handle     string      `json:"handle"`
		VCardArray interface{} `json:"vcardArray"`
	} `json:"entities"`
}

var whoisCache = struct {
	mu    sync.RWMutex
	items map[string]whoisCacheEntry
}{
	items: make(map[string]whoisCacheEntry),
}

var rdapBootstrapCache = struct {
	mu    sync.RWMutex
	entry *rdapBootstrapCacheEntry
}{}

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
	output, err := cmd.CombinedOutput()

	if len(output) > 0 {
		if parsed := parseWhoisRaw(domain, string(output)); whoisResponseHasUsefulData(parsed) {
			setCachedWhois(domain, parsed)
			return parsed, nil
		}
	}

	if rdap, rdapErr := doRDAPWhois(domain); rdapErr == nil && whoisResponseHasUsefulData(rdap) {
		setCachedWhois(domain, rdap)
		return rdap, nil
	}

	if err != nil {
		if len(output) == 0 {
			return nil, fmt.Errorf("whois lookup failed: %w", err)
		}
		return nil, fmt.Errorf("no WHOIS data found for %s", domain)
	}

	return nil, fmt.Errorf("no WHOIS data found for %s", domain)
}

func parseWhoisRaw(domain, raw string) *WhoisResponse {
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

	return result
}

func whoisResponseHasUsefulData(result *WhoisResponse) bool {
	if result == nil {
		return false
	}
	if result.Registrar != "" || result.CreatedDate != "" || result.ExpiryDate != "" || result.UpdatedDate != "" {
		return true
	}
	if len(result.NameServers) > 0 || len(result.Status) > 0 {
		return true
	}
	return len(result.Parsed) > 0
}

func doRDAPWhois(domain string) (*WhoisResponse, error) {
	baseURL, err := rdapBaseURLForDomain(domain)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/domain/"+domain, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("rdap returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var rdap rdapDomainResponse
	if err := json.Unmarshal(body, &rdap); err != nil {
		return nil, err
	}

	result := &WhoisResponse{
		Domain: domain,
		Parsed: map[string]string{},
	}

	for _, entity := range rdap.Entities {
		if sliceContainsFold(entity.Roles, "registrar") {
			if name := extractRDAPFN(entity.VCardArray); name != "" {
				result.Registrar = name
				break
			}
			if entity.Handle != "" {
				result.Registrar = entity.Handle
				break
			}
		}
	}

	for _, event := range rdap.Events {
		switch strings.ToLower(strings.TrimSpace(event.EventAction)) {
		case "registration":
			result.CreatedDate = event.EventDate
		case "expiration":
			result.ExpiryDate = event.EventDate
		case "last changed", "last update of rdap database":
			if result.UpdatedDate == "" {
				result.UpdatedDate = event.EventDate
			}
		}
	}

	for _, ns := range rdap.Nameservers {
		if ns.LDHName != "" {
			result.NameServers = append(result.NameServers, strings.ToLower(ns.LDHName))
		}
	}

	if len(rdap.Status) > 0 {
		result.Status = append(result.Status, rdap.Status...)
	}

	if result.Registrar != "" {
		result.Parsed["Registrar"] = result.Registrar
	}
	if result.CreatedDate != "" {
		result.Parsed["Creation Date"] = result.CreatedDate
	}
	if result.ExpiryDate != "" {
		result.Parsed["Registry Expiry Date"] = result.ExpiryDate
	}
	if result.UpdatedDate != "" {
		result.Parsed["Updated Date"] = result.UpdatedDate
	}

	return result, nil
}

func rdapBaseURLForDomain(domain string) (string, error) {
	tld := domain
	if idx := strings.LastIndex(domain, "."); idx != -1 && idx < len(domain)-1 {
		tld = domain[idx+1:]
	}
	tld = strings.ToLower(strings.TrimSpace(tld))

	rdapBootstrapCache.mu.RLock()
	entry := rdapBootstrapCache.entry
	rdapBootstrapCache.mu.RUnlock()

	if entry == nil || time.Now().After(entry.expiresAt) {
		fresh, err := fetchRDAPBootstrap()
		if err != nil {
			return "", err
		}
		rdapBootstrapCache.mu.Lock()
		rdapBootstrapCache.entry = fresh
		entry = fresh
		rdapBootstrapCache.mu.Unlock()
	}

	if entry == nil || entry.servers == nil {
		return "", fmt.Errorf("rdap bootstrap unavailable")
	}

	baseURL := entry.servers[tld]
	if baseURL == "" {
		return "", fmt.Errorf("no rdap server found for tld %s", tld)
	}
	return baseURL, nil
}

func fetchRDAPBootstrap() (*rdapBootstrapCacheEntry, error) {
	req, err := http.NewRequest(http.MethodGet, ianaRDAPBootstrapURL, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("rdap bootstrap returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var bootstrap rdapBootstrapResponse
	if err := json.Unmarshal(body, &bootstrap); err != nil {
		return nil, err
	}

	servers := make(map[string]string)
	for _, service := range bootstrap.Services {
		if len(service) < 2 || len(service[1]) == 0 {
			continue
		}
		baseURL := service[1][0]
		for _, tld := range service[0] {
			tld = strings.ToLower(strings.TrimSpace(tld))
			if tld != "" {
				servers[tld] = baseURL
			}
		}
	}

	return &rdapBootstrapCacheEntry{
		servers:   servers,
		expiresAt: time.Now().Add(rdapBootstrapTTL),
	}, nil
}

func extractRDAPFN(vcard interface{}) string {
	root, ok := vcard.([]interface{})
	if !ok || len(root) < 2 {
		return ""
	}
	entries, ok := root[1].([]interface{})
	if !ok {
		return ""
	}
	for _, entry := range entries {
		fields, ok := entry.([]interface{})
		if !ok || len(fields) < 4 {
			continue
		}
		name, _ := fields[0].(string)
		if !strings.EqualFold(name, "fn") {
			continue
		}
		value, _ := fields[3].(string)
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func sliceContainsFold(items []string, target string) bool {
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), target) {
			return true
		}
	}
	return false
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
		"getaddrinfo(",
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
