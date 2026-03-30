package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type BTCFearGreed struct {
	Value     float64   `json:"value"`
	Label     string    `json:"label"`
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updated_at"`
}

type BTCMarketStats struct {
	Rank         int     `json:"rank"`
	Change1hPct  float64 `json:"change_1h_pct"`
	Change24hPct float64 `json:"change_24h_pct"`
	Change7dPct  float64 `json:"change_7d_pct"`
	Volume24hUSD float64 `json:"volume_24h_usd"`
	MarketCapUSD float64 `json:"market_cap_usd"`
	Source       string  `json:"source"`
}

// BTCPriceResponse is what we return
type BTCPriceResponse struct {
	BTCUSD      string          `json:"btc_usd"`
	BTCEUR      string          `json:"btc_eur"`
	BTCGBP      string          `json:"btc_gbp"`
	BTCCAD      string          `json:"btc_cad"`
	BTCJPY      string          `json:"btc_jpy"`
	BTCAUD      string          `json:"btc_aud"`
	BTCCHF      string          `json:"btc_chf"`
	BTCCNY      string          `json:"btc_cny"`
	BTCHKD      string          `json:"btc_hkd"`
	BTCSGD      string          `json:"btc_sgd"`
	MarketStats *BTCMarketStats `json:"market_stats,omitempty"`
	FearGreed   *BTCFearGreed   `json:"fear_greed,omitempty"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type coinbaseResponse struct {
	Data struct {
		Currency string            `json:"currency"`
		Rates    map[string]string `json:"rates"`
	} `json:"data"`
}

type alternativeFearGreedResponse struct {
	Name       string `json:"name"`
	Data       []struct {
		Value               string `json:"value"`
		ValueClassification string `json:"value_classification"`
		Timestamp           string `json:"timestamp"`
		TimeUntilUpdate     string `json:"time_until_update"`
	} `json:"data"`
	Metadata struct {
		Error interface{} `json:"error"`
	} `json:"metadata"`
}

type alternativeTickerResponse struct {
	Data map[string]struct {
		Rank   int `json:"rank"`
		Quotes struct {
			USD struct {
				Price               float64 `json:"price"`
				Volume24h           float64 `json:"volume_24h"`
				MarketCap           float64 `json:"market_cap"`
				PercentageChange1h  float64 `json:"percentage_change_1h"`
				PercentageChange24h float64 `json:"percentage_change_24h"`
				PercentageChange7d  float64 `json:"percentage_change_7d"`
			} `json:"USD"`
		} `json:"quotes"`
	} `json:"data"`
}

var supportedBTCPriceCurrencies = []string{"USD", "EUR", "GBP", "CAD", "JPY", "AUD", "CHF", "CNY", "HKD", "SGD"}

func formatFiatRate(raw string) (string, error) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return "", fmt.Errorf("invalid fiat rate %q: %w", raw, err)
	}
	return fmt.Sprintf("%.2f", value), nil
}

func (p *BTCPriceResponse) asMap() map[string]interface{} {
	out := map[string]interface{}{
		"btc_usd":    p.BTCUSD,
		"btc_eur":    p.BTCEUR,
		"btc_gbp":    p.BTCGBP,
		"btc_cad":    p.BTCCAD,
		"btc_jpy":    p.BTCJPY,
		"btc_aud":    p.BTCAUD,
		"btc_chf":    p.BTCCHF,
		"btc_cny":    p.BTCCNY,
		"btc_hkd":    p.BTCHKD,
		"btc_sgd":    p.BTCSGD,
		"updated_at": p.UpdatedAt,
	}
	if p.FearGreed != nil {
		out["fear_greed"] = p.FearGreed
	}
	if p.MarketStats != nil {
		out["market_stats"] = p.MarketStats
	}
	return out
}

func fearGreedLabel(value float64) string {
	switch {
	case value <= 25:
		return "extreme_fear"
	case value <= 45:
		return "fear"
	case value <= 75:
		return "greed"
	default:
		return "extreme_greed"
	}
}

func fetchBTCFearGreed(client *http.Client) (*BTCFearGreed, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.alternative.me/fng/?limit=1&format=json", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build fear-greed request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch fear-greed data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fear-greed provider returned status %d", resp.StatusCode)
	}

	var latest alternativeFearGreedResponse
	if err := json.NewDecoder(resp.Body).Decode(&latest); err != nil {
		return nil, fmt.Errorf("failed to decode fear-greed data: %w", err)
	}
	if len(latest.Data) == 0 {
		return nil, fmt.Errorf("fear-greed provider returned no data")
	}

	value, err := strconv.ParseFloat(latest.Data[0].Value, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid fear-greed value %q: %w", latest.Data[0].Value, err)
	}
	updatedUnix, err := strconv.ParseInt(latest.Data[0].Timestamp, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid fear-greed timestamp %q: %w", latest.Data[0].Timestamp, err)
	}
	updatedAt := time.Unix(updatedUnix, 0).UTC()

	return &BTCFearGreed{
		Value:     value,
		Label:     fearGreedLabel(value),
		Source:    "Alternative.me",
		UpdatedAt: updatedAt,
	}, nil
}

func fetchBTCMarketStats(client *http.Client) (*BTCMarketStats, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.alternative.me/v2/ticker/bitcoin/?structure=dictionary", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build market-stats request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch market stats: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("market stats provider returned status %d", resp.StatusCode)
	}

	var ticker alternativeTickerResponse
	if err := json.NewDecoder(resp.Body).Decode(&ticker); err != nil {
		return nil, fmt.Errorf("failed to decode market stats: %w", err)
	}

	bitcoin, ok := ticker.Data["1"]
	if !ok {
		return nil, fmt.Errorf("market stats provider returned no bitcoin ticker")
	}

	return &BTCMarketStats{
		Rank:         bitcoin.Rank,
		Change1hPct:  bitcoin.Quotes.USD.PercentageChange1h,
		Change24hPct: bitcoin.Quotes.USD.PercentageChange24h,
		Change7dPct:  bitcoin.Quotes.USD.PercentageChange7d,
		Volume24hUSD: bitcoin.Quotes.USD.Volume24h,
		MarketCapUSD: bitcoin.Quotes.USD.MarketCap,
		Source:       "Alternative.me",
	}, nil
}

func parseRequestedBTCCurrencies(r *http.Request) ([]string, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("currencies"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("currency"))
	}
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	seen := make(map[string]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		code := strings.ToUpper(strings.TrimSpace(part))
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		switch code {
		case "USD", "EUR", "GBP", "CAD", "JPY", "AUD", "CHF", "CNY", "HKD", "SGD":
			out = append(out, code)
		default:
			return nil, fmt.Errorf("unsupported currency %q; supported: %s", code, strings.Join(supportedBTCPriceCurrencies, ", "))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid currencies requested; supported: %s", strings.Join(supportedBTCPriceCurrencies, ", "))
	}
	return out, nil
}

var (
	priceCache      *BTCPriceResponse
	priceCacheMu    sync.RWMutex
	priceCacheTTL   = 60 * time.Second
	priceLastUpdate time.Time
)

// BTCPrice handles /api/btc-price
// Cost: 1 sat
func (h *Handler) BTCPrice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use GET"})
		return
	}

	requested, err := parseRequestedBTCCurrencies(r)
	if err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	h.executeHandler(w, r, "/api/btc-price", 1, func() (interface{}, error) {
		price, err := getBTCPrice()
		if err != nil {
			return nil, err
		}
		if len(requested) == 0 {
			return price.asMap(), nil
		}

		all := price.asMap()
		filtered := map[string]interface{}{
			"updated_at": price.UpdatedAt,
		}
		if price.FearGreed != nil {
			filtered["fear_greed"] = price.FearGreed
		}
		if price.MarketStats != nil {
			filtered["market_stats"] = price.MarketStats
		}
		for _, code := range requested {
			filtered["btc_"+strings.ToLower(code)] = all["btc_"+strings.ToLower(code)]
		}
		return filtered, nil
	})
}

func getBTCPrice() (*BTCPriceResponse, error) {
	priceCacheMu.RLock()
	if priceCache != nil && time.Since(priceLastUpdate) < priceCacheTTL {
		defer priceCacheMu.RUnlock()
		return priceCache, nil
	}
	priceCacheMu.RUnlock()

	priceCacheMu.Lock()
	defer priceCacheMu.Unlock()

	// Re-check after acquiring lock
	if priceCache != nil && time.Since(priceLastUpdate) < priceCacheTTL {
		return priceCache, nil
	}

	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "https://api.coinbase.com/v2/exchange-rates?currency=BTC", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build price request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch price: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("price provider returned status %d", resp.StatusCode)
	}

	var cb coinbaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&cb); err != nil {
		return nil, fmt.Errorf("failed to decode price: %w", err)
	}

	formatted := make(map[string]string, len(supportedBTCPriceCurrencies))
	for _, code := range supportedBTCPriceCurrencies {
		raw, ok := cb.Data.Rates[code]
		if !ok {
			return nil, fmt.Errorf("price provider response missing %s rate", code)
		}
		value, err := formatFiatRate(raw)
		if err != nil {
			return nil, err
		}
		formatted[code] = value
	}

	newPrice := &BTCPriceResponse{
		BTCUSD:    formatted["USD"],
		BTCEUR:    formatted["EUR"],
		BTCGBP:    formatted["GBP"],
		BTCCAD:    formatted["CAD"],
		BTCJPY:    formatted["JPY"],
		BTCAUD:    formatted["AUD"],
		BTCCHF:    formatted["CHF"],
		BTCCNY:    formatted["CNY"],
		BTCHKD:    formatted["HKD"],
		BTCSGD:    formatted["SGD"],
		UpdatedAt: time.Now().UTC(),
	}

	if marketStats, err := fetchBTCMarketStats(client); err == nil {
		newPrice.MarketStats = marketStats
	}
	if fearGreed, err := fetchBTCFearGreed(client); err == nil {
		newPrice.FearGreed = fearGreed
	}

	priceCache = newPrice
	priceLastUpdate = time.Now()

	return newPrice, nil
}
