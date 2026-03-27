package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type CVELookupRequest struct {
	CVE string `json:"cve"`
}

type CVELookupResponse struct {
	CVEID               string   `json:"cve_id"`
	Found               bool     `json:"found"`
	Source              string   `json:"source"`
	NVDURL              string   `json:"nvd_url,omitempty"`
	PrimaryReferenceURL string   `json:"primary_reference_url,omitempty"`
	Published           string   `json:"published,omitempty"`
	LastModified        string   `json:"last_modified,omitempty"`
	VulnerabilityStatus string   `json:"vulnerability_status,omitempty"`
	Description         string   `json:"description,omitempty"`
	Severity            string   `json:"severity,omitempty"`
	CVSSScore           float64  `json:"cvss_score,omitempty"`
	CVSSVector          string   `json:"cvss_vector,omitempty"`
	CWEs                []string `json:"cwes,omitempty"`
	References          []string `json:"references,omitempty"`
	HasKEV              bool     `json:"has_kev"`
	KEVAdded            string   `json:"kev_added,omitempty"`
	RequiredAction      string   `json:"required_action,omitempty"`
}

const cveLookupCacheTTL = 24 * time.Hour

type cveLookupCacheEntry struct {
	response  *CVELookupResponse
	expiresAt time.Time
}

var cveLookupCache = struct {
	mu    sync.RWMutex
	items map[string]cveLookupCacheEntry
}{
	items: make(map[string]cveLookupCacheEntry),
}

var cveIDPattern = regexp.MustCompile(`(?i)^CVE-\d{4}-\d{4,}$`)

type nvdCVEResponse struct {
	Vulnerabilities []nvdVulnerability `json:"vulnerabilities"`
}

type nvdVulnerability struct {
	CVE nvdCVE `json:"cve"`
}

type nvdCVE struct {
	ID           string             `json:"id"`
	Published    string             `json:"published"`
	LastModified string             `json:"lastModified"`
	VulnStatus   string             `json:"vulnStatus"`
	Descriptions []nvdDescription   `json:"descriptions"`
	Metrics      nvdMetrics         `json:"metrics"`
	Weaknesses   []nvdWeakness      `json:"weaknesses"`
	References   []nvdReference     `json:"references"`
	Configurations []nvdConfiguration `json:"configurations"`
	CisaExploitAdd   string         `json:"cisaExploitAdd"`
	CisaRequiredAction string       `json:"cisaRequiredAction"`
}

type nvdDescription struct {
	Lang  string `json:"lang"`
	Value string `json:"value"`
}

type nvdMetrics struct {
	CVSSMetricV40 []struct {
		CVSSData struct {
			BaseScore    float64 `json:"baseScore"`
			BaseSeverity string  `json:"baseSeverity"`
			VectorString string  `json:"vectorString"`
		} `json:"cvssData"`
	} `json:"cvssMetricV40"`
	CVSSMetricV31 []struct {
		CVSSData struct {
			BaseScore    float64 `json:"baseScore"`
			BaseSeverity string  `json:"baseSeverity"`
			VectorString string  `json:"vectorString"`
		} `json:"cvssData"`
	} `json:"cvssMetricV31"`
	CVSSMetricV30 []struct {
		CVSSData struct {
			BaseScore    float64 `json:"baseScore"`
			BaseSeverity string  `json:"baseSeverity"`
			VectorString string  `json:"vectorString"`
		} `json:"cvssData"`
	} `json:"cvssMetricV30"`
	CVSSMetricV2 []struct {
		BaseSeverity string `json:"baseSeverity"`
		CVSSData     struct {
			BaseScore    float64 `json:"baseScore"`
			VectorString string  `json:"vectorString"`
		} `json:"cvssData"`
	} `json:"cvssMetricV2"`
}

type nvdWeakness struct {
	Description []nvdDescription `json:"description"`
}

type nvdReference struct {
	URL string `json:"url"`
}

type nvdConfiguration struct {
	Nodes []nvdConfigNode `json:"nodes"`
}

type nvdConfigNode struct {
	Operator string          `json:"operator"`
	Negate   bool            `json:"negate"`
	CPEMatch []nvdCPEMatch   `json:"cpeMatch"`
	Nodes    []nvdConfigNode `json:"nodes"`
}

type nvdCPEMatch struct {
	Vulnerable            bool   `json:"vulnerable"`
	Criteria              string `json:"criteria"`
	VersionStartIncluding string `json:"versionStartIncluding"`
	VersionStartExcluding string `json:"versionStartExcluding"`
	VersionEndIncluding   string `json:"versionEndIncluding"`
	VersionEndExcluding   string `json:"versionEndExcluding"`
}

func (h *Handler) CVELookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req CVELookupRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"cve\": \"CVE-2024-3400\"}"})
		return
	}

	req.CVE = strings.ToUpper(strings.TrimSpace(req.CVE))
	if !cveIDPattern.MatchString(req.CVE) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid CVE format"})
		return
	}

	h.executeHandler(w, r, "/api/cve-lookup", h.Cfg.CVELookupCostSats, func() (interface{}, error) {
		return doCVELookup(req.CVE)
	})
}

