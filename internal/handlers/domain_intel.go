package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	neturl "net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type DomainIntelRequest struct {
	Domain    string `json:"domain"`
	AISummary bool   `json:"ai_summary,omitempty"`
}

type DomainIntelResponse struct {
	Domain          string                    `json:"domain"`
	AISummary       *AIChatResponse           `json:"ai_summary,omitempty"`
	Registration    *DomainRegistrationInfo   `json:"registration,omitempty"`
	Providers       *DomainProviderSummary    `json:"providers,omitempty"`
	Network         *DomainNetworkSummary     `json:"network,omitempty"`
	SecurityTXT     *SecurityTXTResponse      `json:"security_txt,omitempty"`
	RobotsTXT       *RobotsTXTResponse        `json:"robots_txt,omitempty"`
	TechFingerprint *TechFingerprintResponse  `json:"tech_fingerprint,omitempty"`
	HTTPBehavior    *HTTPBehaviorResponse     `json:"http_behavior,omitempty"`
	Subdomains      []string                  `json:"subdomains,omitempty"`
	CTSubdomains    []string                  `json:"ct_subdomains,omitempty"`
	Findings        []string                  `json:"findings,omitempty"`
	Recommendations []string                  `json:"recommendations,omitempty"`
	Cache           *DomainIntelCacheMetadata `json:"cache,omitempty"`
	DNS             *DNSResponse              `json:"dns,omitempty"`
	Whois           *WhoisResponse            `json:"whois,omitempty"`
	SSL             *SSLResponse              `json:"ssl,omitempty"`
	Headers         *HeadersResponse          `json:"headers,omitempty"`
	EmailAuth       *EmailAuthCheckResponse   `json:"email_auth,omitempty"`
	ResolvedIPs     []IPResponse              `json:"resolved_ips,omitempty"`
	Errors          map[string]string         `json:"errors,omitempty"`
}

type DomainRegistrationInfo struct {
	Registrar   string   `json:"registrar,omitempty"`
	CreatedDate string   `json:"created_date,omitempty"`
	UpdatedDate string   `json:"updated_date,omitempty"`
	ExpiryDate  string   `json:"expiry_date,omitempty"`
	NameServers []string `json:"name_servers,omitempty"`
}

type DomainProviderSummary struct {
	Registrar        string `json:"registrar,omitempty"`
	DNSProvider      string `json:"dns_provider,omitempty"`
	MailProvider     string `json:"mail_provider,omitempty"`
	CDNProvider      string `json:"cdn_provider,omitempty"`
	HostingProvider  string `json:"hosting_provider,omitempty"`
	ProxyDetected    bool   `json:"proxy_detected,omitempty"`
	BehindCloudflare bool   `json:"behind_cloudflare,omitempty"`
}

type DomainNetworkSummary struct {
	IPCount      int      `json:"ip_count"`
	ASNs         []string `json:"asns,omitempty"`
	Organizations []string `json:"organizations,omitempty"`
	Countries    []string `json:"countries,omitempty"`
	AnycastOrCDN bool     `json:"anycast_or_cdn,omitempty"`
}

type DomainIntelCacheMetadata struct {
	Cached     bool   `json:"cached"`
	TTLSeconds int    `json:"ttl_seconds"`
	ExpiresAt  string `json:"expires_at"`
}

type SecurityTXTResponse struct {
	Present            bool     `json:"present"`
	SourceURL          string   `json:"source_url,omitempty"`
	Canonical          []string `json:"canonical,omitempty"`
	Contacts           []string `json:"contacts,omitempty"`
	Emails             []string `json:"emails,omitempty"`
	Policy             []string `json:"policy,omitempty"`
	Hiring             []string `json:"hiring,omitempty"`
	Encryption         []string `json:"encryption,omitempty"`
	Acknowledgments    []string `json:"acknowledgments,omitempty"`
	PreferredLanguages []string `json:"preferred_languages,omitempty"`
	CSAF               []string `json:"csaf,omitempty"`
	Expires            string   `json:"expires,omitempty"`
}

type RobotsTXTResponse struct {
	Present    bool     `json:"present"`
	SourceURL  string   `json:"source_url,omitempty"`
	UserAgents []string `json:"user_agents,omitempty"`
	Allow      []string `json:"allow,omitempty"`
	Disallow   []string `json:"disallow,omitempty"`
	Sitemaps   []string `json:"sitemaps,omitempty"`
	CrawlDelay string   `json:"crawl_delay,omitempty"`
	Host       string   `json:"host,omitempty"`
}

type TechFingerprintResponse struct {
	CMS        string   `json:"cms,omitempty"`
	Frontend   string   `json:"frontend,omitempty"`
	Ecommerce  string   `json:"ecommerce,omitempty"`
	Generator  string   `json:"generator,omitempty"`
	Server     string   `json:"server,omitempty"`
	Detected   []string `json:"detected,omitempty"`
	FinalURL   string   `json:"final_url,omitempty"`
}

type HTTPBehaviorResponse struct {
	InitialURL    string   `json:"initial_url"`
	FinalURL      string   `json:"final_url,omitempty"`
	CanonicalHost string   `json:"canonical_host,omitempty"`
	RedirectCount int      `json:"redirect_count"`
	RedirectChain []string `json:"redirect_chain,omitempty"`
	StatusChain   []int    `json:"status_chain,omitempty"`
	HTTPSRedirect bool     `json:"https_redirect,omitempty"`
	WWWRedirect   bool     `json:"www_redirect,omitempty"`
}

const domainIntelCacheTTL = time.Hour

type domainIntelCacheEntry struct {
	response  *DomainIntelResponse
	expiresAt time.Time
}

type domainIntelCacheKey struct {
	Domain    string
	AISummary bool
}

var domainIntelCache = struct {
	mu    sync.RWMutex
	items map[domainIntelCacheKey]domainIntelCacheEntry
}{
	items: make(map[domainIntelCacheKey]domainIntelCacheEntry),
}

func (h *Handler) DomainIntel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req DomainIntelRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"domain\": \"example.com\"}"})
		return
	}

	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	if req.Domain == "" || !isValidDomain(req.Domain) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid domain"})
		return
	}

	h.executeHandler(w, r, "/api/domain-intel", h.Cfg.DomainIntelCostSats, func() (interface{}, error) {
		return h.doDomainIntel(req.Domain, req.AISummary)
	})
}

