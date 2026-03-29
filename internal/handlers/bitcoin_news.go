package handlers

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type BitcoinNewsRequest struct {
	Limit int `json:"limit,omitempty"`
}

type BitcoinNewsItem struct {
	Title       string `json:"title"`
	Link        string `json:"link"`
	Source      string `json:"source"`
	PublishedAt string `json:"published_at"`
	Summary     string `json:"summary,omitempty"`
	Sentiment   string `json:"sentiment,omitempty"`
}

type BitcoinNewsResponse struct {
	Items []BitcoinNewsItem `json:"items"`
}

type rssFeed struct {
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

type bitcoinNewsFeedSource struct {
	Name    string
	URL     string
	Bitcoin bool
}

type bitcoinNewsSortableItem struct {
	BitcoinNewsItem
	Published time.Time
}

var bitcoinNewsFeeds = []bitcoinNewsFeedSource{
	{Name: "Cointelegraph", URL: "https://cointelegraph.com/rss/tag/bitcoin", Bitcoin: true},
	{Name: "Bitcoin News", URL: "https://news.bitcoin.com/feed/", Bitcoin: true},
	{Name: "Bitcoin Magazine", URL: "https://bitcoinmagazine.com/feed", Bitcoin: true},
	{Name: "The Block", URL: "https://www.theblock.co/rss.xml", Bitcoin: false},
	{Name: "Decrypt", URL: "https://decrypt.co/feed", Bitcoin: false},
}

const bitcoinNewsCacheTTL = time.Hour

var bitcoinNewsTitleNoise = regexp.MustCompile(`[^a-z0-9]+`)
var bitcoinNewsPositiveTerms = []string{
	"surge", "rally", "gain", "gains", "rise", "rises", "record", "bull", "bullish",
	"adoption", "approval", "approved", "inflow", "inflows", "breakout", "beats",
	"growth", "profit", "profits", "rebound", "recover", "recovery", "upside",
}
var bitcoinNewsNegativeTerms = []string{
	"crash", "drop", "drops", "fall", "falls", "bear", "bearish", "hack", "hacked",
	"lawsuit", "ban", "banned", "selloff", "liquidation", "liquidations", "fraud",
	"loss", "losses", "decline", "declines", "fear", "war", "tariff", "tariffs",
}

var bitcoinNewsCache struct {
	mu        sync.RWMutex
	items     []BitcoinNewsItem
	expiresAt time.Time
}

func (h *Handler) BitcoinNews(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req BitcoinNewsRequest
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	_ = r.Body.Close()
	if strings.TrimSpace(string(body)) != "" {
		if err := json.Unmarshal(body, &req); err != nil {
			sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {} or {\"limit\": 10}"})
			return
		}
	}

	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 20 {
		req.Limit = 20
	}

	h.executeHandler(w, r, "/api/bitcoin-news", h.Cfg.BitcoinNewsCostSats, func() (interface{}, error) {
		return fetchBitcoinNews(req.Limit)
	})
}

func fetchBitcoinNews(limit int) (*BitcoinNewsResponse, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 20 {
		limit = 20
	}

	if cached := getCachedBitcoinNews(limit); cached != nil {
		return cached, nil
	}

	client := &http.Client{Timeout: 12 * time.Second}
	itemsBySource := make(map[string][]bitcoinNewsSortableItem, len(bitcoinNewsFeeds))
	totalItems := 0

	for _, source := range bitcoinNewsFeeds {
		feedItems, err := fetchFeedItems(client, source)
		if err != nil {
			continue
		}
		if len(feedItems) == 0 {
			continue
		}
		itemsBySource[source.Name] = feedItems
		totalItems += len(feedItems)
	}

	if totalItems == 0 {
		return nil, fmt.Errorf("unable to fetch any Bitcoin news feeds")
	}
	out := selectBitcoinNewsItems(itemsBySource, limit)

	setCachedBitcoinNews(out)
	return &BitcoinNewsResponse{Items: out}, nil
}

func getCachedBitcoinNews(limit int) *BitcoinNewsResponse {
	bitcoinNewsCache.mu.RLock()
	defer bitcoinNewsCache.mu.RUnlock()

	if time.Now().After(bitcoinNewsCache.expiresAt) || len(bitcoinNewsCache.items) == 0 {
		return nil
	}

	if limit > len(bitcoinNewsCache.items) {
		limit = len(bitcoinNewsCache.items)
	}
	items := append([]BitcoinNewsItem(nil), bitcoinNewsCache.items[:limit]...)
	return &BitcoinNewsResponse{Items: items}
}

func setCachedBitcoinNews(items []BitcoinNewsItem) {
	bitcoinNewsCache.mu.Lock()
	defer bitcoinNewsCache.mu.Unlock()

	bitcoinNewsCache.items = append([]BitcoinNewsItem(nil), items...)
	bitcoinNewsCache.expiresAt = time.Now().Add(bitcoinNewsCacheTTL)
}

