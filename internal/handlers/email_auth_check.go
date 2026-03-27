package handlers

import (
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

type EmailAuthCheckRequest struct {
	Domain   string `json:"domain"`
	Selector string `json:"selector,omitempty"`
}

type EmailAuthRecord struct {
	Present bool   `json:"present"`
	Record  string `json:"record,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

type DKIMResult struct {
	Present          bool              `json:"present"`
	SelectorUsed     string            `json:"selector_used,omitempty"`
	SelectorsChecked []string          `json:"selectors_checked"`
	FoundSelectors   []string          `json:"found_selectors,omitempty"`
	Records          map[string]string `json:"records,omitempty"`
}

type EmailAuthCheckResponse struct {
	Domain          string          `json:"domain"`
	Grade           string          `json:"grade"`
	SPF             EmailAuthRecord `json:"spf"`
	DMARC           EmailAuthRecord `json:"dmarc"`
	DKIM            DKIMResult      `json:"dkim"`
	Recommendations []string        `json:"recommendations,omitempty"`
}

var commonDKIMSelectors = []string{
	"default", "selector1", "selector2", "google", "google._domainkey", "k1",
	"dkim", "mail", "smtp", "s1", "s2", "fm1", "zoho", "zmail", "mandrill",
}

const emailAuthCacheTTL = time.Hour

type emailAuthCacheKey struct {
	Domain   string
	Selector string
}

type emailAuthCacheEntry struct {
	response  *EmailAuthCheckResponse
	expiresAt time.Time
}

var emailAuthCache = struct {
	mu    sync.RWMutex
	items map[emailAuthCacheKey]emailAuthCacheEntry
}{
	items: make(map[emailAuthCacheKey]emailAuthCacheEntry),
}

func (h *Handler) EmailAuthCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req EmailAuthCheckRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"domain\": \"example.com\"}"})
		return
	}

	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	req.Selector = strings.ToLower(strings.TrimSpace(req.Selector))

	if req.Domain == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "domain is required"})
		return
	}
	if !isValidDomain(req.Domain) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid domain name"})
		return
	}
	if req.Selector != "" && !isValidDKIMSelector(req.Selector) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid DKIM selector"})
		return
	}

	h.executeHandler(w, r, "/api/email-auth-check", h.Cfg.EmailAuthCostSats, func() (interface{}, error) {
		return doEmailAuthCheck(req.Domain, req.Selector)
	})
}

func doEmailAuthCheck(domain, selector string) (*EmailAuthCheckResponse, error) {
	if cached := getCachedEmailAuth(domain, selector); cached != nil {
		return cached, nil
	}

	resp := &EmailAuthCheckResponse{
		Domain: domain,
		DKIM: DKIMResult{
			SelectorsChecked: []string{},
			Records:          map[string]string{},
		},
	}

	spfRecords, _ := lookupTXTRecords(domain)
	for _, record := range spfRecords {
		if strings.HasPrefix(strings.ToLower(record), "v=spf1") {
			resp.SPF.Present = true
			resp.SPF.Record = record
			resp.SPF.Mode = classifySPF(record)
			break
		}
	}

	dmarcRecords, _ := lookupTXTRecords("_dmarc." + domain)
	for _, record := range dmarcRecords {
		if strings.HasPrefix(strings.ToLower(record), "v=dmarc1") {
			resp.DMARC.Present = true
			resp.DMARC.Record = record
			resp.DMARC.Mode = classifyDMARC(record)
			break
		}
	}

	selectors := buildDKIMSelectorList(selector)
	for _, sel := range selectors {
		recordName := sel + "._domainkey." + domain
		resp.DKIM.SelectorsChecked = append(resp.DKIM.SelectorsChecked, sel)
		records, err := lookupTXTRecords(recordName)
		if err != nil {
			continue
		}
		for _, record := range records {
			if strings.Contains(strings.ToLower(record), "v=dkim1") {
				resp.DKIM.Present = true
				if resp.DKIM.SelectorUsed == "" {
					resp.DKIM.SelectorUsed = sel
				}
				resp.DKIM.FoundSelectors = append(resp.DKIM.FoundSelectors, sel)
				resp.DKIM.Records[sel] = record
				break
			}
		}
	}

	if len(resp.DKIM.FoundSelectors) == 0 {
		resp.DKIM.Records = nil
	}

	resp.Grade = gradeEmailAuth(resp)
	resp.Recommendations = buildEmailAuthRecommendations(resp)

	if !resp.SPF.Present && !resp.DMARC.Present && !resp.DKIM.Present {
		return nil, fmt.Errorf("no SPF, DMARC, or DKIM records found for %s", domain)
	}

	setCachedEmailAuth(domain, selector, resp)
	return resp, nil
}

func getCachedEmailAuth(domain, selector string) *EmailAuthCheckResponse {
	key := emailAuthCacheKey{Domain: domain, Selector: selector}
	emailAuthCache.mu.RLock()
	entry, ok := emailAuthCache.items[key]
	emailAuthCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	clone.DKIM.SelectorsChecked = append([]string(nil), entry.response.DKIM.SelectorsChecked...)
	clone.DKIM.FoundSelectors = append([]string(nil), entry.response.DKIM.FoundSelectors...)
	if entry.response.DKIM.Records != nil {
		clone.DKIM.Records = make(map[string]string, len(entry.response.DKIM.Records))
		for k, v := range entry.response.DKIM.Records {
			clone.DKIM.Records[k] = v
		}
	}
	clone.Recommendations = append([]string(nil), entry.response.Recommendations...)
	return &clone
}

func setCachedEmailAuth(domain, selector string, response *EmailAuthCheckResponse) {
	clone := *response
	clone.DKIM.SelectorsChecked = append([]string(nil), response.DKIM.SelectorsChecked...)
	clone.DKIM.FoundSelectors = append([]string(nil), response.DKIM.FoundSelectors...)
	if response.DKIM.Records != nil {
		clone.DKIM.Records = make(map[string]string, len(response.DKIM.Records))
		for k, v := range response.DKIM.Records {
			clone.DKIM.Records[k] = v
		}
	}
	clone.Recommendations = append([]string(nil), response.Recommendations...)
	emailAuthCache.mu.Lock()
	emailAuthCache.items[emailAuthCacheKey{Domain: domain, Selector: selector}] = emailAuthCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(emailAuthCacheTTL),
	}
	emailAuthCache.mu.Unlock()
}

func lookupTXTRecords(name string) ([]string, error) {
	cmd := exec.Command("dig", "+short", "TXT", name) // #nosec G204
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("txt lookup failed: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	records := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		record := normalizeTXTRecord(line)
		if record != "" {
			records = append(records, record)
		}
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("no TXT records found")
	}
	return records, nil
}

func normalizeTXTRecord(line string) string {
	line = strings.ReplaceAll(line, `" "`, "")
	line = strings.ReplaceAll(line, `"`, "")
	return strings.TrimSpace(line)
}

func classifySPF(record string) string {
	lower := strings.ToLower(record)
	switch {
	case strings.Contains(lower, " -all"):
		return "enforced"
	case strings.Contains(lower, " ~all"):
		return "softfail"
	case strings.Contains(lower, " ?all"):
		return "neutral"
	case strings.Contains(lower, " +all"):
		return "permissive"
	default:
		return "present"
	}
}

func classifyDMARC(record string) string {
	lower := strings.ToLower(record)
	switch {
	case strings.Contains(lower, "p=reject"):
		return "reject"
	case strings.Contains(lower, "p=quarantine"):
		return "quarantine"
	case strings.Contains(lower, "p=none"):
		return "monitoring"
	default:
		return "present"
	}
}

func buildDKIMSelectorList(selector string) []string {
	if selector != "" {
		return []string{selector}
	}
	seen := map[string]struct{}{}
	selectors := make([]string, 0, len(commonDKIMSelectors))
	for _, sel := range commonDKIMSelectors {
		sel = strings.TrimSuffix(sel, "._domainkey")
		if _, ok := seen[sel]; ok || sel == "" {
			continue
		}
		seen[sel] = struct{}{}
		selectors = append(selectors, sel)
	}
	sort.Strings(selectors)
	return selectors
}

func isValidDKIMSelector(selector string) bool {
	if len(selector) == 0 || len(selector) > 63 {
		return false
	}
	for _, r := range selector {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func gradeEmailAuth(resp *EmailAuthCheckResponse) string {
	score := 0
	if resp.SPF.Present {
		score += 2
		if resp.SPF.Mode == "enforced" {
			score++
		}
		if resp.SPF.Mode == "permissive" {
			score--
		}
	}
	if resp.DMARC.Present {
		score += 2
		if resp.DMARC.Mode == "reject" || resp.DMARC.Mode == "quarantine" {
			score += 2
		}
	}
	if resp.DKIM.Present {
		score += 2
	}

	switch {
	case score >= 7:
		return "A"
	case score >= 5:
		return "B"
	case score >= 3:
		return "C"
	case score >= 2:
		return "D"
	case score >= 1:
		return "E"
	default:
		return "F"
	}
}

func buildEmailAuthRecommendations(resp *EmailAuthCheckResponse) []string {
	recommendations := []string{}
	if !resp.SPF.Present {
		recommendations = append(recommendations, "Publish an SPF record for your sending infrastructure.")
	} else if resp.SPF.Mode != "enforced" {
		recommendations = append(recommendations, "Tighten SPF to use -all once your authorized senders are complete.")
	}

	if !resp.DMARC.Present {
		recommendations = append(recommendations, "Publish a DMARC policy at _dmarc."+resp.Domain+".")
	} else if resp.DMARC.Mode == "monitoring" || resp.DMARC.Mode == "present" {
		recommendations = append(recommendations, "Move DMARC from monitoring to quarantine or reject after validation.")
	}

	if !resp.DKIM.Present {
		recommendations = append(recommendations, "Publish at least one DKIM selector or provide a selector to verify.")
	}

	return recommendations
}
