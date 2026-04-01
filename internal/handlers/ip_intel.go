package handlers

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

type IPIntelRequest struct {
	IP         string `json:"ip"`
	MaxAgeDays int    `json:"max_age_days,omitempty"`
}

type IPIntelResponse struct {
	IP               string                `json:"ip"`
	ReverseDNS       []string              `json:"reverse_dns,omitempty"`
	ASNNumber        int                   `json:"asn_number,omitempty"`
	NetworkType      string                `json:"network_type,omitempty"`
	ReportedRecently bool                  `json:"reported_recently"`
	RiskLabel        string                `json:"risk_label"`
	RiskReason       string                `json:"risk_reason"`
	AbuseContact     *IPAbuseContact       `json:"abuse_contact,omitempty"`
	AbuseReportNote  string                `json:"abuse_reporting_note,omitempty"`
	URLhausHost      *IPURLhausHostSummary `json:"urlhaus_host,omitempty"`
	Lookup           *IPResponse           `json:"lookup"`
	Abuse            *IPAbuseCheckResponse `json:"abuse"`
}

func (h *Handler) IPIntel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req IPIntelRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"ip\": \"8.8.8.8\", \"max_age_days\": 30}"})
		return
	}

	req.IP = strings.TrimSpace(req.IP)
	if req.IP == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "ip is required"})
		return
	}

	parsed := net.ParseIP(req.IP)
	if parsed == nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid IP address"})
		return
	}
	if !isPublicRoutableIP(parsed) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "use a public IPv4 or IPv6 address"})
		return
	}

	if req.MaxAgeDays == 0 {
		req.MaxAgeDays = 30
	}
	if req.MaxAgeDays < 1 || req.MaxAgeDays > 365 {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "max_age_days must be between 1 and 365"})
		return
	}

	h.executeHandler(w, r, "/api/ip-intel", h.Cfg.IPIntelCostSats, func() (interface{}, error) {
		lookup, err := doIPLookup(h.Geo, req.IP)
		if err != nil {
			return nil, err
		}

		abuse, err := h.doIPAbuseCheck(req.IP, req.MaxAgeDays)
		if err != nil {
			return nil, err
		}

		reverseDNS := lookupReverseDNS(req.IP)
		asnNumber := parseASNNumber(lookup.AS)
		networkType := normalizeNetworkType(abuse.UsageType)
		reportedRecently := wasReportedRecently(abuse.LastReportedAt)
		urlhausHost, err := h.lookupIPURLhausHost(req.IP)
		if err != nil {
			return nil, err
		}
		riskLabel, riskReason := deriveIPRisk(abuse, urlhausHost, networkType, reportedRecently)
		var abuseContact *IPAbuseContact
		var abuseReportNote string
		if rdap, err := lookupIPAbuseContact(req.IP); err == nil && rdap != nil {
			abuseContact = rdap.Contact
			abuseReportNote = rdap.ReportingNote
		}

		return &IPIntelResponse{
			IP:               req.IP,
			ReverseDNS:       reverseDNS,
			ASNNumber:        asnNumber,
			NetworkType:      networkType,
			ReportedRecently: reportedRecently,
			RiskLabel:        riskLabel,
			RiskReason:       riskReason,
			AbuseContact:     abuseContact,
			AbuseReportNote:  abuseReportNote,
			URLhausHost:      urlhausHost,
			Lookup:           lookup,
			Abuse:            abuse,
		}, nil
	})
}

func lookupReverseDNS(ip string) []string {
	names, err := net.LookupAddr(ip)
	if err != nil || len(names) == 0 {
		return nil
	}

	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		trimmed := strings.TrimSuffix(strings.TrimSpace(name), ".")
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func parseASNNumber(asValue string) int {
	asValue = strings.TrimSpace(asValue)
	if !strings.HasPrefix(asValue, "AS") {
		return 0
	}
	var number int
	if _, err := fmt.Sscanf(asValue, "AS%d", &number); err != nil {
		return 0
	}
	return number
}

func normalizeNetworkType(usage string) string {
	usage = strings.ToLower(strings.TrimSpace(usage))
	switch {
	case strings.Contains(usage, "data center"), strings.Contains(usage, "web hosting"), strings.Contains(usage, "transit"):
		return "hosting"
	case strings.Contains(usage, "content delivery"):
		return "cdn"
	case strings.Contains(usage, "fixed line"), strings.Contains(usage, "consumer"), strings.Contains(usage, "residential"):
		return "residential"
	case strings.Contains(usage, "mobile"):
		return "mobile"
	case strings.Contains(usage, "education"):
		return "education"
	case strings.Contains(usage, "government"):
		return "government"
	case strings.Contains(usage, "enterprise"), strings.Contains(usage, "business"):
		return "enterprise"
	default:
		return "unknown"
	}
}

func wasReportedRecently(lastReportedAt *time.Time) bool {
	if lastReportedAt == nil {
		return false
	}
	return time.Since(lastReportedAt.UTC()) <= 7*24*time.Hour
}

func deriveIPRisk(abuse *IPAbuseCheckResponse, urlhausHost *IPURLhausHostSummary, networkType string, reportedRecently bool) (string, string) {
	if urlhausHost != nil && urlhausHost.Listed {
		if len(urlhausHost.SampleThreats) > 0 {
			return "high", fmt.Sprintf("Listed in URLhaus for %s activity", strings.Join(urlhausHost.SampleThreats, ", "))
		}
		return "high", "Listed in URLhaus as malware-linked host infrastructure"
	}
	if abuse == nil {
		return "unknown", "No abuse data available"
	}

	switch {
	case abuse.AbuseConfidenceScore >= 75:
		if networkType == "residential" {
			return "high", "High abuse score on a residential connection"
		}
		if reportedRecently {
			return "high", "High abuse score with recent reports"
		}
		return "high", "High abuse score"
	case abuse.AbuseConfidenceScore >= 25 || abuse.TotalReports >= 5:
		if abuse.AbuseConfidenceScore == 0 && abuse.IsWhitelisted {
			return "low", "Provider is whitelisted and has no abuse score despite historical reports"
		}
		if networkType == "hosting" || networkType == "cdn" {
			return "medium", "Multiple abuse reports on hosting infrastructure"
		}
		if reportedRecently {
			return "medium", "Recent abuse reports in the selected window"
		}
		return "medium", "Moderate abuse history"
	default:
		if networkType == "hosting" && abuse.TotalReports > 0 {
			return "medium", "Low score but some reports on hosting infrastructure"
		}
		return "low", "No meaningful abuse history in the selected window"
	}
}
