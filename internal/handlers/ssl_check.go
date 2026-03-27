package handlers

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// SSLRequest is what the consumer sends
type SSLRequest struct {
	Domain string `json:"domain"`
	Port   int    `json:"port,omitempty"` // defaults to 443
}

// SSLResponse is the certificate analysis
type SSLResponse struct {
	Domain        string   `json:"domain"`
	Valid         bool     `json:"valid"`
	Issuer        string   `json:"issuer"`
	Subject       string   `json:"subject"`
	NotBefore     string   `json:"not_before"`
	NotAfter      string   `json:"not_after"`
	DaysRemaining int      `json:"days_remaining"`
	Protocol      string   `json:"protocol"`
	CipherSuite   string   `json:"cipher_suite,omitempty"`
	SANs          []string `json:"sans,omitempty"`
	Chain         []string `json:"chain,omitempty"`
	Expired       bool     `json:"expired"`
}

const sslCheckCacheTTL = time.Hour

type sslCacheKey struct {
	Domain string
	Port   int
}

type sslCacheEntry struct {
	response  *SSLResponse
	expiresAt time.Time
}

var sslCheckCache = struct {
	mu    sync.RWMutex
	items map[sslCacheKey]sslCacheEntry
}{
	items: make(map[sslCacheKey]sslCacheEntry),
}

// SSLCheck handles /api/ssl-check
// Cost: 5 sats
// Uses Go's native TLS library — no need to shell out to openssl
func (h *Handler) SSLCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req SSLRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"domain\": \"example.com\"}"})
		return
	}

	if req.Domain == "" || !isValidDomain(req.Domain) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid domain"})
		return
	}

	if req.Port == 0 {
		req.Port = 443
	}
	if !isAllowedTLSPort(req.Port) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "port not allowed — use 443, 465, 636, 853, 993, 995, 8443, or 8883"})
		return
	}

	h.executeHandler(w, r, "/api/ssl-check", 5, func() (interface{}, error) {
		return doSSLCheck(req.Domain, req.Port)
	})
}

// isAllowedTLSPort restricts port scanning to common TLS ports.
func isAllowedTLSPort(port int) bool {
	switch port {
	case 443, 465, 636, 853, 993, 995, 8443, 8883:
		return true
	}
	return false
}

func doSSLCheck(domain string, port int) (*SSLResponse, error) {
	if cached := getCachedSSLCheck(domain, port); cached != nil {
		return cached, nil
	}

	addr := fmt.Sprintf("%s:%d", domain, port)

	// Connect with a timeout
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		// We want to inspect the cert even if it's invalid
		InsecureSkipVerify: true,
	})
	if err != nil {
		return nil, fmt.Errorf("TLS connection failed: %w", err)
	}
	defer conn.Close()

	// Get the connection state
	state := conn.ConnectionState()

	if len(state.PeerCertificates) == 0 {
		return nil, fmt.Errorf("no certificates presented by %s", domain)
	}

	cert := state.PeerCertificates[0]
	now := time.Now()

	result := &SSLResponse{
		Domain:        domain,
		Valid:         now.After(cert.NotBefore) && now.Before(cert.NotAfter),
		Issuer:        cert.Issuer.String(),
		Subject:       cert.Subject.String(),
		NotBefore:     cert.NotBefore.UTC().Format(time.RFC3339),
		NotAfter:      cert.NotAfter.UTC().Format(time.RFC3339),
		DaysRemaining: int(time.Until(cert.NotAfter).Hours() / 24),
		Protocol:      tlsVersionName(state.Version),
		Expired:       now.After(cert.NotAfter),
	}

	// Cipher suite
	result.CipherSuite = tls.CipherSuiteName(state.CipherSuite)

	// Subject Alternative Names
	result.SANs = cert.DNSNames

	// Certificate chain
	for _, c := range state.PeerCertificates {
		result.Chain = append(result.Chain, c.Subject.CommonName)
	}

	setCachedSSLCheck(domain, port, result)
	return result, nil
}

func getCachedSSLCheck(domain string, port int) *SSLResponse {
	key := sslCacheKey{Domain: domain, Port: port}
	sslCheckCache.mu.RLock()
	entry, ok := sslCheckCache.items[key]
	sslCheckCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	if entry.response.SANs != nil {
		clone.SANs = append([]string(nil), entry.response.SANs...)
	}
	if entry.response.Chain != nil {
		clone.Chain = append([]string(nil), entry.response.Chain...)
	}
	return &clone
}

func setCachedSSLCheck(domain string, port int, response *SSLResponse) {
	clone := *response
	if response.SANs != nil {
		clone.SANs = append([]string(nil), response.SANs...)
	}
	if response.Chain != nil {
		clone.Chain = append([]string(nil), response.Chain...)
	}
	sslCheckCache.mu.Lock()
	sslCheckCache.items[sslCacheKey{Domain: domain, Port: port}] = sslCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(sslCheckCacheTTL),
	}
	sslCheckCache.mu.Unlock()
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("unknown (0x%04x)", v)
	}
}
