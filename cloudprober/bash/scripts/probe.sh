#!/bin/bash
# OpenStack API health probe for cloudprober external probe (ONCE mode).
#
# Usage: probe.sh <service>
#   service: identity | compute | network | image | volume
#
# Exit 0  → probe success (HTTP 2xx)
# Exit 1  → probe failure (auth error, non-2xx, or network error)
#
# stdout lines are consumed by cloudprober as additional metrics:
#   http_code   <value>
#   auth_failed <1>        (only on auth error)
#   not_configured <1>     (only when the service URL env var is unset)

set -uo pipefail

SERVICE="${1:?Usage: probe.sh <identity|compute|network|image|volume>}"

TOKEN_CACHE="/tmp/os_token"
TOKEN_TIME="/tmp/os_token_time"
MAX_TOKEN_AGE="${OS_TOKEN_MAX_AGE:-3000}"   # seconds; default 50 min (tokens live 1 h)
PROBE_TIMEOUT="${OS_PROBE_TIMEOUT:-10}"

log() { echo "[$(date -u +%FT%TZ)] probe.sh [$SERVICE] $*" >&2; }

# ── Token management ─────────────────────────────────────────────────────────

get_token() {
    # Return cached token if it is still young enough
    if [[ -f "$TOKEN_CACHE" && -f "$TOKEN_TIME" ]]; then
        local age=$(( $(date +%s) - $(cat "$TOKEN_TIME") ))
        if [[ "$age" -lt "$MAX_TOKEN_AGE" ]]; then
            cat "$TOKEN_CACHE"
            return 0
        fi
    fi

    # Build Keystone v3 password auth payload
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

    if [[ "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
        log "Keystone auth failed: HTTP $http_code"
        return 1
    fi

    local token
    token=$(grep -i "^x-subject-token:" /tmp/os_auth_headers | head -1 | tr -d '\r' | awk '{print $2}')
    if [[ -z "$token" ]]; then
        log "X-Subject-Token header not found in Keystone response"
        return 1
    fi

    printf '%s' "$token" > "$TOKEN_CACHE"
    date +%s > "$TOKEN_TIME"
    printf '%s' "$token"
}

# ── Endpoint check ───────────────────────────────────────────────────────────

check_endpoint() {
    local url="$1"
    local token="${2:-}"

    local -a curl_args=(-s -o /dev/null -w "%{http_code}"
        -X GET "$url"
        --connect-timeout 5
        --max-time "$PROBE_TIMEOUT")

    [[ -n "$token" ]] && curl_args+=(-H "X-Auth-Token: $token")

    local http_code
    http_code=$(curl "${curl_args[@]}") || http_code="000"

    echo "http_code $http_code"

    if [[ "$http_code" -ge 200 && "$http_code" -lt 300 ]]; then
        return 0
    else
        log "Endpoint returned HTTP $http_code"
        return 1
    fi
}

# ── Resolve URL per service ───────────────────────────────────────────────────

URL=""
NEEDS_TOKEN=true

case "$SERVICE" in
    identity)
        # Keystone v3 root is public; also implicitly tests connectivity
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

# Gracefully skip unconfigured optional services
if [[ "$NEEDS_TOKEN" == "true" && -z "$URL" ]]; then
    log "URL not configured — skipping"
    echo "not_configured 1"
    exit 0
fi

# ── Authenticate (if required) ────────────────────────────────────────────────

TOKEN=""
if [[ "$NEEDS_TOKEN" == "true" ]]; then
    TOKEN=$(get_token) || {
        echo "auth_failed 1"
        exit 1
    }
fi

# ── Run probe ────────────────────────────────────────────────────────────────

check_endpoint "$URL" "$TOKEN"
