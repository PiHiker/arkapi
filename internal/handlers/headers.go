package handlers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// HeadersRequest is what the consumer sends
type HeadersRequest struct {
	URL string `json:"url"`
}

// SecurityHeader is a single header check result
type SecurityHeader struct {
	Header  string `json:"header"`
	Value   string `json:"value"`
	Present bool   `json:"present"`
	Rating  string `json:"rating"` // "good", "warning", "bad", "info"
	Note    string `json:"note"`
}

// HeadersResponse is the full analysis
type HeadersResponse struct {
	URL              string            `json:"url"`
	StatusCode       int               `json:"status_code"`
	Server           string            `json:"server,omitempty"`
	SecurityHeaders  []SecurityHeader  `json:"security_headers"`
	Score            int               `json:"score"`     // 0-100
	Grade            string            `json:"grade"`     // A, B, C, D, F
	AllHeaders       map[string]string `json:"all_headers"`
}

const headersCacheTTL = 10 * time.Minute

type headersCacheEntry struct {
	response  *HeadersResponse
	expiresAt time.Time
}

var headersCache = struct {
	mu    sync.RWMutex
	items map[string]headersCacheEntry
}{
	items: make(map[string]headersCacheEntry),
}

// Headers handles /api/headers
// Cost: 3 sats
func (h *Handler) Headers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req HeadersRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"url\": \"https://example.com\"}"})
		return
	}

	if req.URL == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}

	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		req.URL = "https://" + req.URL
	}
	pinnedIP, err := validateSafeURL(req.URL)
	if err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	h.executeHandler(w, r, "/api/headers", 3, func() (interface{}, error) {
		return doHeaders(req.URL, pinnedIP)
	})
}

func doHeaders(url string, pinnedIP net.IP) (*HeadersResponse, error) {
	if cached := getCachedHeaders(url); cached != nil {
		return cached, nil
	}

	// Use a pinned dialer to prevent DNS rebinding attacks
	transport := &http.Transport{
		DialContext: pinnedDialer(pinnedIP),
	}
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
		// Don't follow redirects — we want the headers from the first response
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Head(url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	result := &HeadersResponse{
		URL:        url,
		StatusCode: resp.StatusCode,
		Server:     resp.Header.Get("Server"),
		AllHeaders: make(map[string]string),
	}

	// Collect all headers
	for k, v := range resp.Header {
		result.AllHeaders[k] = strings.Join(v, "; ")
	}

	// Check security headers
	score := 0
	maxScore := 0

	checks := []struct {
		header string
		weight int
		check  func(string) (string, string) // returns (rating, note)
	}{
		{"Strict-Transport-Security", 15, checkHSTS},
		{"Content-Security-Policy", 20, checkCSP},
		{"X-Content-Type-Options", 10, checkXCTO},
		{"X-Frame-Options", 10, checkXFO},
		{"X-XSS-Protection", 5, checkXXSS},
		{"Referrer-Policy", 10, checkReferrer},
		{"Permissions-Policy", 10, checkPermissions},
		{"Content-Type", 5, checkContentType},
		{"X-Powered-By", 5, checkPoweredBy},
		{"Server", 10, checkServer},
	}

	for _, c := range checks {
		maxScore += c.weight
		value := resp.Header.Get(c.header)
		present := value != ""
		rating, note := c.check(value)

		if rating == "good" {
			score += c.weight
		} else if rating == "warning" {
			score += c.weight / 2
		}

		result.SecurityHeaders = append(result.SecurityHeaders, SecurityHeader{
			Header:  c.header,
			Value:   value,
			Present: present,
			Rating:  rating,
			Note:    note,
		})
	}

	// Calculate final score and grade
	result.Score = (score * 100) / maxScore
	result.Grade = scoreToGrade(result.Score)

	setCachedHeaders(url, result)
	return result, nil
}

func getCachedHeaders(targetURL string) *HeadersResponse {
	headersCache.mu.RLock()
	entry, ok := headersCache.items[targetURL]
	headersCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	if entry.response.SecurityHeaders != nil {
		clone.SecurityHeaders = append([]SecurityHeader(nil), entry.response.SecurityHeaders...)
	}
	if entry.response.AllHeaders != nil {
		clone.AllHeaders = make(map[string]string, len(entry.response.AllHeaders))
		for k, v := range entry.response.AllHeaders {
			clone.AllHeaders[k] = v
		}
	}
	return &clone
}

func setCachedHeaders(targetURL string, response *HeadersResponse) {
	clone := *response
	if response.SecurityHeaders != nil {
		clone.SecurityHeaders = append([]SecurityHeader(nil), response.SecurityHeaders...)
	}
	if response.AllHeaders != nil {
		clone.AllHeaders = make(map[string]string, len(response.AllHeaders))
		for k, v := range response.AllHeaders {
			clone.AllHeaders[k] = v
		}
	}
	headersCache.mu.Lock()
	headersCache.items[targetURL] = headersCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(headersCacheTTL),
	}
	headersCache.mu.Unlock()
}

