#!/bin/bash
# cloudprober 外部プローブ（ONCEモード）用 OpenStack API 正常性確認スクリプト。
#
# 使い方: probe.sh <service>
#   service: identity | compute | network | image | volume
#
# 終了コード:
#   0 → プローブ成功（HTTP 2xx）
#   1 → プローブ失敗（認証エラー、非2xx、またはネットワークエラー）
#
# stdout に出力した行は cloudprober の追加メトリクスとして取り込まれる:
#   http_code   <値>
#   auth_failed <1>        （認証エラー時のみ）
#   not_configured <1>     （サービスURLの環境変数が未設定の場合のみ）

set -uo pipefail

SERVICE="${1:?Usage: probe.sh <identity|compute|network|image|volume>}"

# トークンのファイルキャッシュパス（サブプロセス再起動をまたいで再利用する）
TOKEN_CACHE="/tmp/os_token"
TOKEN_TIME="/tmp/os_token_time"
# キャッシュ有効期限（秒）。OpenStack トークンのデフォルト寿命は1時間なので50分を既定値とする
MAX_TOKEN_AGE="${OS_TOKEN_MAX_AGE:-3000}"
PROBE_TIMEOUT="${OS_PROBE_TIMEOUT:-10}"

# 標準エラー出力にタイムスタンプ付きのログを書き出す
log() { echo "[$(date -u +%FT%TZ)] probe.sh [$SERVICE] $*" >&2; }

# ── トークン管理 ─────────────────────────────────────────────────────────────

get_token() {
    # キャッシュファイルが存在し有効期限内であればトークンを返す
    if [[ -f "$TOKEN_CACHE" && -f "$TOKEN_TIME" ]]; then
        local age=$(( $(date +%s) - $(cat "$TOKEN_TIME") ))
        if [[ "$age" -lt "$MAX_TOKEN_AGE" ]]; then
            cat "$TOKEN_CACHE"
            return 0
        fi
    fi

    # Keystone v3 認証リクエストのペイロードを組み立てる
    local payload
    payload=$(cat <<EOF
{
  "auth": {
    "identity": {
      "methods": ["password"],
      "password": {
        "user": {
          "name": "${OS_USERNAME}",
          "password": "${OS_PASSWORD}",
          "domain": {"name": "${OS_USER_DOMAIN_NAME:-Default}"}
        }
      }
    },
    "scope": {
      "project": {
        "name": "${OS_PROJECT_NAME}",
        "domain": {"name": "${OS_PROJECT_DOMAIN_NAME:-Default}"}
      }
    }
  }
}
EOF
    )

    local http_code
    http_code=$(curl -s \
        -D /tmp/os_auth_headers \
        -o /dev/null \
        -w "%{http_code}" \
        -X POST "${OS_AUTH_URL}/auth/tokens" \
        -H "Content-Type: application/json" \
        -d "$payload" \
        --connect-timeout 10 \
        --max-time 30) || http_code="000"

    # HTTP 4xx / 5xx の場合はエラーログを出力して終了する
    if [[ "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
        log "Keystone auth failed: HTTP $http_code"
        return 1
    fi

    # トークンはレスポンスボディではなくヘッダー X-Subject-Token に含まれる
    local token
    token=$(grep -i "^x-subject-token:" /tmp/os_auth_headers | head -1 | tr -d '\r' | awk '{print $2}')
    if [[ -z "$token" ]]; then
        log "X-Subject-Token header not found in Keystone response"
        return 1
    fi

    # 次回のプローブ実行で再利用できるようにファイルへ保存する
    printf '%s' "$token" > "$TOKEN_CACHE"
    date +%s > "$TOKEN_TIME"
    printf '%s' "$token"
}

# ── エンドポイント確認 ───────────────────────────────────────────────────────

check_endpoint() {
    local url="$1"
    local token="${2:-}"

    local -a curl_args=(-s -o /dev/null -w "%{http_code}"
        -X GET "$url"
        --connect-timeout 5
        --max-time "$PROBE_TIMEOUT")

    # identity サービスは認証不要のためトークンがない場合はヘッダーを付与しない
    [[ -n "$token" ]] && curl_args+=(-H "X-Auth-Token: $token")

    local http_code
    http_code=$(curl "${curl_args[@]}") || http_code="000"

    # HTTP ステータスコードを cloudprober のメトリクスとして出力する
    echo "http_code $http_code"

    # 2xx であれば正常、それ以外は異常と判定する
    if [[ "$http_code" -ge 200 && "$http_code" -lt 300 ]]; then
        return 0
    else
        log "Endpoint returned HTTP $http_code"
        return 1
    fi
}

# ── サービス定義 ───────────────────────────────────────────────────────────────

URL=""
NEEDS_TOKEN=true

case "$SERVICE" in
    identity)
        # Keystone はパブリックエンドポイントのため認証不要。接続確認も兼ねる
        URL="${OS_AUTH_URL:?OS_AUTH_URL must be set}"
        NEEDS_TOKEN=false
        ;;
    compute)  URL="${OS_NOVA_URL:-}"    ;;
    network)  URL="${OS_NEUTRON_URL:-}" ;;
    image)    URL="${OS_GLANCE_URL:-}"  ;;
    volume)   URL="${OS_CINDER_URL:-}"  ;;
    *)
        log "Unknown service '$SERVICE'"
        exit 1
        ;;
esac

# URLが未設定のサービスはスキップ扱いとし、アラートを発生させない
if [[ "$NEEDS_TOKEN" == "true" && -z "$URL" ]]; then
    log "URL not configured — skipping"
    echo "not_configured 1"
    exit 0
fi

# ── 認証（必要な場合のみ） ────────────────────────────────────────────────────

TOKEN=""
# 認証が必要なサービスは Keystone からトークンを取得する
if [[ "$NEEDS_TOKEN" == "true" ]]; then
    TOKEN=$(get_token) || {
        echo "auth_failed 1"
        exit 1
    }
fi

# ── エンドポイントへの疎通確認を実行する ────────────────────────────────────

check_endpoint "$URL" "$TOKEN"
