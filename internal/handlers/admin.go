package handlers

import (
	"bufio"
	"bytes"
	"net/url"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	adminTrafficCacheTTL  = time.Minute
	adminTrafficTailLines = 12000
	adminTrafficWindow    = 24 * time.Hour
)

var (
	adminTrafficCacheMu sync.Mutex
	adminTrafficCacheAt time.Time
	adminTrafficCache   *AdminTrafficReport

	adminAccessLogPattern = regexp.MustCompile(`^(\S+) \S+ \S+ \[([^\]]+)\] "([A-Z]+) ([^ ]+) [^"]*" (\d{3}) \S+ "([^"]*)" "([^"]*)"$`)
)

func isAdminManagementIP(ip string) bool {
	raw := strings.TrimSpace(os.Getenv("ARKAPI_ADMIN_MANAGEMENT_IPS"))
	if raw == "" {
		return false
	}
	for _, candidate := range strings.Split(raw, ",") {
		if strings.TrimSpace(candidate) == ip {
			return true
		}
	}
	return false
}

func adminTrafficLogPath() string {
	return strings.TrimSpace(os.Getenv("ARKAPI_ADMIN_TRAFFIC_LOG_PATH"))
}

type AdminOverviewResponse struct {
	GeneratedAt  string      `json:"generated_at"`
	PaymentMode  string      `json:"payment_mode"`
	Usage        interface{} `json:"usage"`
	Traffic      interface{} `json:"traffic,omitempty"`
	MasterWallet interface{} `json:"master_wallet,omitempty"`
}

type AdminTrafficReport struct {
	WindowHours      int                  `json:"window_hours"`
	TotalRequests    int64                `json:"total_requests"`
	ExternalRequests int64                `json:"external_requests"`
	UniqueIPs        int                  `json:"unique_ips"`
	ExternalIPs      int                  `json:"external_ips"`
	PageHits         int64                `json:"page_hits"`
	CrawlerRequests  int64                `json:"crawler_requests"`
	TopPaths         []AdminTrafficPath   `json:"top_paths"`
	TopSources       []AdminTrafficSource `json:"top_sources"`
}

type AdminTrafficPath struct {
	Path     string `json:"path"`
	Requests int64  `json:"requests"`
}

type AdminTrafficSource struct {
	IP       string `json:"ip"`
	Kind     string `json:"kind"`
	Requests int64  `json:"requests"`
	LastPath string `json:"last_path"`
	LastSeen string `json:"last_seen"`
}

type adminTrafficAgg struct {
	Kind     string
	Requests int64
	LastPath string
	LastSeen time.Time
}

func maskToken(token string) string {
	if len(token) <= 10 {
		return token
	}
	return token[:8] + "..." + token[len(token)-4:]
}

func (h *Handler) AdminOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use GET"})
		return
	}

	stats, err := h.DB.GetAdminStats()
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load admin stats"})
		return
	}

	for i := range stats.RecentCalls {
		stats.RecentCalls[i].SessionToken = maskToken(stats.RecentCalls[i].SessionToken)
		stats.RecentCalls[i].Endpoint = strings.TrimPrefix(stats.RecentCalls[i].Endpoint, "/api/")
	}

	resp := AdminOverviewResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		PaymentMode: h.Cfg.PaymentMode,
		Usage:       stats,
	}

	if traffic, err := getAdminTrafficReport(); err == nil {
		resp.Traffic = traffic
	}

	if h.Bark != nil {
		if balance, err := h.Bark.GetBalance(); err == nil {
			resp.MasterWallet = balance
		}
	}

	sendJSON(w, http.StatusOK, resp)
}

func getAdminTrafficReport() (*AdminTrafficReport, error) {
	adminTrafficCacheMu.Lock()
	defer adminTrafficCacheMu.Unlock()

	if adminTrafficCache != nil && time.Since(adminTrafficCacheAt) < adminTrafficCacheTTL {
		return adminTrafficCache, nil
	}

	report, err := loadAdminTrafficReport()
	if err != nil {
		return nil, err
	}

	adminTrafficCache = report
	adminTrafficCacheAt = time.Now()
	return report, nil
}