func (h *Handler) doDomainIntel(domain string, withAISummary bool) (*DomainIntelResponse, error) {
	if cached := getCachedDomainIntel(domain, withAISummary); cached != nil {
		return cached, nil
	}

	resp := &DomainIntelResponse{
		Domain: domain,
		Errors: map[string]string{},
	}

	var wg sync.WaitGroup
	var mu sync.Mutex

	httpsURL := "https://" + domain
	targetURL := httpsURL

	httpBehavior, err := fetchHTTPBehavior(domain)
	if err == nil && httpBehavior != nil {
		resp.HTTPBehavior = httpBehavior
		if strings.TrimSpace(httpBehavior.FinalURL) != "" {
			targetURL = httpBehavior.FinalURL
		}
	} else if err != nil {
		resp.Errors["http_behavior"] = err.Error()
	}

	wg.Add(9)

	go func() {
		defer wg.Done()
		data, err := doDNSLookup(domain)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			resp.Errors["dns"] = err.Error()
			return
		}
		resp.DNS = data
	}()

	go func() {
		defer wg.Done()
		data, err := doWhois(domain)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			resp.Errors["whois"] = err.Error()
			return
		}
		resp.Whois = data
	}()

	go func() {
		defer wg.Done()
		data, err := doSSLCheck(domain, 443)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			resp.Errors["ssl"] = err.Error()
			return
		}
		resp.SSL = data
	}()

	go func() {
		defer wg.Done()
		pinnedIP, err := validateSafeURL(targetURL)
		if err != nil {
			mu.Lock()
			resp.Errors["headers"] = err.Error()
			mu.Unlock()
			return
		}
		data, err := doHeaders(targetURL, pinnedIP)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			resp.Errors["headers"] = err.Error()
			return
		}
		resp.Headers = data
	}()

	go func() {
		defer wg.Done()
		data, err := doEmailAuthCheck(domain, "")
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			resp.Errors["email_auth"] = err.Error()
			return
		}
		resp.EmailAuth = data
	}()

	go func() {
		defer wg.Done()
		data, err := fetchSecurityTXT(domain)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			resp.Errors["security_txt"] = err.Error()
			return
		}
		resp.SecurityTXT = data
	}()

	go func() {
		defer wg.Done()
		data, err := fetchRobotsTXT(domain)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			resp.Errors["robots_txt"] = err.Error()
			return
		}
		resp.RobotsTXT = data
	}()

	go func() {
		defer wg.Done()
		data, err := fetchTechFingerprint(targetURL)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			resp.Errors["tech_fingerprint"] = err.Error()
			return
		}
		resp.TechFingerprint = data
	}()

	go func() {
		defer wg.Done()
		data, err := fetchCTLogSubdomains(domain)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			resp.Errors["ct_subdomains"] = err.Error()
			return
		}
		resp.CTSubdomains = data
	}()

	wg.Wait()

	if resp.DNS != nil {
		resp.ResolvedIPs = h.lookupDomainIntelIPs(resp.DNS)
		if len(resp.ResolvedIPs) == 0 && h.Geo != nil && h.Geo.City != nil {
			resp.Errors["resolved_ips"] = "no geolocated public IPs found"
		}
	}

	resp.Registration = buildDomainRegistrationInfo(resp)
	resp.Providers = buildDomainProviderSummary(resp)
	resp.Network = buildDomainNetworkSummary(resp.ResolvedIPs, resp.Providers)
	resp.Subdomains = discoverLightSubdomains(domain, resp)
	resp.Findings, resp.Recommendations = buildDomainIntelInsights(resp)
	if withAISummary {
		aiSummary, err := h.buildDomainIntelAISummary(resp)
		if err != nil {
			resp.Errors["ai_summary"] = err.Error()
		} else {
			resp.AISummary = aiSummary
		}
	}

	if len(resp.Errors) == 0 {
		resp.Errors = nil
	}
	if resp.DNS == nil && resp.Whois == nil && resp.SSL == nil && resp.Headers == nil && resp.EmailAuth == nil && len(resp.ResolvedIPs) == 0 {
		return nil, fmt.Errorf("no domain intelligence data found for %s", domain)
	}

	setCachedDomainIntel(domain, withAISummary, resp)
	resp.Cache = buildDomainIntelCacheMetadata(false, time.Now().Add(domainIntelCacheTTL))
	return resp, nil
}

func (h *Handler) lookupDomainIntelIPs(dnsResp *DNSResponse) []IPResponse {
	if dnsResp == nil || h.Geo == nil || h.Geo.City == nil {
		return nil
	}

	seen := map[string]struct{}{}
	ips := make([]string, 0, 6)
	for _, qtype := range []string{"A", "AAAA"} {
		for _, record := range dnsResp.Records[qtype] {
			ip := strings.TrimSpace(record.Value)
			if net.ParseIP(ip) == nil {
				continue
			}
			if _, ok := seen[ip]; ok {
				continue
			}
			seen[ip] = struct{}{}
			ips = append(ips, ip)
			if len(ips) >= 6 {
				break
			}
		}
		if len(ips) >= 6 {
			break
		}
	}

	sort.Strings(ips)
	results := make([]IPResponse, 0, len(ips))
	for _, ip := range ips {
		data, err := doIPLookup(h.Geo, ip)
		if err != nil || data == nil {
			continue
		}
		results = append(results, *data)
	}
	return results
}

func getCachedDomainIntel(domain string, withAISummary bool) *DomainIntelResponse {
	key := domainIntelCacheKey{Domain: domain, AISummary: withAISummary}
	domainIntelCache.mu.RLock()
	entry, ok := domainIntelCache.items[key]
	domainIntelCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := cloneDomainIntelResponse(entry.response)
	clone.Cache = buildDomainIntelCacheMetadata(true, entry.expiresAt)
	return clone
}

func setCachedDomainIntel(domain string, withAISummary bool, response *DomainIntelResponse) {
	clone := cloneDomainIntelResponse(response)
	clone.Cache = nil
	domainIntelCache.mu.Lock()
	domainIntelCache.items[domainIntelCacheKey{Domain: domain, AISummary: withAISummary}] = domainIntelCacheEntry{
		response:  clone,
		expiresAt: time.Now().Add(domainIntelCacheTTL),
	}
	domainIntelCache.mu.Unlock()
}