func doCVELookup(cveID string) (*CVELookupResponse, error) {
	if cached := getCachedCVE(cveID); cached != nil {
		return cached, nil
	}

	req, err := http.NewRequest(http.MethodGet, "https://services.nvd.nist.gov/rest/json/cves/2.0?cveId="+cveID, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "ArkAPI.dev CVE Lookup")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nvd request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nvd returned status %d", resp.StatusCode)
	}

	var nvd nvdCVEResponse
	if err := json.NewDecoder(resp.Body).Decode(&nvd); err != nil {
		return nil, fmt.Errorf("decode nvd response: %w", err)
	}

	result := &CVELookupResponse{
		CVEID:  cveID,
		Found:  false,
		Source: "NVD",
		NVDURL: "https://nvd.nist.gov/vuln/detail/" + cveID,
	}

	if len(nvd.Vulnerabilities) == 0 {
		setCachedCVE(cveID, result)
		return result, nil
	}

	cve := nvd.Vulnerabilities[0].CVE
	result.Found = true
	result.Published = cve.Published
	result.LastModified = cve.LastModified
	result.VulnerabilityStatus = cve.VulnStatus
	result.Description = firstEnglishValue(cve.Descriptions)
	result.Severity, result.CVSSScore, result.CVSSVector = selectBestCVSS(cve.Metrics)
	result.CWEs = collectWeaknesses(cve.Weaknesses)
	result.References = collectReferences(cve.References)
	result.PrimaryReferenceURL = selectPrimaryReference(result.References, result.NVDURL)
	result.HasKEV = cve.CisaExploitAdd != "" || cve.CisaRequiredAction != ""
	result.KEVAdded = cve.CisaExploitAdd
	result.RequiredAction = cve.CisaRequiredAction

	setCachedCVE(cveID, result)
	return result, nil
}

func firstEnglishValue(items []nvdDescription) string {
	for _, item := range items {
		if strings.EqualFold(item.Lang, "en") {
			return item.Value
		}
	}
	if len(items) > 0 {
		return items[0].Value
	}
	return ""
}

func selectBestCVSS(metrics nvdMetrics) (string, float64, string) {
	if len(metrics.CVSSMetricV40) > 0 {
		m := metrics.CVSSMetricV40[0].CVSSData
		return m.BaseSeverity, m.BaseScore, m.VectorString
	}
	if len(metrics.CVSSMetricV31) > 0 {
		m := metrics.CVSSMetricV31[0].CVSSData
		return m.BaseSeverity, m.BaseScore, m.VectorString
	}
	if len(metrics.CVSSMetricV30) > 0 {
		m := metrics.CVSSMetricV30[0].CVSSData
		return m.BaseSeverity, m.BaseScore, m.VectorString
	}
	if len(metrics.CVSSMetricV2) > 0 {
		m := metrics.CVSSMetricV2[0]
		return m.BaseSeverity, m.CVSSData.BaseScore, m.CVSSData.VectorString
	}
	return "", 0, ""
}

func collectWeaknesses(weaknesses []nvdWeakness) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, weakness := range weaknesses {
		for _, desc := range weakness.Description {
			if !strings.EqualFold(desc.Lang, "en") || desc.Value == "" {
				continue
			}
			if _, ok := seen[desc.Value]; ok {
				continue
			}
			seen[desc.Value] = struct{}{}
			out = append(out, desc.Value)
		}
	}
	return out
}

func collectReferences(refs []nvdReference) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, ref := range refs {
		if ref.URL == "" {
			continue
		}
		if _, ok := seen[ref.URL]; ok {
			continue
		}
		seen[ref.URL] = struct{}{}
		out = append(out, ref.URL)
	}
	return out
}

func selectPrimaryReference(refs []string, fallback string) string {
	if len(refs) == 0 {
		return fallback
	}

	for _, ref := range refs {
		if strings.Contains(ref, "security.") ||
			strings.Contains(ref, "advisory") ||
			strings.Contains(ref, "support.") ||
			strings.Contains(ref, "vendor") {
			return ref
		}
	}

	return refs[0]
}

func getCachedCVE(cveID string) *CVELookupResponse {
	cveLookupCache.mu.RLock()
	entry, ok := cveLookupCache.items[cveID]
	cveLookupCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	if entry.response.CWEs != nil {
		clone.CWEs = append([]string(nil), entry.response.CWEs...)
	}
	if entry.response.References != nil {
		clone.References = append([]string(nil), entry.response.References...)
	}
	return &clone
}

func setCachedCVE(cveID string, response *CVELookupResponse) {
	clone := *response
	if response.CWEs != nil {
		clone.CWEs = append([]string(nil), response.CWEs...)
	}
	if response.References != nil {
		clone.References = append([]string(nil), response.References...)
	}
	cveLookupCache.mu.Lock()
	cveLookupCache.items[cveID] = cveLookupCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(cveLookupCacheTTL),
	}
	cveLookupCache.mu.Unlock()
}