func fetchFeedItems(client *http.Client, source bitcoinNewsFeedSource) ([]bitcoinNewsSortableItem, error) {
	req, err := http.NewRequest(http.MethodGet, source.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ArkAPI/1.0 (+https://arkapi.dev)")
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml;q=0.9, */*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("feed returned %d", resp.StatusCode)
	}

	var feed rssFeed
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 3<<20)).Decode(&feed); err != nil {
		return nil, err
	}

	items := make([]bitcoinNewsSortableItem, 0, len(feed.Channel.Items))
	for _, item := range feed.Channel.Items {
		title := strings.TrimSpace(item.Title)
		link := strings.TrimSpace(item.Link)
		if title == "" || link == "" {
			continue
		}
		if !source.Bitcoin && !looksBitcoinRelated(title+" "+item.Description) {
			continue
		}
		published := parseRSSDate(item.PubDate)
		items = append(items, bitcoinNewsSortableItem{
			BitcoinNewsItem: BitcoinNewsItem{
				Title:       title,
				Link:        sanitizeLink(link),
				Source:      source.Name,
				PublishedAt: published.UTC().Format(time.RFC3339),
				Summary:     summarizeDescription(item.Description),
				Sentiment:   classifyBitcoinNewsSentiment(title + " " + item.Description),
			},
			Published: published,
		})
	}

	return items, nil
}

func parseRSSDate(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Now().UTC()
	}
	layouts := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		time.RFC3339,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t
		}
	}
	return time.Now().UTC()
}

func looksBitcoinRelated(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "bitcoin") || strings.Contains(lower, " btc")
}

func sanitizeLink(link string) string {
	if idx := strings.Index(link, "?"); idx >= 0 {
		return link[:idx]
	}
	if idx := strings.Index(link, "#"); idx >= 0 {
		return link[:idx]
	}
	return link
}

func summarizeDescription(desc string) string {
	clean := strings.TrimSpace(stripHTML(desc))
	if len(clean) > 180 {
		return clean[:177] + "..."
	}
	return clean
}

func selectBitcoinNewsItems(itemsBySource map[string][]bitcoinNewsSortableItem, limit int) []BitcoinNewsItem {
	if limit <= 0 {
		limit = 10
	}

	sourceItems := make(map[string][]bitcoinNewsSortableItem, len(itemsBySource))
	sourceIndex := make(map[string]int, len(itemsBySource))
	for source, items := range itemsBySource {
		sorted := append([]bitcoinNewsSortableItem(nil), items...)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].Published.After(sorted[j].Published)
		})
		sourceItems[source] = sorted
		sourceIndex[source] = 0
	}

	selected := make([]bitcoinNewsSortableItem, 0, limit)
	seenLinks := make(map[string]struct{}, limit)
	seenTitles := make(map[string]struct{}, limit)

	for len(selected) < limit {
		progress := false
		for _, source := range bitcoinNewsFeeds {
			items := sourceItems[source.Name]
			idx := sourceIndex[source.Name]
			for idx < len(items) {
				item := items[idx]
				idx++
				if !markBitcoinNewsSeen(item, seenLinks, seenTitles) {
					continue
				}
				selected = append(selected, item)
				sourceIndex[source.Name] = idx
				progress = true
				break
			}
			sourceIndex[source.Name] = idx
			if len(selected) >= limit {
				break
			}
		}
		if !progress {
			break
		}
	}

	sort.Slice(selected, func(i, j int) bool {
		return selected[i].Published.After(selected[j].Published)
	})

	out := make([]BitcoinNewsItem, 0, len(selected))
	for _, item := range selected {
		out = append(out, item.BitcoinNewsItem)
	}
	return out
}

func markBitcoinNewsSeen(item bitcoinNewsSortableItem, seenLinks, seenTitles map[string]struct{}) bool {
	linkKey := sanitizeLink(item.Link)
	if _, ok := seenLinks[linkKey]; ok {
		return false
	}

	titleKey := normalizeBitcoinNewsTitle(item.Title)
	if titleKey != "" {
		if _, ok := seenTitles[titleKey]; ok {
			return false
		}
		seenTitles[titleKey] = struct{}{}
	}

	seenLinks[linkKey] = struct{}{}
	return true
}

func normalizeBitcoinNewsTitle(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	if title == "" {
		return ""
	}
	title = bitcoinNewsTitleNoise.ReplaceAllString(title, " ")
	title = strings.Join(strings.Fields(title), " ")
	return title
}

func classifyBitcoinNewsSentiment(text string) string {
	normalized := normalizeBitcoinNewsTitle(text)
	if normalized == "" {
		return "neutral"
	}

	score := 0
	for _, term := range bitcoinNewsPositiveTerms {
		if strings.Contains(normalized, term) {
			score++
		}
	}
	for _, term := range bitcoinNewsNegativeTerms {
		if strings.Contains(normalized, term) {
			score--
		}
	}

	switch {
	case score > 0:
		return "positive"
	case score < 0:
		return "negative"
	default:
		return "neutral"
	}
}

func stripHTML(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	return strings.Join(strings.Fields(strings.ReplaceAll(b.String(), "&nbsp;", " ")), " ")
}
