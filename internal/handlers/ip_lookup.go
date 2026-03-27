package handlers

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/oschwald/geoip2-golang"
)

// IPRequest is what the consumer sends
type IPRequest struct {
	IP string `json:"ip"`
}

// IPResponse is the geolocation data
type IPResponse struct {
	IP          string  `json:"ip"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Region      string  `json:"region"`
	City        string  `json:"city"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	ISP         string  `json:"isp"`
	Org         string  `json:"org"`
	AS          string  `json:"as"`
}

// GeoReaders holds opened DB-IP Lite/MMDB readers.
type GeoReaders struct {
	City *geoip2.Reader
	ASN  *geoip2.Reader
}

// OpenGeoDBs opens the configured MMDB GeoIP databases. Returns nil readers for
// any file that doesn't exist (handler will return an error on lookup).
func OpenGeoDBs(cityPath, asnPath string) *GeoReaders {
	g := &GeoReaders{}

	if cityPath != "" {
		r, err := geoip2.Open(cityPath)
		if err != nil {
			log.Printf("warning: could not open GeoIP city database at %s: %v", cityPath, err)
		} else {
			g.City = r
			log.Printf("GeoIP city database loaded from %s", cityPath)
		}
	}

	if asnPath != "" {
		r, err := geoip2.Open(asnPath)
		if err != nil {
			log.Printf("warning: could not open GeoIP ASN database at %s: %v", asnPath, err)
		} else {
			g.ASN = r
			log.Printf("GeoIP ASN database loaded from %s", asnPath)
		}
	}

	return g
}

// Close closes the GeoIP database readers.
func (g *GeoReaders) Close() {
	if g == nil {
		return
	}
	if g.City != nil {
		g.City.Close()
	}
	if g.ASN != nil {
		g.ASN.Close()
	}
}

const ipLookupCacheTTL = 24 * time.Hour

type ipLookupCacheEntry struct {
	response  *IPResponse
	expiresAt time.Time
}

var ipLookupCache = struct {
	mu    sync.RWMutex
	items map[string]ipLookupCacheEntry
}{
	items: make(map[string]ipLookupCacheEntry),
}

// IPLookup handles /api/ip-lookup
// Cost: 3 sats
// Uses self-hosted MMDB GeoIP databases
func (h *Handler) IPLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req IPRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"ip\": \"8.8.8.8\"}"})
		return
	}

	if req.IP == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "ip is required"})
		return
	}

	if net.ParseIP(req.IP) == nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid IP address"})
		return
	}

	h.executeHandler(w, r, "/api/ip-lookup", 3, func() (interface{}, error) {
		return doIPLookup(h.Geo, req.IP)
	})
}

func doIPLookup(geo *GeoReaders, ip string) (*IPResponse, error) {
	if cached := getCachedIPLookup(ip); cached != nil {
		return cached, nil
	}

	if geo == nil || geo.City == nil {
		return nil, fmt.Errorf("GeoIP database not available")
	}

	parsed := net.ParseIP(ip)
	if parsed == nil {
		return nil, fmt.Errorf("invalid IP address")
	}

	city, err := geo.City.City(parsed)
	if err != nil {
		return nil, fmt.Errorf("GeoIP lookup failed: %w", err)
	}

	result := &IPResponse{
		IP:          ip,
		Country:     city.Country.Names["en"],
		CountryCode: city.Country.IsoCode,
		City:        city.City.Names["en"],
		Lat:         city.Location.Latitude,
		Lon:         city.Location.Longitude,
	}

	// Use the most specific subdivision as the region
	if len(city.Subdivisions) > 0 {
		result.Region = city.Subdivisions[0].Names["en"]
	}

	// Add ISP/ASN data if the ASN database is loaded
	if geo.ASN != nil {
		asn, err := geo.ASN.ASN(parsed)
		if err == nil {
			result.ISP = asn.AutonomousSystemOrganization
			result.Org = asn.AutonomousSystemOrganization
			if asn.AutonomousSystemNumber > 0 {
				result.AS = fmt.Sprintf("AS%d %s", asn.AutonomousSystemNumber, asn.AutonomousSystemOrganization)
			}
		}
	}

	setCachedIPLookup(ip, result)
	return result, nil
}

func getCachedIPLookup(ip string) *IPResponse {
	ipLookupCache.mu.RLock()
	entry, ok := ipLookupCache.items[ip]
	ipLookupCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	return &clone
}

func setCachedIPLookup(ip string, response *IPResponse) {
	clone := *response
	ipLookupCache.mu.Lock()
	ipLookupCache.items[ip] = ipLookupCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(ipLookupCacheTTL),
	}
	ipLookupCache.mu.Unlock()
}
