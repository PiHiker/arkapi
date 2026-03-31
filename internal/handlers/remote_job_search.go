package handlers

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	remoteJobSearchCacheTTL   = 24 * time.Hour
	remoteJobSearchDefaultMax = 10
	remoteJobSearchMaxLimit   = 25
)

type RemoteJobSearchResponse struct {
	Search        string                 `json:"search,omitempty"`
	Category      string                 `json:"category,omitempty"`
	CompanyName   string                 `json:"company_name,omitempty"`
	Limit         int                    `json:"limit"`
	Source        string                 `json:"source"`
	ProviderNote  string                 `json:"provider_note,omitempty"`
	JobCount      int                    `json:"job_count"`
	TotalJobCount int                    `json:"total_job_count,omitempty"`
	Jobs          []RemoteJobSearchEntry `json:"jobs"`
}

type RemoteJobSearchEntry struct {
	ID                        int        `json:"id"`
	URL                       string     `json:"url"`
	Title                     string     `json:"title"`
	CompanyName               string     `json:"company_name"`
	CompanyLogo               string     `json:"company_logo,omitempty"`
	Category                  string     `json:"category,omitempty"`
	Tags                      []string   `json:"tags,omitempty"`
	JobType                   string     `json:"job_type,omitempty"`
	PublicationDate           *time.Time `json:"publication_date,omitempty"`
	CandidateRequiredLocation string     `json:"candidate_required_location,omitempty"`
	Salary                    string     `json:"salary,omitempty"`
	DescriptionText           string     `json:"description_text,omitempty"`
}

type remotiveRemoteJobsResponse struct {
	JobCount      int                `json:"job-count"`
	TotalJobCount int                `json:"total-job-count"`
	Jobs          []remotiveJobEntry `json:"jobs"`
}

type remotiveJobEntry struct {
	ID                        int      `json:"id"`
	URL                       string   `json:"url"`
	Title                     string   `json:"title"`
	CompanyName               string   `json:"company_name"`
	CompanyLogo               string   `json:"company_logo"`
	CompanyLogoURL            string   `json:"company_logo_url"`
	Category                  string   `json:"category"`
	Tags                      []string `json:"tags"`
	JobType                   string   `json:"job_type"`
	PublicationDate           string   `json:"publication_date"`
	CandidateRequiredLocation string   `json:"candidate_required_location"`
	Salary                    string   `json:"salary"`
	Description               string   `json:"description"`
}

type remoteJobSearchCacheEntry struct {
	response  *RemoteJobSearchResponse
	expiresAt time.Time
}

var remoteJobSearchCache = struct {
	mu    sync.RWMutex
	items map[string]remoteJobSearchCacheEntry
}{
	items: make(map[string]remoteJobSearchCacheEntry),
}

var (
	htmlTagPattern    = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRunPattern   = regexp.MustCompile(`\s+`)
)

// RemoteJobSearch handles /api/remote-job-search
func (h *Handler) RemoteJobSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use GET"})
		return
	}

	search := strings.TrimSpace(r.URL.Query().Get("search"))
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	companyName := strings.TrimSpace(r.URL.Query().Get("company_name"))
	if search == "" && category == "" && companyName == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "provide at least one of search, category, or company_name"})
		return
	}

	limit := remoteJobSearchDefaultMax
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		var parsed int
		if _, err := fmt.Sscanf(rawLimit, "%d", &parsed); err != nil || parsed < 1 || parsed > remoteJobSearchMaxLimit {
			sendJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and 25"})
			return
		}
		limit = parsed
	}

	h.executeHandler(w, r, "/api/remote-job-search", h.Cfg.RemoteJobSearchCostSats, func() (interface{}, error) {
		return h.doRemoteJobSearch(search, category, companyName, limit)
	})
}

