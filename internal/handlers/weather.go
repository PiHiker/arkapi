package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// WeatherRequest is what the consumer sends
type WeatherRequest struct {
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	City string  `json:"city,omitempty"` // optional - we'll geocode it
}

// WeatherResponse is the combined weather data
type WeatherResponse struct {
	Location  LocationInfo   `json:"location"`
	Current   interface{}    `json:"current"`
	Hourly    interface{}    `json:"hourly,omitempty"`
	Daily     interface{}    `json:"daily,omitempty"`
}

type LocationInfo struct {
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	Name string  `json:"name,omitempty"`
}

const weatherCacheTTL = 15 * time.Minute

type weatherCacheEntry struct {
	response  *WeatherResponse
	expiresAt time.Time
}

var weatherCache = struct {
	mu    sync.RWMutex
	items map[string]weatherCacheEntry
}{
	items: make(map[string]weatherCacheEntry),
}

// Weather handles /api/weather
// Cost: 3 sats
// Proxies Open-Meteo's free API and bundles current + forecast
func (h *Handler) Weather(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req WeatherRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"lat\": 40.7, \"lon\": -74.0} or {\"city\": \"New York\"}"})
		return
	}

	// If city is provided, geocode it first
	if req.City != "" && req.Lat == 0 && req.Lon == 0 {
		lat, lon, name, err := geocode(req.City)
		if err != nil {
			sendJSON(w, http.StatusBadRequest, map[string]string{"error": "could not find the requested city"})
			return
		}
		req.Lat = lat
		req.Lon = lon
		req.City = name
	}

	if req.Lat == 0 && req.Lon == 0 {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "provide lat/lon or city"})
		return
	}

	h.executeHandler(w, r, "/api/weather", 3, func() (interface{}, error) {
		return doWeather(req.Lat, req.Lon, req.City)
	})
}

func doWeather(lat, lon float64, city string) (*WeatherResponse, error) {
	cacheKey := fmt.Sprintf("%.4f|%.4f|%s", lat, lon, strings.ToLower(strings.TrimSpace(city)))
	if cached := getCachedWeather(cacheKey); cached != nil {
		return cached, nil
	}

	// Call Open-Meteo forecast API with current + daily data
	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f"+
			"&current=temperature_2m,relative_humidity_2m,apparent_temperature,precipitation,weather_code,wind_speed_10m,wind_direction_10m"+
			"&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum,sunrise,sunset"+
			"&timezone=auto&forecast_days=7",
		lat, lon,
	)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("weather API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("weather API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read weather response: %w", err)
	}

	// Parse the raw JSON from Open-Meteo
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse weather data: %w", err)
	}

	result := &WeatherResponse{
		Location: LocationInfo{
			Lat:  lat,
			Lon:  lon,
			Name: city,
		},
		Current: raw["current"],
		Daily:   raw["daily"],
	}

	setCachedWeather(cacheKey, result)
	return result, nil
}

func getCachedWeather(key string) *WeatherResponse {
	weatherCache.mu.RLock()
	entry, ok := weatherCache.items[key]
	weatherCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	return &clone
}

func setCachedWeather(key string, response *WeatherResponse) {
	clone := *response
	weatherCache.mu.Lock()
	weatherCache.items[key] = weatherCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(weatherCacheTTL),
	}
	weatherCache.mu.Unlock()
}

// geocode uses Open-Meteo's free geocoding API to convert city name to coordinates
func geocode(city string) (float64, float64, string, error) {
	url := fmt.Sprintf("https://geocoding-api.open-meteo.com/v1/search?name=%s&count=1", url.QueryEscape(city))

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, 0, "", err
	}
	defer resp.Body.Close()

	var data struct {
		Results []struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			Name      string  `json:"name"`
			Country   string  `json:"country"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, 0, "", err
	}

	if len(data.Results) == 0 {
		return 0, 0, "", fmt.Errorf("city not found")
	}

	r := data.Results[0]
	name := fmt.Sprintf("%s, %s", r.Name, r.Country)
	return r.Latitude, r.Longitude, name, nil
}
