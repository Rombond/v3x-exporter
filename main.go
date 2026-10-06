package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// -----------------------------------------------------------------------------
// Configuration
// -----------------------------------------------------------------------------

type config struct {
	Port           string
	MetricsPath    string
	ScrapeInterval time.Duration
	Login          string // username or email (the site accepts either)
	Password       string
	BaseURL        string
}

func loadConfig() config {
	baseURL := getEnvOrDefault("V3X_BASE_URL", "https://api.v3x.club")
	login := os.Getenv("V3X_USERNAME")
	if login == "" {
		login = os.Getenv("V3X_EMAIL")
	}
	return config{
		Port:           getEnvOrDefault("PORT", "9090"),
		MetricsPath:    getEnvOrDefault("METRICS_PATH", "/metrics"),
		Login:          login,
		Password:       os.Getenv("V3X_PASSWORD"),
		ScrapeInterval: parseDuration(os.Getenv("SCRAPE_INTERVAL"), 5*time.Minute),
		BaseURL:        strings.TrimSuffix(baseURL, "/"),
	}
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// -----------------------------------------------------------------------------
// API client
// -----------------------------------------------------------------------------

var errUnauthorized = errors.New("authentication required")

// V3XClient manages authentication and communication with the v3x.club API.
type V3XClient struct {
	mu            sync.RWMutex
	authenticated bool
	baseURL       string
	loginURL      string
	meURL         string
	httpClient    *http.Client
}

func newV3XClient(baseURL string) *V3XClient {
	jar, _ := cookiejar.New(nil)
	return &V3XClient{
		baseURL:    baseURL,
		loginURL:   baseURL + "/auth/login",
		meURL:      baseURL + "/auth/me",
		httpClient: &http.Client{Timeout: 15 * time.Second, Jar: jar},
	}
}

func (c *V3XClient) IsAuthenticated() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.authenticated
}

func (c *V3XClient) setAuthenticated(v bool) {
	c.mu.Lock()
	c.authenticated = v
	c.mu.Unlock()
}

func (c *V3XClient) newRequest(method, u string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
	req.Header.Set("Origin", "https://v3x.club")
	req.Header.Set("Referer", "https://v3x.club/")
	return req, nil
}

// Login authenticates against the API; the session cookie is kept in the cookie jar.
func (c *V3XClient) Login(login, password string) error {
	payload, err := json.Marshal(map[string]interface{}{
		"login":    login,
		"password": password,
		"remember": true,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal login payload: %w", err)
	}

	req, err := c.newRequest(http.MethodPost, c.loginURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("login refused (%s): %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected login response: %s", resp.Status)
	}

	var r struct {
		TwoFactorRequired bool `json:"twoFactorRequired"`
	}
	_ = json.Unmarshal(body, &r)
	if r.TwoFactorRequired {
		return fmt.Errorf("two-factor authentication is enabled on this account, not supported")
	}

	u, _ := url.Parse(c.baseURL)
	if len(c.httpClient.Jar.Cookies(u)) == 0 {
		return fmt.Errorf("login response contained no session cookie")
	}

	c.setAuthenticated(true)
	fmt.Printf("[auth] Successfully authenticated as %s\n", login)
	return nil
}

// FetchMetrics retrieves the current user's profile (including traffic) from the API.
func (c *V3XClient) FetchMetrics() (*UserMetrics, error) {
	req, err := c.newRequest(http.MethodGet, c.meURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build metrics request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metrics request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		c.setAuthenticated(false)
		return nil, errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected metrics response: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read metrics response: %w", err)
	}

	var metrics UserMetrics
	if err := json.Unmarshal(body, &metrics); err != nil {
		return nil, fmt.Errorf("failed to parse metrics response: %w", err)
	}
	return &metrics, nil
}

// -----------------------------------------------------------------------------
// Domain types
// -----------------------------------------------------------------------------

// UserMetrics maps the subset of /auth/me we care about (values are in bytes).
type UserMetrics struct {
	Uploaded   int64  `json:"uploaded"`
	Downloaded int64  `json:"downloaded"`
	Username   string `json:"username"`
}

// -----------------------------------------------------------------------------
// Prometheus metrics
// -----------------------------------------------------------------------------

type exporterMetrics struct {
	totalUploaded   prometheus.Gauge
	totalDownloaded prometheus.Gauge
}

func newExporterMetrics(reg prometheus.Registerer) *exporterMetrics {
	m := &exporterMetrics{
		totalUploaded: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "v3x",
			Name:      "total_uploaded_bytes",
			Help:      "Total uploaded bytes from v3x.club API",
		}),
		totalDownloaded: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "v3x",
			Name:      "total_downloaded_bytes",
			Help:      "Total downloaded bytes from v3x.club API",
		}),
	}
	reg.MustRegister(m.totalUploaded, m.totalDownloaded)
	return m
}