// validateSafeURL resolves the URL's host and returns a validated public IP.
// Callers must use the returned IP to connect, preventing DNS rebinding.
func validateSafeURL(rawURL string) (net.IP, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("only http and https URLs are allowed")
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("invalid URL host")
	}
	if strings.EqualFold(host, "localhost") {
		return nil, fmt.Errorf("target host is not allowed")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve target host")
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("failed to resolve target host")
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return nil, fmt.Errorf("target host resolves to a private or reserved address")
		}
	}
	return ips[0], nil
}

// pinnedDialer returns a DialContext that always connects to pinnedIP,
// ignoring DNS, to prevent DNS rebinding attacks.
func pinnedDialer(pinnedIP net.IP) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		pinned := net.JoinHostPort(pinnedIP.String(), port)
		d := net.Dialer{Timeout: 10 * time.Second}
		return d.DialContext(ctx, network, pinned)
	}
}

func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	return true
}

func checkHSTS(v string) (string, string) {
	if v == "" {
		return "bad", "missing — site vulnerable to protocol downgrade attacks"
	}
	if strings.Contains(v, "max-age") {
		return "good", "present"
	}
	return "warning", "present but may be misconfigured"
}

func checkCSP(v string) (string, string) {
	if v == "" {
		return "bad", "missing — no content security policy"
	}
	return "good", "present"
}

func checkXCTO(v string) (string, string) {
	if v == "" {
		return "bad", "missing — browser may MIME-sniff responses"
	}
	if v == "nosniff" {
		return "good", "nosniff set"
	}
	return "warning", "present but unexpected value"
}

func checkXFO(v string) (string, string) {
	if v == "" {
		return "warning", "missing — page can be embedded in iframes"
	}
	return "good", "present"
}

func checkXXSS(v string) (string, string) {
	if v == "" {
		return "info", "not set — modern browsers use CSP instead"
	}
	return "info", "legacy header"
}

func checkReferrer(v string) (string, string) {
	if v == "" {
		return "warning", "missing — full URLs may leak in referrer headers"
	}
	return "good", "present"
}

func checkPermissions(v string) (string, string) {
	if v == "" {
		return "warning", "missing — browser features not restricted"
	}
	return "good", "present"
}

func checkContentType(v string) (string, string) {
	if v == "" {
		return "warning", "missing"
	}
	return "good", "present"
}

func checkPoweredBy(v string) (string, string) {
	if v == "" {
		return "good", "not exposed — good"
	}
	return "warning", "exposed: " + v + " — consider removing"
}

func checkServer(v string) (string, string) {
	if v == "" {
		return "good", "not exposed"
	}
	// Check if it reveals version info
	if strings.Contains(v, "/") {
		return "warning", "exposes version info — consider removing"
	}
	return "info", "server type exposed but no version"
}

func scoreToGrade(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 75:
		return "B"
	case score >= 60:
		return "C"
	case score >= 40:
		return "D"
	default:
		return "F"
	}
}
