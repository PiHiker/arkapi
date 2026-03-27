package handlers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	axfrCacheTTL        = time.Hour
	axfrMaxRecordReturn = 500
	axfrTimeout         = 8 * time.Second
)

type AXFRCheckRequest struct {
	Domain string `json:"domain"`
}

type AXFRCheckResponse struct {
	Domain               string                 `json:"domain"`
	Allowed              bool                   `json:"allowed"`
	Explanation          string                 `json:"explanation"`
	NameserversChecked   []string               `json:"nameservers_checked"`
	SuccessfulNameserver string                 `json:"successful_nameserver,omitempty"`
	TransferRecordCount  int                    `json:"transfer_record_count"`
	Truncated            bool                   `json:"truncated,omitempty"`
	Records              map[string][]DNSRecord `json:"records,omitempty"`
}

type axfrCacheEntry struct {
	response  *AXFRCheckResponse
	expiresAt time.Time
}

var axfrCheckCache = struct {
	mu    sync.RWMutex
	items map[string]axfrCacheEntry
}{
	items: make(map[string]axfrCacheEntry),
}

func (h *Handler) AXFRCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req AXFRCheckRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"domain\": \"example.com\"}"})
		return
	}

	req.Domain = strings.TrimSpace(req.Domain)
	if req.Domain == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "domain is required"})
		return
	}
	if !isValidDomain(req.Domain) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid domain name"})
		return
	}

	h.executeHandler(w, r, "/api/axfr-check", h.Cfg.AXFRCheckCostSats, func() (interface{}, error) {
		return doAXFRCheck(req.Domain)
	})
}

func doAXFRCheck(domain string) (*AXFRCheckResponse, error) {
	if cached := getCachedAXFRCheck(domain); cached != nil {
		return cached, nil
	}

	nameservers := lookupNameservers(domain)
	if len(nameservers) == 0 {
		return nil, fmt.Errorf("no nameservers found for %s", domain)
	}

	result := &AXFRCheckResponse{
		Domain:             domain,
		NameserversChecked: append([]string(nil), nameservers...),
		Explanation:        "AXFR was not permitted by the checked authoritative nameservers, so no zone records were returned.",
	}

	for _, ns := range nameservers {
		parsed, total := attemptAXFR(domain, ns)
		if total == 0 {
			continue
		}
		result.Allowed = true
		result.SuccessfulNameserver = ns
		result.TransferRecordCount = total
		result.Explanation = fmt.Sprintf("AXFR was permitted by %s, so ArkAPI returned the exposed zone records.", ns)
		if total > len(parsed) {
			result.Truncated = true
			result.Explanation = fmt.Sprintf("AXFR was permitted by %s. ArkAPI returned the exposed zone records, but truncated the response after %d records.", ns, axfrMaxRecordReturn)
		}
		if len(parsed) > 0 {
			result.Records = groupDNSRecords(parsed)
		}
		break
	}

	setCachedAXFRCheck(domain, result)
	return result, nil
}

func lookupNameservers(domain string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), dnsLookupTimeout)
	defer cancel()

	records, err := net.DefaultResolver.LookupNS(ctx, domain)
	if err != nil || len(records) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(records))
	names := make([]string, 0, len(records))
	for _, record := range records {
		if record == nil {
			continue
		}
		host := strings.TrimSuffix(strings.TrimSpace(record.Host), ".")
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		names = append(names, host)
	}
	sort.Strings(names)
	return names
}

func attemptAXFR(domain, nameserver string) ([]DNSRecord, int) {
	ctx, cancel := context.WithTimeout(context.Background(), axfrTimeout)
	defer cancel()

	// #nosec G204 -- nameserver and domain are validated before use
	cmd := exec.CommandContext(ctx, "dig", "+time=3", "+tries=1", "+noall", "+answer", "@"+nameserver, domain, "AXFR")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, 0
	}

	records := make([]DNSRecord, 0, 32)
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		record := parseDIGLineForType(line, "")
		if record == nil {
			continue
		}
		total++
		if len(records) < axfrMaxRecordReturn {
			records = append(records, *record)
		}
	}
	return records, total
}

func groupDNSRecords(records []DNSRecord) map[string][]DNSRecord {
	grouped := make(map[string][]DNSRecord)
	for _, record := range records {
		grouped[record.Type] = append(grouped[record.Type], record)
	}
	return grouped
}

func getCachedAXFRCheck(domain string) *AXFRCheckResponse {
	axfrCheckCache.mu.RLock()
	entry, ok := axfrCheckCache.items[domain]
	axfrCheckCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	if entry.response.NameserversChecked != nil {
		clone.NameserversChecked = append([]string(nil), entry.response.NameserversChecked...)
	}
	if entry.response.Records != nil {
		clone.Records = make(map[string][]DNSRecord, len(entry.response.Records))
		for k, v := range entry.response.Records {
			clone.Records[k] = append([]DNSRecord(nil), v...)
		}
	}
	return &clone
}

func setCachedAXFRCheck(domain string, response *AXFRCheckResponse) {
	clone := *response
	if response.NameserversChecked != nil {
		clone.NameserversChecked = append([]string(nil), response.NameserversChecked...)
	}
	if response.Records != nil {
		clone.Records = make(map[string][]DNSRecord, len(response.Records))
		for k, v := range response.Records {
			clone.Records[k] = append([]DNSRecord(nil), v...)
		}
	}
	axfrCheckCache.mu.Lock()
	axfrCheckCache.items[domain] = axfrCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(axfrCacheTTL),
	}
	axfrCheckCache.mu.Unlock()
}
