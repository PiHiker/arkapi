package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const ipURLhausCacheTTL = 24 * time.Hour

type IPURLhausHostSummary struct {
	Listed        bool     `json:"listed"`
	FirstSeen     string   `json:"first_seen,omitempty"`
	URLCount      int      `json:"url_count,omitempty"`
	SpamhausDBL   string   `json:"spamhaus_dbl,omitempty"`
	SURBL         string   `json:"surbl,omitempty"`
	SampleThreats []string `json:"sample_threats,omitempty"`
	SampleTags    []string `json:"sample_tags,omitempty"`
	Source        string   `json:"source"`
}

type ipURLhausHostResponse struct {
	QueryStatus string `json:"query_status"`
	FirstSeen   string `json:"firstseen"`
	URLCount    string `json:"url_count"`
	Blacklists  struct {
		SpamhausDBL string `json:"spamhaus_dbl"`
		SURBL       string `json:"surbl"`
	} `json:"blacklists"`
	URLs []struct {
		Threat string   `json:"threat"`
		Tags   []string `json:"tags"`
	} `json:"urls"`
}

type ipURLhausCacheEntry struct {
	response  *IPURLhausHostSummary
	expiresAt time.Time
}

var ipURLhausCache = struct {
	mu    sync.RWMutex
	items map[string]ipURLhausCacheEntry
}{
	items: make(map[string]ipURLhausCacheEntry),
}

func (h *Handler) lookupIPURLhausHost(ip string) (*IPURLhausHostSummary, error) {
	if strings.TrimSpace(h.Cfg.URLhausAuthKey) == "" {
		return nil, nil
	}
	if cached := getCachedIPURLhausHost(ip); cached != nil {
		return cached, nil
	}

	form := url.Values{}
	form.Set("host", ip)

	req, err := http.NewRequest(http.MethodPost, "https://urlhaus-api.abuse.ch/v1/host/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to build URLhaus request: %w", err)
	}
	req.Header.Set("Auth-Key", h.Cfg.URLhausAuthKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query URLhaus: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("urlhaus authentication failed")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("urlhaus rate limit reached")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("urlhaus returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var upstream ipURLhausHostResponse
	if err := json.NewDecoder(resp.Body).Decode(&upstream); err != nil {
		return nil, fmt.Errorf("failed to decode URLhaus response: %w", err)
	}

	summary := &IPURLhausHostSummary{
		Listed: upstream.QueryStatus == "ok",
		Source: "URLhaus",
	}
	if !summary.Listed {
		setCachedIPURLhausHost(ip, summary)
		return summary, nil
	}

	summary.FirstSeen = strings.TrimSpace(upstream.FirstSeen)
	summary.SpamhausDBL = strings.TrimSpace(upstream.Blacklists.SpamhausDBL)
	summary.SURBL = strings.TrimSpace(upstream.Blacklists.SURBL)
	if n, err := strconv.Atoi(strings.TrimSpace(upstream.URLCount)); err == nil {
		summary.URLCount = n
	}

	for _, item := range upstream.URLs {
		summary.SampleThreats = appendUniqueString(summary.SampleThreats, strings.TrimSpace(item.Threat))
		summary.SampleTags = appendUniqueString(summary.SampleTags, item.Tags...)
	}

	setCachedIPURLhausHost(ip, summary)
	return summary, nil
}

func getCachedIPURLhausHost(ip string) *IPURLhausHostSummary {
	ipURLhausCache.mu.RLock()
	entry, ok := ipURLhausCache.items[ip]
	ipURLhausCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	clone.SampleThreats = append([]string(nil), entry.response.SampleThreats...)
	clone.SampleTags = append([]string(nil), entry.response.SampleTags...)
	return &clone
}

func setCachedIPURLhausHost(ip string, response *IPURLhausHostSummary) {
	clone := *response
	clone.SampleThreats = append([]string(nil), response.SampleThreats...)
	clone.SampleTags = append([]string(nil), response.SampleTags...)
	ipURLhausCache.mu.Lock()
	ipURLhausCache.items[ip] = ipURLhausCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(ipURLhausCacheTTL),
	}
	pruneTTLCacheEntries(ipURLhausCache.items, maxHandlerCacheEntries, func(entry ipURLhausCacheEntry) time.Time {
		return entry.expiresAt
	})
	ipURLhausCache.mu.Unlock()
}