func cloneDomainIntelResponse(resp *DomainIntelResponse) *DomainIntelResponse {
	if resp == nil {
		return nil
	}
	clone := *resp
	if resp.Subdomains != nil {
		clone.Subdomains = append([]string(nil), resp.Subdomains...)
	}
	if resp.CTSubdomains != nil {
		clone.CTSubdomains = append([]string(nil), resp.CTSubdomains...)
	}
	if resp.Findings != nil {
		clone.Findings = append([]string(nil), resp.Findings...)
	}
	if resp.Recommendations != nil {
		clone.Recommendations = append([]string(nil), resp.Recommendations...)
	}
	if resp.ResolvedIPs != nil {
		clone.ResolvedIPs = append([]IPResponse(nil), resp.ResolvedIPs...)
	}
	if resp.AISummary != nil {
		aiSummary := *resp.AISummary
		if resp.AISummary.Usage != nil {
			usage := *resp.AISummary.Usage
			aiSummary.Usage = &usage
		}
		clone.AISummary = &aiSummary
	}
	if resp.HTTPBehavior != nil {
		httpBehavior := *resp.HTTPBehavior
		if resp.HTTPBehavior.RedirectChain != nil {
			httpBehavior.RedirectChain = append([]string(nil), resp.HTTPBehavior.RedirectChain...)
		}
		if resp.HTTPBehavior.StatusChain != nil {
			httpBehavior.StatusChain = append([]int(nil), resp.HTTPBehavior.StatusChain...)
		}
		clone.HTTPBehavior = &httpBehavior
	}
	if resp.Errors != nil {
		clone.Errors = make(map[string]string, len(resp.Errors))
		for k, v := range resp.Errors {
			clone.Errors[k] = v
		}
	}
	if resp.Registration != nil {
		registration := *resp.Registration
		registration.NameServers = append([]string(nil), resp.Registration.NameServers...)
		clone.Registration = &registration
	}
	if resp.Providers != nil {
		providers := *resp.Providers
		clone.Providers = &providers
	}
	if resp.Network != nil {
		network := *resp.Network
		network.ASNs = append([]string(nil), resp.Network.ASNs...)
		network.Organizations = append([]string(nil), resp.Network.Organizations...)
		network.Countries = append([]string(nil), resp.Network.Countries...)
		clone.Network = &network
	}
	if resp.SecurityTXT != nil {
		securityTXT := *resp.SecurityTXT
		securityTXT.Canonical = append([]string(nil), resp.SecurityTXT.Canonical...)
		securityTXT.Contacts = append([]string(nil), resp.SecurityTXT.Contacts...)
		securityTXT.Emails = append([]string(nil), resp.SecurityTXT.Emails...)
		securityTXT.Policy = append([]string(nil), resp.SecurityTXT.Policy...)
		securityTXT.Hiring = append([]string(nil), resp.SecurityTXT.Hiring...)
		securityTXT.Encryption = append([]string(nil), resp.SecurityTXT.Encryption...)
		securityTXT.Acknowledgments = append([]string(nil), resp.SecurityTXT.Acknowledgments...)
		securityTXT.PreferredLanguages = append([]string(nil), resp.SecurityTXT.PreferredLanguages...)
		securityTXT.CSAF = append([]string(nil), resp.SecurityTXT.CSAF...)
		clone.SecurityTXT = &securityTXT
	}
	if resp.RobotsTXT != nil {
		robotsTXT := *resp.RobotsTXT
		robotsTXT.UserAgents = append([]string(nil), resp.RobotsTXT.UserAgents...)
		robotsTXT.Allow = append([]string(nil), resp.RobotsTXT.Allow...)
		robotsTXT.Disallow = append([]string(nil), resp.RobotsTXT.Disallow...)
		robotsTXT.Sitemaps = append([]string(nil), resp.RobotsTXT.Sitemaps...)
		clone.RobotsTXT = &robotsTXT
	}
	if resp.TechFingerprint != nil {
		techFingerprint := *resp.TechFingerprint
		techFingerprint.Detected = append([]string(nil), resp.TechFingerprint.Detected...)
		clone.TechFingerprint = &techFingerprint
	}
	if resp.Cache != nil {
		cache := *resp.Cache
		clone.Cache = &cache
	}
	return &clone
}

func buildDomainRegistrationInfo(resp *DomainIntelResponse) *DomainRegistrationInfo {
	if resp == nil || (resp.Whois == nil && resp.DNS == nil) {
		return nil
	}

	info := &DomainRegistrationInfo{}
	if resp.Whois != nil {
		info.Registrar = resp.Whois.Registrar
		info.CreatedDate = resp.Whois.CreatedDate
		info.UpdatedDate = resp.Whois.UpdatedDate
		info.ExpiryDate = resp.Whois.ExpiryDate
		info.NameServers = append(info.NameServers, resp.Whois.NameServers...)
	}
	if len(info.NameServers) == 0 && resp.DNS != nil {
		for _, record := range resp.DNS.Records["NS"] {
			if value := strings.TrimSpace(record.Value); value != "" {
				info.NameServers = append(info.NameServers, value)
			}
		}
		sort.Strings(info.NameServers)
	}
	if len(info.NameServers) > 0 {
		seen := map[string]struct{}{}
		deduped := make([]string, 0, len(info.NameServers))
		for _, nameServer := range info.NameServers {
			nameServer = strings.ToLower(strings.TrimSpace(nameServer))
			if nameServer == "" {
				continue
			}
			if _, ok := seen[nameServer]; ok {
				continue
			}
			seen[nameServer] = struct{}{}
			deduped = append(deduped, nameServer)
		}
		sort.Strings(deduped)
		info.NameServers = deduped
	}
	if info.Registrar == "" && info.CreatedDate == "" && info.UpdatedDate == "" && info.ExpiryDate == "" && len(info.NameServers) == 0 {
		return nil
	}
	return info
}

func buildDomainProviderSummary(resp *DomainIntelResponse) *DomainProviderSummary {
	if resp == nil {
		return nil
	}

	summary := &DomainProviderSummary{}
	if resp.Registration != nil {
		summary.Registrar = resp.Registration.Registrar
		summary.DNSProvider = detectDNSProvider(resp.Registration.NameServers)
	}
	if summary.DNSProvider == "" && resp.DNS != nil {
		summary.DNSProvider = detectDNSProvider(recordValues(resp.DNS.Records["NS"]))
	}
	summary.MailProvider = detectMailProvider(resp)
	summary.CDNProvider = detectCDNProvider(resp)
	summary.ProxyDetected = summary.CDNProvider != ""
	summary.BehindCloudflare = strings.EqualFold(summary.CDNProvider, "Cloudflare")
	summary.HostingProvider = detectHostingProvider(resp.ResolvedIPs, summary.CDNProvider)

	if summary.Registrar == "" && summary.DNSProvider == "" && summary.MailProvider == "" && summary.CDNProvider == "" && summary.HostingProvider == "" && !summary.ProxyDetected && !summary.BehindCloudflare {
		return nil
	}
	return summary
}

func buildDomainNetworkSummary(resolvedIPs []IPResponse, providers *DomainProviderSummary) *DomainNetworkSummary {
	if len(resolvedIPs) == 0 {
		return nil
	}

	asnSet := map[string]struct{}{}
	orgSet := map[string]struct{}{}
	countrySet := map[string]struct{}{}
	for _, ip := range resolvedIPs {
		if value := strings.TrimSpace(ip.AS); value != "" {
			asnSet[value] = struct{}{}
		}
		if value := strings.TrimSpace(ip.Org); value != "" {
			orgSet[value] = struct{}{}
		}
		if value := strings.TrimSpace(ip.Country); value != "" {
			countrySet[value] = struct{}{}
		}
	}

	summary := &DomainNetworkSummary{
		IPCount:       len(resolvedIPs),
		ASNs:          sortedKeys(asnSet),
		Organizations: sortedKeys(orgSet),
		Countries:     sortedKeys(countrySet),
	}
	if providers != nil && providers.CDNProvider != "" {
		summary.AnycastOrCDN = true
	}
	for _, org := range summary.Organizations {
		lower := strings.ToLower(org)
		if strings.Contains(lower, "cloudflare") || strings.Contains(lower, "fastly") || strings.Contains(lower, "akamai") || strings.Contains(lower, "cloudfront") {
			summary.AnycastOrCDN = true
			break
		}
	}
	return summary
}

