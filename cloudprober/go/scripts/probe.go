// OpenStack API health probe for cloudprober external probe (ONCE mode).
//
// Usage: probe <service>
//   service: identity | compute | network | image | volume
//
// Exit 0  → probe success (HTTP 2xx)
// Exit 1  → probe failure
//
// stdout lines are consumed by cloudprober as additional metrics:
//   http_code <value>
//   auth_failed 1       (only on auth error)
//   not_configured 1    (only when the service URL env var is unset)
//
// Build: go build -o probe probe.go

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	tokenCache = "/tmp/os_token"
	tokenTime  = "/tmp/os_token_time"
)

func maxTokenAge() time.Duration {
	if v := os.Getenv("OS_TOKEN_MAX_AGE"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return 50 * time.Minute
}

func logf(service, format string, args ...any) {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	log.Printf("[%s] probe.go [%s] %s", ts, service, fmt.Sprintf(format, args...))
}

// ── Token management ──────────────────────────────────────────────────────────

func getCachedToken() string {
	tb, err := os.ReadFile(tokenTime)
	if err != nil {
		return ""
	}
	cachedAt, err := strconv.ParseInt(strings.TrimSpace(string(tb)), 10, 64)
	if err != nil {
		return ""
	}
	if time.Since(time.Unix(cachedAt, 0)) >= maxTokenAge() {
		return ""
	}
	tok, err := os.ReadFile(tokenCache)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(tok))
}

func refreshToken() (string, error) {
	type domain struct {
		Name string `json:"name"`
	}
	type user struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		Domain   domain `json:"domain"`
	}
	type project struct {
		Name   string `json:"name"`
		Domain domain `json:"domain"`
	}
	type payload struct {
		Auth struct {
			Identity struct {
				Methods  []string `json:"methods"`
				Password struct {
					User user `json:"user"`
				} `json:"password"`
			} `json:"identity"`
			Scope struct {
				Project project `json:"project"`
			} `json:"scope"`
		} `json:"auth"`
	}

	var p payload
	p.Auth.Identity.Methods = []string{"password"}
	p.Auth.Identity.Password.User = user{
		Name:     os.Getenv("OS_USERNAME"),
		Password: os.Getenv("OS_PASSWORD"),
		Domain:   domain{Name: envOrDefault("OS_USER_DOMAIN_NAME", "Default")},
	}
	p.Auth.Scope.Project = project{
		Name:   os.Getenv("OS_PROJECT_NAME"),
		Domain: domain{Name: envOrDefault("OS_PROJECT_DOMAIN_NAME", "Default")},
	}

	body, _ := json.Marshal(p)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(
		os.Getenv("OS_AUTH_URL")+"/auth/tokens",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return "", fmt.Errorf("keystone request: %w", err)
	}
	defer func() { io.Copy(io.Discard, resp.Body); resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("keystone auth failed: HTTP %d", resp.StatusCode)
	}

	token := resp.Header.Get("X-Subject-Token")
	if token == "" {
		return "", fmt.Errorf("X-Subject-Token header not found")
	}

	if err := os.WriteFile(tokenCache, []byte(token), 0600); err != nil {
		return "", err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.WriteFile(tokenTime, []byte(ts), 0600); err != nil {
		return "", err
	}
	return token, nil
}

func getToken() (string, error) {
	if tok := getCachedToken(); tok != "" {
		return tok, nil
	}
	return refreshToken()
}

// ── Endpoint check ────────────────────────────────────────────────────────────

func checkEndpoint(url, token string) (int, error) {
	timeout := 10
	if v := os.Getenv("OS_PROBE_TIMEOUT"); v != "" {
		if t, err := strconv.Atoi(v); err == nil {
			timeout = t
		}
	}

	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	if token != "" {
		req.Header.Set("X-Auth-Token", token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}
	defer func() { io.Copy(io.Discard, resp.Body); resp.Body.Close() }()
	return resp.StatusCode, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ── Main ──────────────────────────────────────────────────────────────────────

type svcConf struct {
	envVar    string
	needsAuth bool
}

var serviceMap = map[string]svcConf{
	"identity": {"OS_AUTH_URL",    false},
	"compute":  {"OS_NOVA_URL",    true},
	"network":  {"OS_NEUTRON_URL", true},
	"image":    {"OS_GLANCE_URL",  true},
	"volume":   {"OS_CINDER_URL",  true},
}

func main() {
	log.SetFlags(0)

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: probe <identity|compute|network|image|volume>")
		os.Exit(1)
	}

	service := os.Args[1]
	conf, ok := serviceMap[service]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown service: %s\n", service)
		os.Exit(1)
	}

	url := os.Getenv(conf.envVar)
	if conf.needsAuth && url == "" {
		logf(service, "URL not configured — skipping")
		fmt.Println("not_configured 1")
		os.Exit(0)
	}

	var token string
	if conf.needsAuth {
		var err error
		token, err = getToken()
		if err != nil {
			logf(service, "authentication error: %v", err)
			fmt.Println("auth_failed 1")
			os.Exit(1)
		}
	}

	httpCode, err := checkEndpoint(url, token)
	if err != nil {
		logf(service, "endpoint error: %v", err)
		fmt.Println("http_code 0")
		os.Exit(1)
	}

	fmt.Printf("http_code %d\n", httpCode)

	if httpCode >= 200 && httpCode < 300 {
		os.Exit(0)
	}

	logf(service, "endpoint returned HTTP %d", httpCode)
	os.Exit(1)
}
