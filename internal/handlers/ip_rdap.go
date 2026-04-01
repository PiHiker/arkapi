package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const ipRDAPCacheTTL = 24 * time.Hour

type IPAbuseContact struct {
	Name            string   `json:"name,omitempty"`
	Org             string   `json:"org,omitempty"`
	Email           string   `json:"email,omitempty"`
	Phone           string   `json:"phone,omitempty"`
	Roles           []string `json:"roles,omitempty"`
	Source          string   `json:"source,omitempty"`
	IsAbuseSpecific bool     `json:"is_abuse_specific"`
}

type ipRDAPLookupResult struct {
	Contact       *IPAbuseContact
	ReportingNote string
}

type ipRDAPCacheEntry struct {
	result    *ipRDAPLookupResult
	expiresAt time.Time
}

var ipRDAPCache = struct {
	mu    sync.RWMutex
	items map[string]ipRDAPCacheEntry
}{
	items: make(map[string]ipRDAPCacheEntry),
}

type rdapIPResponse struct {
	Entities []rdapEntity `json:"entities"`
	Remarks  []rdapRemark `json:"remarks"`
}

type rdapEntity struct {
	Handle     string       `json:"handle"`
	Roles      []string     `json:"roles"`
	VCardArray []any        `json:"vcardArray"`
	Remarks    []rdapRemark `json:"remarks"`
	Entities   []rdapEntity `json:"entities"`
}

type rdapRemark struct {
	Title       string   `json:"title"`
	Description []string `json:"description"`
}

func lookupIPAbuseContact(ip string) (*ipRDAPLookupResult, error) {
	if cached := getCachedIPRDAP(ip); cached != nil {
		return cached, nil
	}

	req, err := http.NewRequest(http.MethodGet, "https://rdap-bootstrap.arin.net/bootstrap/ip/"+url.PathEscape(ip), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build RDAP request: %w", err)
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query RDAP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rdap returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read RDAP response: %w", err)
	}

	var upstream rdapIPResponse
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, fmt.Errorf("failed to decode RDAP response: %w", err)
	}

	source := formatRDAPSource(resp.Request.URL.Host)
	best := pickBestAbuseRDAPEntity(flattenRDAPEntities(upstream.Entities))
	result := &ipRDAPLookupResult{
		ReportingNote: extractRDAPReportingNote(upstream.Remarks),
	}
	if best != nil {
		result.Contact = &IPAbuseContact{
			Name:            best.Name,
			Org:             best.Org,
			Email:           best.Email,
			Phone:           best.Phone,
			Roles:           best.Roles,
			Source:          source,
			IsAbuseSpecific: hasRDAPRole(best.Roles, "abuse"),
		}
		if note := extractRDAPReportingNote(best.Remarks); note != "" {
			result.ReportingNote = note
		}
	}

	setCachedIPRDAP(ip, result)
	return result, nil
}

type rdapEntityContact struct {
	Name    string
	Org     string
	Email   string
	Phone   string
	Roles   []string
	Remarks []rdapRemark
}

func flattenRDAPEntities(entities []rdapEntity) []rdapEntityContact {
	out := make([]rdapEntityContact, 0, len(entities))
	var walk func([]rdapEntity)
	walk = func(list []rdapEntity) {
		for _, entity := range list {
			contact := parseRDAPEntityContact(entity)
			if contact.Email != "" || contact.Phone != "" {
				out = append(out, contact)
			}
			if len(entity.Entities) > 0 {
				walk(entity.Entities)
			}
		}
	}
	walk(entities)
	return out
}

func parseRDAPEntityContact(entity rdapEntity) rdapEntityContact {
	contact := rdapEntityContact{
		Roles:   append([]string(nil), entity.Roles...),
		Remarks: append([]rdapRemark(nil), entity.Remarks...),
	}
	if len(entity.VCardArray) < 2 {
		return contact
	}
	rows, ok := entity.VCardArray[1].([]any)
	if !ok {
		return contact
	}
	for _, row := range rows {
		fields, ok := row.([]any)
		if !ok || len(fields) < 4 {
			continue
		}
		key, _ := fields[0].(string)
		value, _ := fields[3].(string)
		value = strings.TrimSpace(value)
		switch key {
		case "fn":
			contact.Name = value
		case "org":
			contact.Org = value
		case "email":
			if contact.Email == "" {
				contact.Email = value
			}
		case "tel":
			if contact.Phone == "" {
				contact.Phone = value
			}
		}
	}
	return contact
}

func pickBestAbuseRDAPEntity(entities []rdapEntityContact) *rdapEntityContact {
	bestScore := 999
	var best *rdapEntityContact
	for i := range entities {
		if !hasRDAPRole(entities[i].Roles, "abuse") {
			continue
		}
		score := rdapRoleScore(entities[i].Roles)
		if score < bestScore {
			bestScore = score
			best = &entities[i]
		}
	}
	return best
}

func rdapRoleScore(roles []string) int {
	switch {
	case hasRDAPRole(roles, "abuse"):
		return 0
	case hasRDAPRole(roles, "noc"):
		return 1
	case hasRDAPRole(roles, "technical"):
		return 2
	case hasRDAPRole(roles, "administrative"):
		return 3
	default:
		return 10
	}
}

func hasRDAPRole(roles []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, role := range roles {
		if strings.ToLower(strings.TrimSpace(role)) == target {
			return true
		}
	}
	return false
}

func extractRDAPReportingNote(remarks []rdapRemark) string {
	for _, remark := range remarks {
		text := strings.Join(remark.Description, " ")
		lower := strings.ToLower(text)
		if strings.Contains(lower, "abuse") || strings.Contains(lower, "report") || strings.Contains(lower, "contact/") {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func formatRDAPSource(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	switch {
	case strings.Contains(host, "arin"):
		return "ARIN RDAP"
	case strings.Contains(host, "apnic"):
		return "APNIC RDAP"
	case strings.Contains(host, "afrinic"):
		return "AFRINIC RDAP"
	case strings.Contains(host, "lacnic"):
		return "LACNIC RDAP"
	case strings.Contains(host, "ripe"):
		return "RIPE RDAP"
	default:
		return "RDAP"
	}
}

func getCachedIPRDAP(ip string) *ipRDAPLookupResult {
	ipRDAPCache.mu.RLock()
	entry, ok := ipRDAPCache.items[ip]
	ipRDAPCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.result == nil {
		return nil
	}
	clone := *entry.result
	if entry.result.Contact != nil {
		contact := *entry.result.Contact
		if entry.result.Contact.Roles != nil {
			contact.Roles = append([]string(nil), entry.result.Contact.Roles...)
		}
		clone.Contact = &contact
	}
	return &clone
}

func setCachedIPRDAP(ip string, result *ipRDAPLookupResult) {
	clone := *result
	if result.Contact != nil {
		contact := *result.Contact
		if result.Contact.Roles != nil {
			contact.Roles = append([]string(nil), result.Contact.Roles...)
		}
		clone.Contact = &contact
	}
	ipRDAPCache.mu.Lock()
	ipRDAPCache.items[ip] = ipRDAPCacheEntry{
		result:    &clone,
		expiresAt: time.Now().Add(ipRDAPCacheTTL),
	}
	pruneTTLCacheEntries(ipRDAPCache.items, maxHandlerCacheEntries, func(entry ipRDAPCacheEntry) time.Time {
		return entry.expiresAt
	})
	ipRDAPCache.mu.Unlock()
}