func discoverLightSubdomains(domain string, resp *DomainIntelResponse) []string {
	discovered := map[string]struct{}{}
	add := func(host string) {
		host = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(host, ".")))
		if host == "" || host == domain || strings.HasPrefix(host, "*.") {
			return
		}
		if !strings.HasSuffix(host, "."+domain) {
			return
		}
		discovered[host] = struct{}{}
	}

	if resp != nil && resp.SSL != nil {
		for _, san := range resp.SSL.SANs {
			add(san)
		}
	}
	if resp != nil && resp.DNS != nil {
		for _, record := range resp.DNS.Records["MX"] {
			fields := strings.Fields(record.Value)
			if len(fields) > 0 {
				add(fields[len(fields)-1])
			}
		}
		for _, record := range resp.DNS.Records["NS"] {
			add(record.Value)
		}
	}

	common := []string{"www", "api", "mail", "app", "blog", "dev"}
	type discoveredHost struct {
		host string
		ok   bool
	}
	results := make(chan discoveredHost, len(common))
	var wg sync.WaitGroup
	for _, prefix := range common {
		host := prefix + "." + domain
		wg.Add(1)
		go func(candidate string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			ips, err := net.DefaultResolver.LookupHost(ctx, candidate)
			if err != nil || len(ips) == 0 {
				return
			}
			for _, ip := range ips {
				if parsed := net.ParseIP(ip); parsed != nil && isPublicIP(parsed) {
					results <- discoveredHost{host: candidate, ok: true}
					return
				}
			}
		}(host)
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.ok {
			add(result.host)
		}
	}

	if len(discovered) == 0 {
		return nil
	}
	values := sortedKeys(discovered)
	if len(values) > 12 {
		values = values[:12]
	}
	return values
}

func buildDomainIntelInsights(resp *DomainIntelResponse) ([]string, []string) {
	if resp == nil {
		return nil, nil
	}

	findings := []string{}
	recommendations := []string{}
	addFinding := func(value string) {
		if value != "" {
			findings = append(findings, value)
		}
	}
	addRecommendation := func(value string) {
		if value != "" && !containsString(recommendations, value) {
			recommendations = append(recommendations, value)
		}
	}

	if resp.Headers != nil {
		for _, header := range resp.Headers.SecurityHeaders {
			switch header.Header {
			case "Content-Security-Policy":
				if !header.Present {
					addFinding("Content-Security-Policy is missing on the checked page.")
					addRecommendation("Add a Content-Security-Policy header to reduce XSS and content injection risk.")
				}
			case "Strict-Transport-Security":
				if header.Present && strings.Contains(strings.ToLower(header.Value), "max-age=0") {
					addFinding("Strict-Transport-Security is present but disabled with max-age=0.")
					addRecommendation("Set a real HSTS max-age, ideally 31536000 with includeSubDomains once HTTPS is stable.")
				}
			}
		}
		if resp.Headers.Grade == "C" || resp.Headers.Grade == "D" || resp.Headers.Grade == "F" {
			addFinding(fmt.Sprintf("HTTP security headers grade is %s.", resp.Headers.Grade))
		}
	}

	if resp.EmailAuth != nil {
		switch resp.EmailAuth.DMARC.Mode {
		case "monitoring", "none":
			addFinding("DMARC policy is monitoring-only and does not quarantine or reject unauthenticated mail.")
			addRecommendation("Move DMARC to quarantine or reject after validating legitimate mail flow.")
		}
		if !resp.EmailAuth.DKIM.Present {
			addFinding("No DKIM selector was detected.")
			addRecommendation("Publish at least one DKIM selector for the service that sends outbound mail.")
		}
		switch resp.EmailAuth.SPF.Mode {
		case "softfail":
			addFinding("SPF ends in ~all softfail.")
			addRecommendation("Tighten SPF to -all once your authorized senders are complete.")
		case "permissive":
			addFinding("SPF is overly permissive.")
			addRecommendation("Replace +all with a restrictive SPF policy.")
		}
	}

	if resp.SSL != nil {
		if !resp.SSL.Valid || resp.SSL.Expired {
			addFinding("TLS certificate is expired or currently invalid.")
			addRecommendation("Renew or replace the TLS certificate immediately.")
		} else if resp.SSL.DaysRemaining >= 0 && resp.SSL.DaysRemaining < 30 {
			addFinding(fmt.Sprintf("TLS certificate expires in %d days.", resp.SSL.DaysRemaining))
			addRecommendation("Renew the TLS certificate before it reaches expiry.")
		}
	}

	if resp.SecurityTXT != nil && resp.SecurityTXT.Present {
		if len(resp.SecurityTXT.Emails) > 0 {
			addFinding(fmt.Sprintf("security.txt is published with %d contact email(s).", len(resp.SecurityTXT.Emails)))
		} else {
			addFinding("security.txt is published for vulnerability disclosure.")
		}
	} else {
		addFinding("No security.txt policy file was detected.")
		addRecommendation("Publish /.well-known/security.txt with at least a Contact field for security disclosures.")
	}

	if resp.RobotsTXT != nil && resp.RobotsTXT.Present {
		if len(resp.RobotsTXT.Sitemaps) > 0 {
			addFinding(fmt.Sprintf("robots.txt publishes %d sitemap URL(s).", len(resp.RobotsTXT.Sitemaps)))
		}
	} else {
		addFinding("No robots.txt file was detected.")
		addRecommendation("Publish a robots.txt file with at least a sitemap reference if the site should be indexed.")
	}

	if resp.TechFingerprint != nil {
		if resp.TechFingerprint.CMS != "" {
			addFinding(fmt.Sprintf("Tech fingerprint suggests %s.", resp.TechFingerprint.CMS))
		}
		if resp.TechFingerprint.Frontend != "" {
			addFinding(fmt.Sprintf("Frontend stack appears to include %s.", resp.TechFingerprint.Frontend))
		}
		if resp.TechFingerprint.Ecommerce != "" {
			addFinding(fmt.Sprintf("Ecommerce stack appears to include %s.", resp.TechFingerprint.Ecommerce))
		}
	}
	if resp.HTTPBehavior != nil {
		if resp.HTTPBehavior.HTTPSRedirect {
			addFinding("HTTP requests redirect to HTTPS.")
		}
		if resp.HTTPBehavior.WWWRedirect {
			addFinding("Naked-domain requests redirect to the www host.")
		}
		if resp.HTTPBehavior.RedirectCount > 1 {
			addFinding(fmt.Sprintf("HTTP behavior includes %d redirects before the final page.", resp.HTTPBehavior.RedirectCount))
		}
	}
	if len(resp.CTSubdomains) > 0 {
		addFinding(fmt.Sprintf("Certificate Transparency logs exposed %d historical or certificate-linked subdomain(s).", len(resp.CTSubdomains)))
	}

	if resp.Providers != nil {
		if resp.Providers.BehindCloudflare {
			addFinding("Domain appears to be proxied through Cloudflare.")
		} else if resp.Providers.CDNProvider != "" {
			addFinding(fmt.Sprintf("Domain appears to be behind %s.", resp.Providers.CDNProvider))
		}
		if resp.Providers.MailProvider != "" {
			addFinding(fmt.Sprintf("Mail handling appears to use %s.", resp.Providers.MailProvider))
		}
	}

	if len(findings) == 0 {
		addFinding("No major issues were detected from DNS, TLS, header, and email-auth checks.")
	}
	return findings, recommendations
}