func (h *Handler) doRemoteJobSearch(search, category, companyName string, limit int) (*RemoteJobSearchResponse, error) {
	cacheKey := strings.ToLower(strings.Join([]string{
		strings.TrimSpace(search),
		strings.TrimSpace(category),
		strings.TrimSpace(companyName),
		fmt.Sprintf("%d", limit),
	}, "|"))
	if cached := getCachedRemoteJobSearch(cacheKey); cached != nil {
		return cached, nil
	}

	req, err := http.NewRequest(http.MethodGet, "https://remotive.com/api/remote-jobs", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build remote jobs request: %w", err)
	}

	query := url.Values{}
	if search != "" {
		query.Set("search", search)
	}
	if category != "" {
		query.Set("category", category)
	}
	if companyName != "" {
		query.Set("company_name", companyName)
	}
	query.Set("limit", fmt.Sprintf("%d", limit))
	req.URL.RawQuery = query.Encode()

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query remote jobs provider: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote jobs provider returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read remote jobs response: %w", err)
	}

	var upstream remotiveRemoteJobsResponse
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, fmt.Errorf("failed to decode remote jobs response: %w", err)
	}

	items := make([]RemoteJobSearchEntry, 0, len(upstream.Jobs))
	for _, job := range upstream.Jobs {
		var publishedAt *time.Time
		if strings.TrimSpace(job.PublicationDate) != "" {
			if ts, err := time.Parse("2006-01-02T15:04:05", job.PublicationDate); err == nil {
				utc := ts.UTC()
				publishedAt = &utc
			}
		}

		companyLogo := strings.TrimSpace(job.CompanyLogo)
		if companyLogo == "" {
			companyLogo = strings.TrimSpace(job.CompanyLogoURL)
		}

		items = append(items, RemoteJobSearchEntry{
			ID:                        job.ID,
			URL:                       strings.TrimSpace(job.URL),
			Title:                     strings.TrimSpace(job.Title),
			CompanyName:               strings.TrimSpace(job.CompanyName),
			CompanyLogo:               companyLogo,
			Category:                  strings.TrimSpace(job.Category),
			Tags:                      job.Tags,
			JobType:                   strings.TrimSpace(job.JobType),
			PublicationDate:           publishedAt,
			CandidateRequiredLocation: strings.TrimSpace(job.CandidateRequiredLocation),
			Salary:                    strings.TrimSpace(job.Salary),
			DescriptionText:           compactHTMLText(job.Description),
		})
	}

	result := &RemoteJobSearchResponse{
		Search:        search,
		Category:      category,
		CompanyName:   companyName,
		Limit:         limit,
		Source:        "Remotive",
		ProviderNote:  "Remotive public API jobs are delayed by 24 hours and should be fetched sparingly.",
		JobCount:      len(items),
		TotalJobCount: upstream.TotalJobCount,
		Jobs:          items,
	}
	if result.TotalJobCount == 0 {
		result.TotalJobCount = upstream.JobCount
	}

	setCachedRemoteJobSearch(cacheKey, result)
	return result, nil
}

func compactHTMLText(input string) string {
	text := strings.TrimSpace(input)
	if text == "" {
		return ""
	}
	text = htmlTagPattern.ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	text = spaceRunPattern.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	if len(text) > 1200 {
		text = strings.TrimSpace(text[:1200]) + "..."
	}
	return text
}

func getCachedRemoteJobSearch(key string) *RemoteJobSearchResponse {
	remoteJobSearchCache.mu.RLock()
	entry, ok := remoteJobSearchCache.items[key]
	remoteJobSearchCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	if entry.response.Jobs != nil {
		clone.Jobs = append([]RemoteJobSearchEntry(nil), entry.response.Jobs...)
	}
	return &clone
}

func setCachedRemoteJobSearch(key string, response *RemoteJobSearchResponse) {
	clone := *response
	if response.Jobs != nil {
		clone.Jobs = append([]RemoteJobSearchEntry(nil), response.Jobs...)
	}
	remoteJobSearchCache.mu.Lock()
	remoteJobSearchCache.items[key] = remoteJobSearchCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(remoteJobSearchCacheTTL),
	}
	pruneTTLCacheEntries(remoteJobSearchCache.items, maxHandlerCacheEntries, func(entry remoteJobSearchCacheEntry) time.Time {
		return entry.expiresAt
	})
	remoteJobSearchCache.mu.Unlock()
}
