package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type CVESearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type CVESearchItem struct {
	CVEID               string  `json:"cve_id"`
	NVDURL              string  `json:"nvd_url"`
	PrimaryReferenceURL string  `json:"primary_reference_url,omitempty"`
	Published           string  `json:"published,omitempty"`
	LastModified        string  `json:"last_modified,omitempty"`
	Description         string  `json:"description,omitempty"`
	Severity            string  `json:"severity,omitempty"`
	CVSSScore           float64 `json:"cvss_score,omitempty"`
	HasKEV              bool    `json:"has_kev"`
}

type CVESearchResponse struct {
	Query   string          `json:"query"`
	Results []CVESearchItem `json:"results"`
}

const cveSearchCacheTTL = 24 * time.Hour

type cveSearchCacheEntry struct {
	response  *CVESearchResponse
	expiresAt time.Time
}

var cveSearchCache = struct {
	mu    sync.RWMutex
	items map[string]cveSearchCacheEntry
}{
	items: make(map[string]cveSearchCacheEntry),
}

var wordpressCoreVersionQuery = regexp.MustCompile(`(?i)^\s*wordpress(?:\s+core)?\s+(\d+(?:\.\d+){1,2})\s*$`)

func (h *Handler) CVESearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req CVESearchRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"query\": \"wordpress information disclosure\", \"limit\": 10}"})
		return
	}

	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "query is required"})
		return
	}
	if len(req.Query) > 200 {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "query too long (max 200 characters)"})
		return
	}

	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 20 {
		req.Limit = 20
	}

	h.executeHandler(w, r, "/api/cve-search", h.Cfg.CVESearchCostSats, func() (interface{}, error) {
		return doCVESearch(req.Query, req.Limit)
	})
}

func doCVESearch(query string, limit int) (*CVESearchResponse, error) {
	cacheKey := strings.ToLower(strings.TrimSpace(query)) + "|" + fmt.Sprintf("%d", limit)
	if cached := getCachedCVESearch(cacheKey); cached != nil {
		return cached, nil
	}

	searchQuery := query
	resultsPerPage := limit
	wpVersion, isWordPressCoreVersionQuery := detectWordPressCoreVersionQuery(query)
	if isWordPressCoreVersionQuery {
		searchQuery = "from " + wpVersion + " through"
		resultsPerPage = 50
	}

	endpoint := fmt.Sprintf(
		"https://services.nvd.nist.gov/rest/json/cves/2.0?keywordSearch=%s&resultsPerPage=%d&noRejected",
		url.QueryEscape(searchQuery),
		resultsPerPage,
	)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "ArkAPI.dev CVE Search")
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

	vulns := nvd.Vulnerabilities
	if isWordPressCoreVersionQuery {
		vulns = filterWordPressCoreVersionVulnerabilities(vulns, wpVersion)
		if len(vulns) > limit {
			vulns = vulns[:limit]
		}
	}

	result := &CVESearchResponse{
		Query:   query,
		Results: make([]CVESearchItem, 0, len(vulns)),
	}

	for _, vuln := range vulns {
		cve := vuln.CVE
		item := CVESearchItem{
			CVEID:        cve.ID,
			NVDURL:       "https://nvd.nist.gov/vuln/detail/" + cve.ID,
			Published:    cve.Published,
			LastModified: cve.LastModified,
			Description:  firstEnglishValue(cve.Descriptions),
			HasKEV:       cve.CisaExploitAdd != "" || cve.CisaRequiredAction != "",
		}
		item.Severity, item.CVSSScore, _ = selectBestCVSS(cve.Metrics)
		item.PrimaryReferenceURL = selectPrimaryReference(collectReferences(cve.References), item.NVDURL)
		result.Results = append(result.Results, item)
	}

	setCachedCVESearch(cacheKey, result)
	return result, nil
}

func getCachedCVESearch(key string) *CVESearchResponse {
	cveSearchCache.mu.RLock()
	entry, ok := cveSearchCache.items[key]
	cveSearchCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	if entry.response.Results != nil {
		clone.Results = append([]CVESearchItem(nil), entry.response.Results...)
	}
	return &clone
}