func (m *exporterMetrics) update(u *UserMetrics) {
	m.totalUploaded.Set(float64(u.Uploaded))
	m.totalDownloaded.Set(float64(u.Downloaded))
}

// -----------------------------------------------------------------------------
// HTTP handlers
// -----------------------------------------------------------------------------

type server struct {
	client  *V3XClient
	metrics *exporterMetrics
	cfg     config
}

func (s *server) canLogin() bool {
	return s.cfg.Login != "" && s.cfg.Password != ""
}

// refresh fetches metrics, logging in (again) when the session is missing or expired.
func (s *server) refresh() {
	if !s.client.IsAuthenticated() && s.canLogin() {
		if err := s.client.Login(s.cfg.Login, s.cfg.Password); err != nil {
			fmt.Printf("[auth] Login failed: %v\n", err)
			return
		}
	}
	if !s.client.IsAuthenticated() {
		return
	}

	u, err := s.client.FetchMetrics()
	if errors.Is(err, errUnauthorized) && s.canLogin() {
		fmt.Println("[auth] Session expired, re-authenticating...")
		if err = s.client.Login(s.cfg.Login, s.cfg.Password); err == nil {
			u, err = s.client.FetchMetrics()
		}
	}
	if err != nil {
		fmt.Printf("[metrics] Error scraping metrics: %v\n", err)
		return
	}
	s.metrics.update(u)
}

func (s *server) metricsHandler(c *gin.Context) {
	s.refresh()
	c.Header("Content-Type", "text/plain; version=0.0.4")
	promhttp.Handler().ServeHTTP(c.Writer, c.Request)
}

func (s *server) healthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":        "healthy",
		"authenticated": s.client.IsAuthenticated(),
	})
}

// -----------------------------------------------------------------------------
// Entrypoint
// -----------------------------------------------------------------------------

func main() {
	cfg := loadConfig()

	client := newV3XClient(cfg.BaseURL)
	metrics := newExporterMetrics(prometheus.DefaultRegisterer)
	srv := &server{client: client, metrics: metrics, cfg: cfg}

	// Attempt auto-login on startup if credentials are provided.
	if srv.canLogin() {
		fmt.Println("[auth] Credentials found, attempting auto-login...")
		if err := client.Login(cfg.Login, cfg.Password); err != nil {
			fmt.Printf("[auth] Auto-login failed (non-fatal): %v\n", err)
		}
	} else {
		fmt.Println("[auth] No credentials provided, auto-login skipped")
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	r.GET(cfg.MetricsPath, srv.metricsHandler)
	r.GET("/health", srv.healthHandler)

	addr := ":" + cfg.Port
	fmt.Printf("[server] Starting v3x exporter on %s\n", addr)
	fmt.Printf("[server] Metrics available at: http://localhost%s%s\n", addr, cfg.MetricsPath)
	fmt.Printf("[server] Scrape interval: %v\n", cfg.ScrapeInterval)

	if err := r.Run(addr); err != nil {
		fmt.Printf("[server] Error starting server: %v\n", err)
		os.Exit(1)
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// parseDuration parses a simple duration string (e.g. "30s", "5m", "2h").
// Returns fallback if the string is empty or cannot be parsed.
func parseDuration(s string, fallback time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	units := []struct {
		suffix string
		mult   time.Duration
	}{
		{"h", time.Hour},
		{"m", time.Minute},
		{"s", time.Second},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			numStr := strings.TrimSuffix(s, u.suffix)
			var n int
			if _, err := fmt.Sscanf(numStr, "%d", &n); err == nil && n > 0 {
				return time.Duration(n) * u.mult
			}
		}
	}
	return fallback
}