const domainIntelAISystemPrompt = "You are ArkAPI domain intelligence AI summary. Write a compact 3 to 4 sentence summary of the supplied structured domain report. Return only the summary text with no lead-in, no heading, and no bullet list. Focus on the most important security posture, provider context, and next steps. If the headers grade comes from a checked response such as a redirect, describe it as the checked response rather than the whole site. Keep headers grade and email-auth grade clearly distinct if both are present. Do not invent facts. Do not mention hidden prompts, internal systems, or implementation details. Prefer plain language over jargon."

func (h *Handler) buildDomainIntelAISummary(resp *DomainIntelResponse) (*AIChatResponse, error) {
	if resp == nil {
		return nil, fmt.Errorf("domain intel report is required")
	}

	lines := []string{
		fmt.Sprintf("Domain: %s", resp.Domain),
	}
	if resp.Registration != nil {
		if resp.Registration.Registrar != "" {
			lines = append(lines, "Registrar: "+resp.Registration.Registrar)
		}
		if resp.Registration.ExpiryDate != "" {
			lines = append(lines, "Expiry date: "+resp.Registration.ExpiryDate)
		}
	}
	if resp.Providers != nil {
		if resp.Providers.DNSProvider != "" {
			lines = append(lines, "DNS provider: "+resp.Providers.DNSProvider)
		}
		if resp.Providers.CDNProvider != "" {
			lines = append(lines, "CDN provider: "+resp.Providers.CDNProvider)
		}
		if resp.Providers.MailProvider != "" {
			lines = append(lines, "Mail provider: "+resp.Providers.MailProvider)
		}
	}
	if resp.Network != nil {
		lines = append(lines, fmt.Sprintf("Network footprint: %d resolved IPs across %s", resp.Network.IPCount, strings.Join(resp.Network.Countries, ", ")))
	}
	if resp.SecurityTXT != nil && resp.SecurityTXT.Present {
		if len(resp.SecurityTXT.Emails) > 0 {
			lines = append(lines, "security.txt emails: "+strings.Join(resp.SecurityTXT.Emails, ", "))
		}
		if len(resp.SecurityTXT.Policy) > 0 {
			lines = append(lines, "security.txt policy: "+strings.Join(resp.SecurityTXT.Policy, ", "))
		}
	}
	if resp.RobotsTXT != nil && resp.RobotsTXT.Present {
		if len(resp.RobotsTXT.Sitemaps) > 0 {
			lines = append(lines, "robots.txt sitemaps: "+strings.Join(resp.RobotsTXT.Sitemaps, ", "))
		}
	}
	if resp.TechFingerprint != nil {
		if resp.TechFingerprint.CMS != "" {
			lines = append(lines, "CMS: "+resp.TechFingerprint.CMS)
		}
		if resp.TechFingerprint.Frontend != "" {
			lines = append(lines, "Frontend: "+resp.TechFingerprint.Frontend)
		}
		if resp.TechFingerprint.Ecommerce != "" {
			lines = append(lines, "Ecommerce: "+resp.TechFingerprint.Ecommerce)
		}
	}
	if resp.HTTPBehavior != nil {
		lines = append(lines, fmt.Sprintf("HTTP behavior: final_url=%s, redirects=%d, https_redirect=%t, www_redirect=%t", resp.HTTPBehavior.FinalURL, resp.HTTPBehavior.RedirectCount, resp.HTTPBehavior.HTTPSRedirect, resp.HTTPBehavior.WWWRedirect))
	}
	if len(resp.CTSubdomains) > 0 {
		lines = append(lines, "CT log subdomains: "+strings.Join(resp.CTSubdomains, ", "))
	}
	if resp.SSL != nil {
		lines = append(lines, fmt.Sprintf("TLS: valid=%t, days_remaining=%d, protocol=%s", resp.SSL.Valid, resp.SSL.DaysRemaining, resp.SSL.Protocol))
	}
	if resp.Headers != nil {
		lines = append(lines, fmt.Sprintf("Headers grade: %s (%d)", resp.Headers.Grade, resp.Headers.Score))
	}
	if resp.EmailAuth != nil {
		lines = append(lines, fmt.Sprintf("Email auth grade: %s", resp.EmailAuth.Grade))
	}
	if len(resp.Subdomains) > 0 {
		lines = append(lines, "Light subdomains: "+strings.Join(resp.Subdomains, ", "))
	}
	if len(resp.Findings) > 0 {
		lines = append(lines, "Findings:")
		for i, finding := range resp.Findings {
			if i >= 6 {
				break
			}
			lines = append(lines, "- "+finding)
		}
	}
	if len(resp.Recommendations) > 0 {
		lines = append(lines, "Recommendations:")
		for i, recommendation := range resp.Recommendations {
			if i >= 5 {
				break
			}
			lines = append(lines, "- "+recommendation)
		}
	}

	messages := []AIChatMessage{
		{
			Role:    "user",
			Content: "Summarize this domain intelligence report:\n\n" + strings.Join(lines, "\n"),
		},
	}
	return h.doAIChatWithSystemPrompt(messages, domainIntelAISystemPrompt)
}

func buildDomainIntelCacheMetadata(cached bool, expiresAt time.Time) *DomainIntelCacheMetadata {
	return &DomainIntelCacheMetadata{
		Cached:     cached,
		TTLSeconds: int(time.Until(expiresAt).Seconds()),
		ExpiresAt:  expiresAt.UTC().Format(time.RFC3339),
	}
}

func detectDNSProvider(nameServers []string) string {
	return detectProviderFromHosts(nameServers, map[string]string{
		"ns.cloudflare.com":        "Cloudflare",
		"awsdns-":                  "Amazon Route 53",
		"dns.google":               "Google Cloud DNS",
		"domaincontrol.com":        "GoDaddy",
		"digitalocean.com":         "DigitalOcean",
		"ultradns":                 "UltraDNS",
		"azure-dns":                "Azure DNS",
		"cloudns":                  "ClouDNS",
		"nsone.net":                "NS1",
	})
}

