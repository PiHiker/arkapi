package handlers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DNSRequest is what the consumer sends
type DNSRequest struct {
	Domain string `json:"domain"`
}

// DNSRecord is a single DNS record
type DNSRecord struct {
	Name  string `json:"name"`
	TTL   string `json:"ttl"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// DNSResponse is what we return
type DNSResponse struct {
	Domain  string                 `json:"domain"`
	Records map[string][]DNSRecord `json:"records"`
	Raw     string                 `json:"raw,omitempty"`
}

const dnsLookupCacheTTL = 5 * time.Minute
const dnsLookupTimeout = 3 * time.Second

type dnsCacheEntry struct {
	response  *DNSResponse
	expiresAt time.Time
}

var dnsLookupCache = struct {
	mu    sync.RWMutex
	items map[string]dnsCacheEntry
}{
	items: make(map[string]dnsCacheEntry),
}

// DNSLookup handles /api/dns-lookup
// Cost: 3 sats
// Runs: dig <domain> ANY +noall +answer
func (h *Handler) DNSLookup(w http.ResponseWriter, r *http.Request) {
	// Only accept POST
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	// Parse request body
	var req DNSRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"domain\": \"example.com\"}"})
		return
	}

	// Basic validation - prevent command injection
	if req.Domain == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "domain is required"})
		return
	}
	if !isValidDomain(req.Domain) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid domain name"})
		return
	}

	// Execute the handler with cost of 3 sats
	h.executeHandler(w, r, "/api/dns-lookup", 3, func() (interface{}, error) {
		return doDNSLookup(req.Domain)
	})
}

// doDNSLookup runs dig and parses the output into structured JSON
func doDNSLookup(domain string) (*DNSResponse, error) {
	if cached := getCachedDNSLookup(domain); cached != nil {
		return cached, nil
	}

	result := &DNSResponse{
		Domain:  domain,
		Records: make(map[string][]DNSRecord),
	}

	appendLookupIPs(result, domain, "A", "ip4")
	appendLookupIPs(result, domain, "AAAA", "ip6")
	appendLookupMX(result, domain)
	appendLookupTXT(result, domain)
	appendLookupNS(result, domain)
	appendLookupCNAME(result, domain)
	appendLookupSOA(result, domain)

	if len(result.Records) == 0 {
		return nil, fmt.Errorf("no DNS records found for %s", domain)
	}

	setCachedDNSLookup(domain, result)
	return result, nil
}

func appendLookupIPs(result *DNSResponse, domain, qtype, network string) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsLookupTimeout)
	defer cancel()

	ips, err := net.DefaultResolver.LookupIP(ctx, network, domain)
	if err != nil || len(ips) == 0 {
		return
	}

	values := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		values = append(values, ip.String())
	}
	sort.Strings(values)
	for _, value := range values {
		result.Records[qtype] = append(result.Records[qtype], DNSRecord{
			Name:  domain,
			TTL:   "",
			Type:  qtype,
			Value: value,
		})
	}
}

func appendLookupMX(result *DNSResponse, domain string) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsLookupTimeout)
	defer cancel()

	records, err := net.DefaultResolver.LookupMX(ctx, domain)
	if err != nil || len(records) == 0 {
		return
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].Pref == records[j].Pref {
			return records[i].Host < records[j].Host
		}
		return records[i].Pref < records[j].Pref
	})
	for _, record := range records {
		if record == nil {
			continue
		}
		host := strings.TrimSuffix(record.Host, ".")
		if host == "" && strings.TrimSpace(record.Host) == "." {
			host = "."
		}
		result.Records["MX"] = append(result.Records["MX"], DNSRecord{
			Name:  domain,
			TTL:   "",
			Type:  "MX",
			Value: fmt.Sprintf("%d %s", record.Pref, host),
		})
	}
}

func appendLookupTXT(result *DNSResponse, domain string) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsLookupTimeout)
	defer cancel()

	records, err := net.DefaultResolver.LookupTXT(ctx, domain)
	if err != nil || len(records) == 0 {
		return
	}

	sort.Strings(records)
	for _, record := range records {
		if strings.TrimSpace(record) == "" {
			continue
		}
		result.Records["TXT"] = append(result.Records["TXT"], DNSRecord{
			Name:  domain,
			TTL:   "",
			Type:  "TXT",
			Value: record,
		})
	}
}

func appendLookupNS(result *DNSResponse, domain string) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsLookupTimeout)
	defer cancel()

	records, err := net.DefaultResolver.LookupNS(ctx, domain)
	if err != nil || len(records) == 0 {
		return
	}

	names := make([]string, 0, len(records))
	for _, record := range records {
		if record == nil {
			continue
		}
		names = append(names, strings.TrimSuffix(record.Host, "."))
	}
	sort.Strings(names)
	for _, host := range names {
		result.Records["NS"] = append(result.Records["NS"], DNSRecord{
			Name:  domain,
			TTL:   "",
			Type:  "NS",
			Value: host,
		})
	}
}

func appendLookupCNAME(result *DNSResponse, domain string) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsLookupTimeout)
	defer cancel()

	cname, err := net.DefaultResolver.LookupCNAME(ctx, domain)
	if err != nil {
		return
	}

	cname = strings.TrimSuffix(strings.TrimSpace(cname), ".")
	if cname == "" || strings.EqualFold(cname, domain) {
		return
	}

	result.Records["CNAME"] = append(result.Records["CNAME"], DNSRecord{
		Name:  domain,
		TTL:   "",
		Type:  "CNAME",
		Value: cname,
	})
}

func appendLookupSOA(result *DNSResponse, domain string) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsLookupTimeout)
	defer cancel()

	// #nosec G204 -- domain is validated before use
	cmd := exec.CommandContext(ctx, "dig", "+time=2", "+tries=1", "+noall", "+answer", domain, "SOA")
	output, err := cmd.Output()
	if err != nil {
		return
	}

	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		record := parseDIGLineForType(line, "SOA")
		if record != nil {
			result.Records["SOA"] = append(result.Records["SOA"], *record)
		}
	}
}

func getCachedDNSLookup(domain string) *DNSResponse {
	dnsLookupCache.mu.RLock()
	entry, ok := dnsLookupCache.items[domain]
	dnsLookupCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	if entry.response.Records != nil {
		clone.Records = make(map[string][]DNSRecord, len(entry.response.Records))
		for k, v := range entry.response.Records {
			clone.Records[k] = append([]DNSRecord(nil), v...)
		}
	}
	return &clone
}

func setCachedDNSLookup(domain string, response *DNSResponse) {
	clone := *response
	if response.Records != nil {
		clone.Records = make(map[string][]DNSRecord, len(response.Records))
		for k, v := range response.Records {
			clone.Records[k] = append([]DNSRecord(nil), v...)
		}
	}
	dnsLookupCache.mu.Lock()
	dnsLookupCache.items[domain] = dnsCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(dnsLookupCacheTTL),
	}
	dnsLookupCache.mu.Unlock()
}

// parseDIGLine parses a single line of dig output
// Example: "example.com.		3600	IN	A	93.184.216.34"
func parseDIGLineForType(line, expectedType string) *DNSRecord {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, ";") || strings.Contains(line, " timed out") {
		return nil
	}

	// Split on whitespace (tabs and spaces)
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return nil
	}
	if _, err := strconv.Atoi(fields[1]); err != nil {
		return nil
	}
	if !strings.EqualFold(fields[2], "IN") {
		return nil
	}
	if expectedType != "" && !strings.EqualFold(fields[3], expectedType) {
		return nil
	}
	return &DNSRecord{
		Name:  strings.TrimSuffix(fields[0], "."),
		TTL:   fields[1],
		Type:  fields[3],
		Value: strings.Join(fields[4:], " "),
	}
}

// isValidDomain checks that a domain name is safe to pass to dig.
// This prevents command injection attacks.
var domainRegex = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*\.[a-zA-Z]{2,}$`)

func isValidDomain(domain string) bool {
	if len(domain) > 253 {
		return false
	}
	return domainRegex.MatchString(domain)
}