func setCachedCVESearch(key string, response *CVESearchResponse) {
	clone := *response
	if response.Results != nil {
		clone.Results = append([]CVESearchItem(nil), response.Results...)
	}
	cveSearchCache.mu.Lock()
	cveSearchCache.items[key] = cveSearchCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(cveSearchCacheTTL),
	}
	cveSearchCache.mu.Unlock()
}

func detectWordPressCoreVersionQuery(query string) (string, bool) {
	matches := wordpressCoreVersionQuery.FindStringSubmatch(strings.TrimSpace(query))
	if len(matches) != 2 {
		return "", false
	}
	return matches[1], true
}

func filterWordPressCoreVersionVulnerabilities(vulns []nvdVulnerability, version string) []nvdVulnerability {
	filtered := make([]nvdVulnerability, 0, len(vulns))

	for _, vuln := range vulns {
		if matchesWordPressCoreVersion(vuln.CVE.Configurations, version) || descriptionMentionsWordPressCoreVersion(firstEnglishValue(vuln.CVE.Descriptions), version) {
			filtered = append(filtered, vuln)
		}
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].CVE.Published > filtered[j].CVE.Published
	})

	return filtered
}

func matchesWordPressCoreVersion(configurations []nvdConfiguration, version string) bool {
	for _, config := range configurations {
		if nodeMatchesWordPressCoreVersion(config.Nodes, version) {
			return true
		}
	}
	return false
}

func nodeMatchesWordPressCoreVersion(nodes []nvdConfigNode, version string) bool {
	for _, node := range nodes {
		for _, match := range node.CPEMatch {
			if cpeMatchIsWordPressCoreVersion(match, version) {
				return true
			}
		}
		if nodeMatchesWordPressCoreVersion(node.Nodes, version) {
			return true
		}
	}
	return false
}

func cpeMatchIsWordPressCoreVersion(match nvdCPEMatch, version string) bool {
	if !match.Vulnerable {
		return false
	}

	parts := strings.Split(match.Criteria, ":")
	if len(parts) < 6 {
		return false
	}
	if parts[3] != "wordpress" || parts[4] != "wordpress" {
		return false
	}

	criteriaVersion := parts[5]
	if criteriaVersion != "" && criteriaVersion != "*" && criteriaVersion != "-" {
		return compareDotVersion(criteriaVersion, version) == 0
	}

	if match.VersionStartIncluding == "" && match.VersionStartExcluding == "" && match.VersionEndIncluding == "" && match.VersionEndExcluding == "" {
		return false
	}

	if match.VersionStartIncluding != "" && compareDotVersion(version, match.VersionStartIncluding) < 0 {
		return false
	}
	if match.VersionStartExcluding != "" && compareDotVersion(version, match.VersionStartExcluding) <= 0 {
		return false
	}
	if match.VersionEndIncluding != "" && compareDotVersion(version, match.VersionEndIncluding) > 0 {
		return false
	}
	if match.VersionEndExcluding != "" && compareDotVersion(version, match.VersionEndExcluding) >= 0 {
		return false
	}

	return true
}

func descriptionMentionsWordPressCoreVersion(description, version string) bool {
	lower := strings.ToLower(description)
	versionLower := strings.ToLower(version)
	if !strings.Contains(lower, "wordpress") {
		return false
	}
	return strings.Contains(lower, "from "+versionLower+" through") ||
		strings.Contains(lower, "wordpress "+versionLower) ||
		strings.Contains(lower, "wordpress:"+versionLower)
}

func compareDotVersion(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	maxLen := len(aParts)
	if len(bParts) > maxLen {
		maxLen = len(bParts)
	}
	for i := 0; i < maxLen; i++ {
		aVal := 0
		bVal := 0
		if i < len(aParts) {
			aVal = parseVersionPart(aParts[i])
		}
		if i < len(bParts) {
			bVal = parseVersionPart(bParts[i])
		}
		if aVal < bVal {
			return -1
		}
		if aVal > bVal {
			return 1
		}
	}
	return 0
}

func parseVersionPart(part string) int {
	n, err := strconv.Atoi(part)
	if err != nil {
		return 0
	}
	return n
}