func detectMailProvider(resp *DomainIntelResponse) string {
	if resp == nil {
		return ""
	}

	mxHosts := []string{}
	if resp.DNS != nil {
		for _, record := range resp.DNS.Records["MX"] {
			fields := strings.Fields(record.Value)
			if len(fields) > 0 {
				mxHosts = append(mxHosts, fields[len(fields)-1])
			}
		}
	}

	if provider := detectProviderFromHosts(mxHosts, map[string]string{
		"mx.cloudflare.net":             "Cloudflare Email Routing",
		"google.com":                    "Google Workspace",
		"googlemail.com":                "Google Workspace",
		"mail.protection.outlook.com":   "Microsoft 365",
		"protonmail.ch":                 "Proton Mail",
		"zoho.com":                      "Zoho Mail",
		"messagingengine.com":           "Fastmail",
		"yahoodns.net":                  "Yahoo Mail",
	}); provider != "" {
		return provider
	}

	if resp.EmailAuth != nil && resp.EmailAuth.SPF.Record != "" {
		return detectProviderFromText(resp.EmailAuth.SPF.Record, map[string]string{
			"_spf.mx.cloudflare.net":       "Cloudflare Email Routing",
			"_spf.google.com":              "Google Workspace",
			"spf.protection.outlook.com":   "Microsoft 365",
			"_spf.protonmail.ch":           "Proton Mail",
			"zoho.com":                     "Zoho Mail",
			"spf.mandrillapp.com":          "Mailchimp Transactional",
			"sendgrid.net":                 "SendGrid",
			"mailgun.org":                  "Mailgun",
		})
	}

	return ""
}

func detectCDNProvider(resp *DomainIntelResponse) string {
	if resp == nil {
		return ""
	}
	if resp.Headers != nil && resp.Headers.Server != "" {
		if provider := detectProviderFromText(resp.Headers.Server, map[string]string{
			"cloudflare": "Cloudflare",
			"cloudfront": "Amazon CloudFront",
			"fastly":     "Fastly",
			"akamai":     "Akamai",
		}); provider != "" {
			return provider
		}
	}
	orgs := make([]string, 0, len(resp.ResolvedIPs))
	for _, ip := range resp.ResolvedIPs {
		if value := strings.TrimSpace(ip.Org); value != "" {
			orgs = append(orgs, value)
		}
	}
	if provider := detectProviderFromText(strings.Join(orgs, " "), map[string]string{
		"cloudflare": "Cloudflare",
		"fastly":     "Fastly",
		"akamai":     "Akamai",
		"cloudfront": "Amazon CloudFront",
	}); provider != "" {
		return provider
	}
	if resp.Registration != nil {
		if provider := detectProviderFromText(strings.Join(resp.Registration.NameServers, " "), map[string]string{
			"cloudflare": "Cloudflare",
		}); provider != "" {
			return provider
		}
	}
	return ""
}

func detectHostingProvider(resolvedIPs []IPResponse, cdnProvider string) string {
	if cdnProvider != "" {
		return cdnProvider
	}
	if len(resolvedIPs) == 0 {
		return ""
	}
	orgCount := map[string]int{}
	for _, ip := range resolvedIPs {
		if org := strings.TrimSpace(ip.Org); org != "" {
			orgCount[org]++
		}
	}
	best := ""
	bestCount := 0
	for org, count := range orgCount {
		if count > bestCount {
			best = org
			bestCount = count
		}
	}
	return best
}

func detectProviderFromHosts(hosts []string, patterns map[string]string) string {
	return detectProviderFromText(strings.Join(hosts, " "), patterns)
}

func detectProviderFromText(value string, patterns map[string]string) string {
	lower := strings.ToLower(value)
	for pattern, provider := range patterns {
		if strings.Contains(lower, strings.ToLower(pattern)) {
			return provider
		}
	}
	return ""
}

func recordValues(records []DNSRecord) []string {
	values := make([]string, 0, len(records))
	for _, record := range records {
		if value := strings.TrimSpace(record.Value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func sortedKeys(set map[string]struct{}) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

var securityTXTEmailRegex = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)

func fetchSecurityTXT(domain string) (*SecurityTXTResponse, error) {
	candidates := []string{
		"https://" + domain + "/.well-known/security.txt",
		"https://" + domain + "/security.txt",
	}
	for _, candidate := range candidates {
		resp, err := fetchSecurityTXTURL(candidate)
		if err == nil && resp != nil && resp.Present {
			return resp, nil
		}
	}
	return nil, nil
}

func fetchSecurityTXTURL(rawURL string) (*SecurityTXTResponse, error) {
	currentURL := rawURL
	for range 4 {
		pinnedIP, err := validateSafeURL(currentURL)
		if err != nil {
			return nil, err
		}

		transport := &http.Transport{DialContext: pinnedDialer(pinnedIP)}
		client := &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}

		req, err := http.NewRequest(http.MethodGet, currentURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "ArkAPI/1.0 (+https://arkapi.dev)")
		req.Header.Set("Accept", "text/plain, text/*;q=0.9, */*;q=0.1")

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
			resp.Body.Close()
			if location == "" {
				return nil, fmt.Errorf("redirect without location")
			}
			nextURL, err := resolveRedirectURL(currentURL, location)
			if err != nil {
				return nil, err
			}
			currentURL = nextURL
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("returned status %d", resp.StatusCode)
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read failed: %w", err)
		}
		parsed := parseSecurityTXT(currentURL, string(body))
		if parsed == nil || !parsed.Present {
			return nil, fmt.Errorf("no security.txt fields found")
		}
		return parsed, nil
	}
	return nil, fmt.Errorf("too many redirects")
}

func resolveRedirectURL(baseURL, location string) (string, error) {
	base, err := neturl.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid redirect base")
	}
	next, err := base.Parse(location)
	if err != nil {
		return "", fmt.Errorf("invalid redirect target")
	}
	if next.Scheme != "http" && next.Scheme != "https" {
		return "", fmt.Errorf("redirect target uses unsupported scheme")
	}
	return next.String(), nil
}

func parseSecurityTXT(sourceURL, raw string) *SecurityTXTResponse {
	result := &SecurityTXTResponse{
		Present:   false,
		SourceURL: sourceURL,
	}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		if value == "" {
			continue
		}
		result.Present = true
		switch key {
		case "contact":
			result.Contacts = appendUniqueString(result.Contacts, value)
			result.Emails = appendUniqueEmails(result.Emails, extractSecurityTXTEmails(value)...)
		case "canonical":
			result.Canonical = appendUniqueString(result.Canonical, value)
		case "policy":
			result.Policy = appendUniqueString(result.Policy, value)
		case "hiring":
			result.Hiring = appendUniqueString(result.Hiring, value)
		case "encryption":
			result.Encryption = appendUniqueString(result.Encryption, value)
		case "acknowledgments":
			result.Acknowledgments = appendUniqueString(result.Acknowledgments, value)
		case "preferred-languages":
			for _, language := range strings.Split(value, ",") {
				language = strings.TrimSpace(language)
				if language != "" {
					result.PreferredLanguages = appendUniqueString(result.PreferredLanguages, language)
				}
			}
		case "expires":
			if result.Expires == "" {
				result.Expires = value
			}
		case "csaf":
			result.CSAF = appendUniqueString(result.CSAF, value)
		}
	}

	if !result.Present {
		return nil
	}
	return result
}