func loadAdminTrafficReport() (*AdminTrafficReport, error) {
	logPath := adminTrafficLogPath()
	if logPath == "" {
		return nil, http.ErrMissingFile
	}

	cmd := exec.Command("tail", "-n", strconv.Itoa(adminTrafficTailLines), logPath)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	report := &AdminTrafficReport{
		WindowHours: int(adminTrafficWindow / time.Hour),
		TopPaths:    make([]AdminTrafficPath, 0, 6),
		TopSources:  make([]AdminTrafficSource, 0, 5),
	}

	cutoff := time.Now().UTC().Add(-adminTrafficWindow)
	seenIPs := make(map[string]struct{})
	externalIPs := make(map[string]struct{})
	pathCounts := make(map[string]int64)
	sourceAggs := make(map[string]*adminTrafficAgg)

	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		match := adminAccessLogPattern.FindStringSubmatch(line)
		if len(match) != 8 {
			continue
		}

		ip := match[1]
		when, err := time.Parse("02/Jan/2006:15:04:05 -0700", match[2])
		if err != nil || when.UTC().Before(cutoff) {
			continue
		}

		path := normalizeTrafficPath(match[4])
		userAgent := match[7]
		kind := classifyTraffic(ip, path, userAgent)

		report.TotalRequests++
		seenIPs[ip] = struct{}{}

		if kind == "crawler" {
			report.CrawlerRequests++
		}

		if isAdminManagementIP(ip) {
			continue
		}

		report.ExternalRequests++
		externalIPs[ip] = struct{}{}

		if isTrackableTrafficPath(path) {
			report.PageHits++
			pathCounts[path]++
		}

		agg := sourceAggs[ip]
		if agg == nil {
			agg = &adminTrafficAgg{Kind: kind}
			sourceAggs[ip] = agg
		}
		agg.Kind = mergeTrafficKind(agg.Kind, kind)
		agg.Requests++
		agg.LastSeen = when.UTC()
		agg.LastPath = path
	}

	report.UniqueIPs = len(seenIPs)
	report.ExternalIPs = len(externalIPs)
	report.TopPaths = topTrafficPaths(pathCounts, 6)
	report.TopSources = topTrafficSources(sourceAggs, 5)

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return report, nil
}

func normalizeTrafficPath(path string) string {
	if path == "" {
		return "/"
	}
	if idx := strings.Index(path, "?"); idx >= 0 {
		path = path[:idx]
	}
	if decoded, err := url.PathUnescape(path); err == nil && decoded != "" {
		path = decoded
	}
	if path == "" {
		return "/"
	}
	return path
}

func classifyTraffic(ip, path, userAgent string) string {
	if isAdminManagementIP(ip) {
		return "management"
	}
	ua := strings.ToLower(userAgent)
	botTerms := []string{
		"bot",
		"crawler",
		"spider",
		"crawl",
		"semrush",
		"ahrefs",
		"dotbot",
		"claudebot",
		"oai-searchbot",
		"bingbot",
		"googlebot",
		"applebot",
	}
	for _, term := range botTerms {
		if strings.Contains(ua, term) {
			return "crawler"
		}
	}
	suspiciousPathTerms := []string{
		".env",
		".git/",
		"wp-login.php",
		"xmlrpc.php",
		"package.json",
		"phpinfo.php",
		"config.php",
		".aws/",
		".s3cfg",
		"vendor/phpunit",
	}
	lowerPath := strings.ToLower(path)
	for _, term := range suspiciousPathTerms {
		if strings.Contains(lowerPath, term) {
			return "scanner"
		}
	}
	return "visitor"
}

func isTrackableTrafficPath(path string) bool {
	switch path {
	case "/health", "/v1/stats", "/v1/admin/overview", "/v1/balance":
		return false
	}
	if strings.HasPrefix(path, "/api/") {
		return false
	}
	if strings.HasPrefix(path, "/v1/downloads/") {
		return false
	}
	return true
}

func topTrafficPaths(counts map[string]int64, limit int) []AdminTrafficPath {
	paths := make([]AdminTrafficPath, 0, len(counts))
	for path, count := range counts {
		paths = append(paths, AdminTrafficPath{Path: path, Requests: count})
	}
	sort.Slice(paths, func(i, j int) bool {
		if paths[i].Requests == paths[j].Requests {
			return paths[i].Path < paths[j].Path
		}
		return paths[i].Requests > paths[j].Requests
	})
	if len(paths) > limit {
		paths = paths[:limit]
	}
	return paths
}

func topTrafficSources(aggs map[string]*adminTrafficAgg, limit int) []AdminTrafficSource {
	sources := make([]AdminTrafficSource, 0, len(aggs))
	for ip, agg := range aggs {
		sources = append(sources, AdminTrafficSource{
			IP:       ip,
			Kind:     agg.Kind,
			Requests: agg.Requests,
			LastPath: agg.LastPath,
			LastSeen: agg.LastSeen.Format(time.RFC3339),
		})
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Requests == sources[j].Requests {
			return sources[i].LastSeen > sources[j].LastSeen
		}
		return sources[i].Requests > sources[j].Requests
	})
	if len(sources) > limit {
		sources = sources[:limit]
	}
	return sources
}

func mergeTrafficKind(existing, next string) string {
	rank := map[string]int{
		"visitor":    1,
		"crawler":    2,
		"scanner":    3,
		"management": 4,
	}
	if rank[next] > rank[existing] {
		return next
	}
	return existing
}
