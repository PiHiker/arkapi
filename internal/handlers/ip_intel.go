package handlers

import (
	"net"
	"net/http"
	"strings"
)

type IPIntelRequest struct {
	IP         string `json:"ip"`
	MaxAgeDays int    `json:"max_age_days,omitempty"`
}

type IPIntelResponse struct {
	IP     string                `json:"ip"`
	Lookup *IPResponse           `json:"lookup"`
	Abuse  *IPAbuseCheckResponse `json:"abuse"`
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

		return &IPIntelResponse{
			IP:     req.IP,
			Lookup: lookup,
			Abuse:  abuse,
		}, nil
	})
}