func extractSecurityTXTEmails(value string) []string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(trimmed), "mailto:") {
		trimmed = strings.TrimSpace(trimmed[len("mailto:"):])
		if parsed, err := mail.ParseAddress(trimmed); err == nil && parsed.Address != "" {
			return []string{strings.ToLower(parsed.Address)}
		}
	}
	matches := securityTXTEmailRegex.FindAllString(trimmed, -1)
	if len(matches) == 0 {
		return nil
	}
	result := make([]string, 0, len(matches))
	for _, match := range matches {
		result = appendUniqueString(result, strings.ToLower(strings.TrimSpace(match)))
	}
	return result
}

func appendUniqueString(values []string, additions ...string) []string {
	for _, addition := range additions {
		addition = strings.TrimSpace(addition)
		if addition == "" || containsString(values, addition) {
			continue
		}
		values = append(values, addition)
	}
	return values
}

func appendUniqueEmails(values []string, additions ...string) []string {
	for _, addition := range additions {
		addition = strings.ToLower(strings.TrimSpace(addition))
		if addition == "" || containsString(values, addition) {
			continue
		}
		values = append(values, addition)
	}
	return values
}

func fetchRobotsTXT(domain string) (*RobotsTXTResponse, error) {
	resp, err := fetchTextResource("https://" + domain + "/robots.txt", "text/plain, text/*;q=0.9, */*;q=0.1", 64*1024)
	if err != nil {
		return nil, nil
	}
	return parseRobotsTXT(resp.FinalURL, resp.Body), nil
}

func parseRobotsTXT(sourceURL, raw string) *RobotsTXTResponse {
	result := &RobotsTXTResponse{
		Present:   false,
		SourceURL: sourceURL,
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
			if line == "" {
				continue
			}
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		if value == "" {
			continue
		}
		result.Present = true
		switch key {
		case "user-agent":
			result.UserAgents = appendUniqueString(result.UserAgents, value)
		case "allow":
			result.Allow = appendUniqueString(result.Allow, value)
		case "disallow":
			result.Disallow = appendUniqueString(result.Disallow, value)
		case "sitemap":
			result.Sitemaps = appendUniqueString(result.Sitemaps, value)
		case "crawl-delay":
			if result.CrawlDelay == "" {
				result.CrawlDelay = value
			}
		case "host":
			if result.Host == "" {
				result.Host = value
			}
		}
	}
	if !result.Present {
		return nil
	}
	return result
}

func fetchTechFingerprint(rawURL string) (*TechFingerprintResponse, error) {
	resource, err := fetchTextResource(rawURL, "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1", 256*1024)
	if err != nil {
		return nil, nil
	}

	result := &TechFingerprintResponse{
		Server:   resource.Headers.Get("Server"),
		FinalURL: resource.FinalURL,
	}

	bodyLower := strings.ToLower(resource.Body)
	linkHeader := strings.ToLower(resource.Headers.Get("Link"))
	xPoweredBy := strings.TrimSpace(resource.Headers.Get("X-Powered-By"))
	generatorHeader := strings.TrimSpace(resource.Headers.Get("X-Generator"))
	generatorMeta := extractMetaGenerator(resource.Body)

	if generatorHeader != "" {
		result.Generator = generatorHeader
	} else if generatorMeta != "" {
		result.Generator = generatorMeta
	}

	if strings.Contains(linkHeader, "wp-json") || strings.Contains(bodyLower, "/wp-content/") || strings.Contains(bodyLower, "/wp-includes/") {
		result.CMS = "WordPress"
		result.Detected = appendUniqueString(result.Detected, "WordPress")
	}
	if strings.Contains(bodyLower, "elementor") {
		result.Detected = appendUniqueString(result.Detected, "Elementor")
		if result.CMS == "" {
			result.CMS = "WordPress"
		}
	}
	if strings.Contains(bodyLower, "googlesitekit") || strings.Contains(strings.ToLower(result.Generator), "site kit by google") {
		result.Detected = appendUniqueString(result.Detected, "Site Kit by Google")
		if result.CMS == "" {
			result.CMS = "WordPress"
		}
	}
	if strings.Contains(bodyLower, "woocommerce") {
		result.Ecommerce = "WooCommerce"
		result.Detected = appendUniqueString(result.Detected, "WooCommerce")
		if result.CMS == "" {
			result.CMS = "WordPress"
		}
	}
	if strings.Contains(bodyLower, "cdn.shopify.com") || strings.Contains(bodyLower, "shopify.theme") || strings.Contains(bodyLower, "shopify-section") {
		result.CMS = firstNonEmptyDomainIntel(result.CMS, "Shopify")
		result.Ecommerce = firstNonEmptyDomainIntel(result.Ecommerce, "Shopify")
		result.Detected = appendUniqueString(result.Detected, "Shopify")
	}
	if strings.Contains(bodyLower, "__next_data__") || strings.Contains(bodyLower, "/_next/") {
		result.Frontend = "Next.js"
		result.Detected = appendUniqueString(result.Detected, "Next.js")
	}
	if strings.Contains(bodyLower, "data-reactroot") || strings.Contains(bodyLower, "reactdom") {
		result.Frontend = firstNonEmptyDomainIntel(result.Frontend, "React")
		result.Detected = appendUniqueString(result.Detected, "React")
	}
	if strings.Contains(bodyLower, "/_nuxt/") {
		result.Frontend = firstNonEmptyDomainIntel(result.Frontend, "Nuxt")
		result.Detected = appendUniqueString(result.Detected, "Nuxt")
	}
	if strings.Contains(bodyLower, "/_astro/") {
		result.Frontend = firstNonEmptyDomainIntel(result.Frontend, "Astro")
		result.Detected = appendUniqueString(result.Detected, "Astro")
	}
	if strings.Contains(bodyLower, "gatsby-script-loader") {
		result.Frontend = firstNonEmptyDomainIntel(result.Frontend, "Gatsby")
		result.Detected = appendUniqueString(result.Detected, "Gatsby")
	}
	if strings.Contains(bodyLower, "data-server-rendered=\"true\"") || strings.Contains(bodyLower, "__vue__") {
		result.Frontend = firstNonEmptyDomainIntel(result.Frontend, "Vue")
		result.Detected = appendUniqueString(result.Detected, "Vue")
	}
	if strings.Contains(bodyLower, "drupal-settings-json") || strings.Contains(strings.ToLower(result.Generator), "drupal") {
		result.CMS = firstNonEmptyDomainIntel(result.CMS, "Drupal")
		result.Detected = appendUniqueString(result.Detected, "Drupal")
	}
	if strings.Contains(strings.ToLower(result.Generator), "joomla") {
		result.CMS = firstNonEmptyDomainIntel(result.CMS, "Joomla")
		result.Detected = appendUniqueString(result.Detected, "Joomla")
	}
	if strings.Contains(bodyLower, "mage/cookies.js") || strings.Contains(strings.ToLower(result.Generator), "magento") {
		result.CMS = firstNonEmptyDomainIntel(result.CMS, "Magento")
		result.Ecommerce = firstNonEmptyDomainIntel(result.Ecommerce, "Magento")
		result.Detected = appendUniqueString(result.Detected, "Magento")
	}
	if strings.Contains(bodyLower, "static.wixstatic.com") || strings.Contains(strings.ToLower(result.Generator), "wix") {
		result.CMS = firstNonEmptyDomainIntel(result.CMS, "Wix")
		result.Detected = appendUniqueString(result.Detected, "Wix")
	}
	if strings.Contains(bodyLower, "cdn.webflow.com") || strings.Contains(strings.ToLower(result.Generator), "webflow") {
		result.CMS = firstNonEmptyDomainIntel(result.CMS, "Webflow")
		result.Detected = appendUniqueString(result.Detected, "Webflow")
	}
	if strings.Contains(bodyLower, "static.squarespace.com") || strings.Contains(strings.ToLower(result.Generator), "squarespace") {
		result.CMS = firstNonEmptyDomainIntel(result.CMS, "Squarespace")
		result.Detected = appendUniqueString(result.Detected, "Squarespace")
	}
	if strings.Contains(strings.ToLower(result.Generator), "ghost") {
		result.CMS = firstNonEmptyDomainIntel(result.CMS, "Ghost")
		result.Detected = appendUniqueString(result.Detected, "Ghost")
	}
	if strings.Contains(strings.ToLower(result.Generator), "hugo") {
		result.CMS = firstNonEmptyDomainIntel(result.CMS, "Hugo")
		result.Detected = appendUniqueString(result.Detected, "Hugo")
	}
	if strings.Contains(strings.ToLower(result.Server), "cloudflare") && !strings.EqualFold(result.Server, "cloudflare") {
		result.Detected = appendUniqueString(result.Detected, "Cloudflare")
	}
	if xPoweredBy != "" {
		result.Detected = appendUniqueString(result.Detected, xPoweredBy)
	}
	if result.Server != "" {
		result.Detected = appendUniqueString(result.Detected, result.Server)
	}

	if result.CMS == "" && result.Frontend == "" && result.Ecommerce == "" && result.Generator == "" && len(result.Detected) == 0 && result.Server == "" {
		return nil, nil
	}
	return result, nil
}

