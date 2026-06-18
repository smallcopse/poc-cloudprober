// cloudprober 外部プローブ（ONCEモード）用 OpenStack API 正常性確認スクリプト。
//
// 使い方: probe <service>
//   service: identity | compute | network | image | volume
//
// 終了コード:
//   0 → プローブ成功（HTTP 2xx）
//   1 → プローブ失敗
//
// stdout に出力した行は cloudprober の追加メトリクスとして取り込まれる:
//   http_code <値>
//   auth_failed 1       （認証エラー時のみ）
//   not_configured 1    （サービスURLの環境変数が未設定の場合のみ）
//
// ビルド: go build -o probe probe.go

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
	// トークンのファイルキャッシュパス（サブプロセス再起動をまたいで再利用する）
	tokenCache = "/tmp/os_token"
	tokenTime  = "/tmp/os_token_time"
)

// キャッシュ有効期限を返す。OS_TOKEN_MAX_AGE が未設定の場合は50分を既定値とする
func maxTokenAge() time.Duration {
	if v := os.Getenv("OS_TOKEN_MAX_AGE"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return 50 * time.Minute
}

// 標準エラー出力にタイムスタンプ付きのログを書き出す
func logf(service, format string, args ...any) {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	log.Printf("[%s] probe.go [%s] %s", ts, service, fmt.Sprintf(format, args...))
}

// ── トークン管理 ──────────────────────────────────────────────────────────────

// キャッシュファイルが存在し有効期限内であればトークンを返す
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

// Keystone v3 パスワード認証でトークンを取得し、キャッシュファイルに保存する
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

	// Keystone v3 認証リクエストのペイロードを組み立てる
	var p payload
	p.Auth.Identity.Methods = []string{"password"}
	p.Auth.Identity.Password.User = user{
		Name:     os.Getenv("OS_USERNAME"),
		Password: os.Getenv("OS_PASSWORD"),
		// ユーザードメインが未指定の場合は "Default" を使用する
		Domain: domain{Name: envOrDefault("OS_USER_DOMAIN_NAME", "Default")},
	}
	p.Auth.Scope.Project = project{
		Name: os.Getenv("OS_PROJECT_NAME"),
		// プロジェクトドメインが未指定の場合は "Default" を使用する
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

	// HTTP 4xx / 5xx の場合はエラーを返して呼び出し元でハンドリングする
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("keystone auth failed: HTTP %d", resp.StatusCode)
	}

	// トークンはレスポンスボディではなくヘッダー X-Subject-Token に含まれる
	token := resp.Header.Get("X-Subject-Token")
	if token == "" {
		return "", fmt.Errorf("X-Subject-Token header not found")
	}

	// 次回のプローブ実行で再利用できるようにファイルへ保存する
	if err := os.WriteFile(tokenCache, []byte(token), 0600); err != nil {
		return "", err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.WriteFile(tokenTime, []byte(ts), 0600); err != nil {
		return "", err
	}
	return token, nil
}

// キャッシュが有効であればそれを返し、期限切れの場合は再取得する
func getToken() (string, error) {
	if tok := getCachedToken(); tok != "" {
		return tok, nil
	}
	return refreshToken()
}

// ── エンドポイント確認 ────────────────────────────────────────────────────────

// 指定 URL に GET リクエストを送り HTTP ステータスコードを返す
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
	// identity サービスは認証不要のためトークンがない場合はヘッダーを付与しない
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

// ── ヘルパー ──────────────────────────────────────────────────────────────────

// 環境変数 key を返す。未設定の場合は def を返す
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ── メイン ────────────────────────────────────────────────────────────────────

// キー: サービス名, 値: (URLを格納する環境変数名, 認証トークンが必要か)
type svcConf struct {
	envVar    string
	needsAuth bool
}

var serviceMap = map[string]svcConf{
	"identity": {"OS_AUTH_URL",    false}, // Keystone はパブリックエンドポイントのため認証不要
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
	// URLが未設定のサービスはスキップ扱いとし、アラートを発生させない
	if conf.needsAuth && url == "" {
		logf(service, "URL not configured — skipping")
		fmt.Println("not_configured 1")
		os.Exit(0)
	}

	// 認証が必要なサービスは Keystone からトークンを取得する
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

	// エンドポイントへの疎通確認を実行する
	httpCode, err := checkEndpoint(url, token)
	if err != nil {
		logf(service, "endpoint error: %v", err)
		fmt.Println("http_code 0")
		os.Exit(1)
	}

	// HTTP ステータスコードを cloudprober のメトリクスとして出力する
	fmt.Printf("http_code %d\n", httpCode)

	// 2xx であれば正常、それ以外は異常と判定する
	if httpCode >= 200 && httpCode < 300 {
		os.Exit(0)
	}

	logf(service, "endpoint returned HTTP %d", httpCode)
	os.Exit(1)
}
