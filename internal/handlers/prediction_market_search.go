package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type PredictionMarketSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type PredictionMarketSearchResponse struct {
	Query    string                         `json:"query"`
	Platform string                         `json:"platform"`
	Results  []PredictionMarketSearchResult `json:"results"`
}

type PredictionMarketSearchResult struct {
	MarketID    string  `json:"market_id"`
	EventID     string  `json:"event_id,omitempty"`
	Title       string  `json:"title"`
	EventTitle  string  `json:"event_title,omitempty"`
	Platform    string  `json:"platform"`
	Status      string  `json:"status"`
	Probability float64 `json:"probability,omitempty"`
	VolumeUSD   float64 `json:"volume_usd,omitempty"`
	CloseTime   string  `json:"close_time,omitempty"`
	URL         string  `json:"url,omitempty"`
}

type polymarketSearchEnvelope struct {
	Events []polymarketEvent `json:"events"`
}

type polymarketEvent struct {
	ID      interface{}       `json:"id"`
	Slug    string            `json:"slug"`
	Title   string            `json:"title"`
	Markets []polymarketMarket `json:"markets"`
}

type polymarketMarket struct {
	ID            interface{} `json:"id"`
	Question      string      `json:"question"`
	Slug          string      `json:"slug"`
	EndDate       string      `json:"endDate"`
	Outcomes      string      `json:"outcomes"`
	OutcomePrices string      `json:"outcomePrices"`
	Volume        string      `json:"volume"`
	VolumeNum     float64     `json:"volumeNum"`
	Active        bool        `json:"active"`
	Closed        bool        `json:"closed"`
	Archived      bool        `json:"archived"`
}

const predictionMarketSearchCacheTTL = 5 * time.Minute

type predictionMarketSearchCacheEntry struct {
	response  *PredictionMarketSearchResponse
	expiresAt time.Time
}

var predictionMarketSearchCache = struct {
	mu    sync.RWMutex
	items map[string]predictionMarketSearchCacheEntry
}{
	items: make(map[string]predictionMarketSearchCacheEntry),
}

func (h *Handler) PredictionMarketSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req PredictionMarketSearchRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"query\": \"bitcoin\", \"limit\": 10}"})
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

	h.executeHandler(w, r, "/api/prediction-market-search", h.Cfg.PredictionMarketSearchCostSats, func() (interface{}, error) {
		return doPredictionMarketSearch(req.Query, req.Limit)
	})
}

func doPredictionMarketSearch(query string, limit int) (*PredictionMarketSearchResponse, error) {
	cacheKey := strings.ToLower(strings.TrimSpace(query)) + "|" + strconv.Itoa(limit)
	if cached := getCachedPredictionMarketSearch(cacheKey); cached != nil {
		return cached, nil
	}

	endpoint := fmt.Sprintf(
		"https://gamma-api.polymarket.com/public-search?q=%s&limit_per_type=%d&search_tags=false&search_profiles=false&cache=true",
		url.QueryEscape(query),
		limit,
	)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	req.Header.Set("User-Agent", "ArkAPI.dev Prediction Market Search")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("polymarket request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("polymarket returned status %d", resp.StatusCode)
	}

	var envelope polymarketSearchEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode polymarket response: %w", err)
	}

	results := make([]PredictionMarketSearchResult, 0, limit)
	for _, event := range envelope.Events {
		for _, market := range event.Markets {
			if market.Archived || market.Closed || !market.Active {
				continue
			}

			item := PredictionMarketSearchResult{
				MarketID:   stringifyAny(market.ID),
				EventID:    stringifyAny(event.ID),
				Title:      firstNonEmpty(market.Question, event.Title),
				EventTitle: event.Title,
				Platform:   "Polymarket",
				Status:     marketStatus(market),
				VolumeUSD:  marketVolumeUSD(market),
				CloseTime:  market.EndDate,
				URL:        polymarketURL(event.Slug, market.Slug),
			}

			if probability, ok := marketProbability(market); ok {
				item.Probability = probability
			}

			results = append(results, item)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Status != results[j].Status {
			return results[i].Status == "open"
		}
		return results[i].VolumeUSD > results[j].VolumeUSD
	})

	if len(results) > limit {
		results = results[:limit]
	}

	response := &PredictionMarketSearchResponse{
		Query:    query,
		Platform: "Polymarket",
		Results:  results,
	}
	setCachedPredictionMarketSearch(cacheKey, response)
	return response, nil
}

func getCachedPredictionMarketSearch(key string) *PredictionMarketSearchResponse {
	predictionMarketSearchCache.mu.RLock()
	entry, ok := predictionMarketSearchCache.items[key]
	predictionMarketSearchCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	if entry.response.Results != nil {
		clone.Results = append([]PredictionMarketSearchResult(nil), entry.response.Results...)
	}
	return &clone
}

func setCachedPredictionMarketSearch(key string, response *PredictionMarketSearchResponse) {
	clone := *response
	if response.Results != nil {
		clone.Results = append([]PredictionMarketSearchResult(nil), response.Results...)
	}
	predictionMarketSearchCache.mu.Lock()
	predictionMarketSearchCache.items[key] = predictionMarketSearchCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(predictionMarketSearchCacheTTL),
	}
	predictionMarketSearchCache.mu.Unlock()
}

func marketStatus(_ polymarketMarket) string {
	return "open"
}

func marketVolumeUSD(m polymarketMarket) float64 {
	if m.VolumeNum > 0 {
		return m.VolumeNum
	}
	if m.Volume == "" {
		return 0
	}
	value, err := strconv.ParseFloat(m.Volume, 64)
	if err != nil {
		return 0
	}
	return value
}

func marketProbability(m polymarketMarket) (float64, bool) {
	var outcomes []string
	var prices []string
	if err := json.Unmarshal([]byte(m.Outcomes), &outcomes); err != nil {
		return 0, false
	}
	if err := json.Unmarshal([]byte(m.OutcomePrices), &prices); err != nil {
		return 0, false
	}
	if len(prices) == 0 {
		return 0, false
	}

	index := 0
	for i, outcome := range outcomes {
		if strings.EqualFold(strings.TrimSpace(outcome), "yes") {
			index = i
			break
		}
	}
	if index >= len(prices) {
		return 0, false
	}
	value, err := strconv.ParseFloat(prices[index], 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func polymarketURL(eventSlug, marketSlug string) string {
	if eventSlug != "" {
		return "https://polymarket.com/event/" + eventSlug
	}
	if marketSlug != "" {
		return "https://polymarket.com/event/" + marketSlug
	}
	return ""
}

func stringifyAny(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