func fetchHTTPBehavior(domain string) (*HTTPBehaviorResponse, error) {
	resource, err := fetchTextResource("http://"+domain, "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1", 64*1024)
	if err != nil {
		return nil, nil
	}

	result := &HTTPBehaviorResponse{
		InitialURL:    resource.InitialURL,
		FinalURL:      resource.FinalURL,
		CanonicalHost: extractHost(resource.FinalURL),
		RedirectCount: len(resource.RedirectChain),
		StatusChain:   append([]int(nil), resource.StatusChain...),
	}
	if len(resource.RedirectChain) > 0 {
		result.RedirectChain = append([]string(nil), resource.RedirectChain...)
	}

	initialHost := extractHost(resource.InitialURL)
	finalHost := extractHost(resource.FinalURL)
	initialScheme := extractScheme(resource.InitialURL)
	finalScheme := extractScheme(resource.FinalURL)

	result.HTTPSRedirect = initialScheme == "http" && finalScheme == "https"
	if initialHost != "" && finalHost != "" && !strings.HasPrefix(initialHost, "www.") && finalHost == "www."+initialHost {
		result.WWWRedirect = true
	}
	if result.FinalURL == "" {
		return nil, nil
	}
	return result, nil
}

func extractHost(rawURL string) string {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func extractScheme(rawURL string) string {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Scheme)
}

type fetchedTextResource struct {
	InitialURL    string
	FinalURL string
	RedirectChain []string
	StatusChain   []int
	Body          string
	Headers       http.Header
}

func fetchTextResource(rawURL, acceptHeader string, maxBytes int64) (*fetchedTextResource, error) {
	currentURL := rawURL
	redirectChain := []string{}
	statusChain := []int{}
	for range 4 {
		pinnedIP, err := validateSafeURL(currentURL)
		if err != nil {
			return nil, err
		}
		transport := &http.Transport{DialContext: pinnedDialer(pinnedIP)}
		client := &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		req, err := http.NewRequest(http.MethodGet, currentURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "ArkAPI/1.0 (+https://arkapi.dev)")
		req.Header.Set("Accept", acceptHeader)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			statusChain = append(statusChain, resp.StatusCode)
			location := resp.Header.Get("Location")
			resp.Body.Close()
			if location == "" {
				return nil, fmt.Errorf("redirect without location")
			}
			nextURL, err := resolveRedirectURL(currentURL, location)
			if err != nil {
				return nil, err
			}
			redirectChain = append(redirectChain, nextURL)
			currentURL = nextURL
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("returned status %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
		headers := resp.Header.Clone()
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read failed: %w", err)
		}
		statusChain = append(statusChain, resp.StatusCode)
		return &fetchedTextResource{
			InitialURL:    rawURL,
			FinalURL:      currentURL,
			RedirectChain: redirectChain,
			StatusChain:   statusChain,
			Body:          string(body),
			Headers:       headers,
		}, nil
	}
	return nil, fmt.Errorf("too many redirects")
}

func extractMetaGenerator(raw string) string {
	re := regexp.MustCompile(`(?is)<meta[^>]+name=["']generator["'][^>]+content=["']([^"']+)["']`)
	matches := re.FindStringSubmatch(raw)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func firstNonEmptyDomainIntel(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type crtSHEntry struct {
	NameValue string `json:"name_value"`
}

func fetchCTLogSubdomains(domain string) ([]string, error) {
	queryURL := fmt.Sprintf("https://crt.sh/?q=%s&output=json", neturl.QueryEscape("%."+domain))
	client := &http.Client{Timeout: 12 * time.Second}
	req, err := http.NewRequest(http.MethodGet, queryURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ArkAPI/1.0 (+https://arkapi.dev)")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, nil
	}
	var entries []crtSHEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, nil
	}

	discovered := map[string]struct{}{}
	for _, entry := range entries {
		for _, rawName := range strings.Split(entry.NameValue, "\n") {
			name := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(rawName, ".")))
			name = strings.TrimPrefix(name, "*.")
			if name == "" || name == domain || !strings.HasSuffix(name, "."+domain) {
				continue
			}
			if !isValidDomain(name) {
				continue
			}
			discovered[name] = struct{}{}
			if len(discovered) >= 25 {
				break
			}
		}
		if len(discovered) >= 25 {
			break
		}
	}
	if len(discovered) == 0 {
		return nil, nil
	}
	return sortedKeys(discovered), nil
}
