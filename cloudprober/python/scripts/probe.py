#!/usr/bin/env python3
"""
cloudprober 外部プローブ（ONCEモード）用 OpenStack API 正常性確認スクリプト。

使い方: probe.py <service>
  service: identity | compute | network | image | volume

終了コード:
  0 → プローブ成功（HTTP 2xx）
  1 → プローブ失敗

stdout に出力した行は cloudprober の追加メトリクスとして取り込まれる:
  http_code <値>
  auth_failed 1       （認証エラー時のみ）
  not_configured 1    （サービスURLの環境変数が未設定の場合のみ）
"""

import os
import sys
import time

import requests

# トークンのファイルキャッシュパス（サブプロセス再起動をまたいで再利用する）
TOKEN_CACHE = "/tmp/os_token"
TOKEN_TIME  = "/tmp/os_token_time"

# キャッシュ有効期限（秒）。OpenStack トークンのデフォルト寿命は1時間なので50分を既定値とする
MAX_TOKEN_AGE = int(os.environ.get("OS_TOKEN_MAX_AGE", "3000"))


def log(service: str, msg: str) -> None:
    """標準エラー出力にタイムスタンプ付きのログを書き出す。"""
    ts = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    print(f"[{ts}] probe.py [{service}] {msg}", file=sys.stderr)


# ── トークン管理 ──────────────────────────────────────────────────────────────

def get_cached_token() -> str | None:
    """キャッシュファイルが存在し有効期限内であればトークンを返す。"""
    try:
        with open(TOKEN_TIME) as f:
            age = int(time.time()) - int(f.read().strip())
        if age < MAX_TOKEN_AGE:
            with open(TOKEN_CACHE) as f:
                return f.read().strip()
    except (FileNotFoundError, ValueError):
        # キャッシュファイルが存在しない、または破損している場合は再取得する
        pass
    return None


def refresh_token() -> str:
    """Keystone v3 パスワード認証でトークンを取得し、キャッシュファイルに保存する。"""
    # Keystone v3 認証リクエストのペイロードを組み立てる
    payload = {
        "auth": {
            "identity": {
                "methods": ["password"],
                "password": {
                    "user": {
                        "name":     os.environ["OS_USERNAME"],
                        "password": os.environ["OS_PASSWORD"],
                        # ユーザードメインが未指定の場合は "Default" を使用する
                        "domain": {"name": os.environ.get("OS_USER_DOMAIN_NAME", "Default")},
                    }
                },
            },
            "scope": {
                "project": {
                    "name": os.environ["OS_PROJECT_NAME"],
                    # プロジェクトドメインが未指定の場合は "Default" を使用する
                    "domain": {"name": os.environ.get("OS_PROJECT_DOMAIN_NAME", "Default")},
                }
            },
        }
    }

    resp = requests.post(
        os.environ["OS_AUTH_URL"] + "/auth/tokens",
        json=payload,
        timeout=30,
    )
    # HTTP 4xx / 5xx の場合は例外を送出して呼び出し元でキャッチする
    resp.raise_for_status()

    # トークンはレスポンスボディではなくヘッダー X-Subject-Token に含まれる
    token = resp.headers["X-Subject-Token"]

    # 次回のプローブ実行で再利用できるようにファイルへ保存する
    with open(TOKEN_CACHE, "w") as f:
        f.write(token)
    with open(TOKEN_TIME, "w") as f:
        f.write(str(int(time.time())))

    return token


def get_token() -> str:
    """キャッシュが有効であればそれを返し、期限切れの場合は再取得する。"""
    token = get_cached_token()
    return token if token else refresh_token()


# ── エンドポイント確認 ────────────────────────────────────────────────────────

def check_endpoint(url: str, token: str | None) -> int:
    """指定 URL に GET リクエストを送り HTTP ステータスコードを返す。"""
    timeout = int(os.environ.get("OS_PROBE_TIMEOUT", "10"))
    # identity サービスは認証不要のためトークンがない場合はヘッダーを付与しない
    headers = {"X-Auth-Token": token} if token else {}
    resp = requests.get(url, headers=headers, timeout=timeout)
    return resp.status_code


# ── サービス定義 ──────────────────────────────────────────────────────────────

# キー: サービス名, 値: (URLを格納する環境変数名, 認証トークンが必要か)
SERVICE_MAP = {
    "identity": ("OS_AUTH_URL",    False),  # Keystone はパブリックエンドポイントのため認証不要
    "compute":  ("OS_NOVA_URL",    True),
    "network":  ("OS_NEUTRON_URL", True),
    "image":    ("OS_GLANCE_URL",  True),
    "volume":   ("OS_CINDER_URL",  True),
}


# ── エントリポイント ──────────────────────────────────────────────────────────

def main() -> int:
    if len(sys.argv) < 2:
        print("Usage: probe.py <identity|compute|network|image|volume>", file=sys.stderr)
        return 1

    service = sys.argv[1]
    if service not in SERVICE_MAP:
        print(f"Unknown service: {service}", file=sys.stderr)
        return 1

    env_var, needs_auth = SERVICE_MAP[service]
    url = os.environ.get(env_var, "")

    # URLが未設定のサービスはスキップ扱いとし、アラートを発生させない
    if needs_auth and not url:
        log(service, "URL not configured — skipping")
        print("not_configured 1")
        return 0

    # 認証が必要なサービスは Keystone からトークンを取得する
    token = None
    if needs_auth:
        try:
            token = get_token()
        except Exception as e:
            log(service, f"authentication error: {e}")
            print("auth_failed 1")
            return 1

    # エンドポイントへの疎通確認を実行する
    try:
        http_code = check_endpoint(url, token)
    except Exception as e:
        log(service, f"endpoint error: {e}")
        print("http_code 0")
        return 1

    # HTTP ステータスコードを cloudprober のメトリクスとして出力する
    print(f"http_code {http_code}")

    # 2xx であれば正常、それ以外は異常と判定する
    if 200 <= http_code < 300:
        return 0

    log(service, f"endpoint returned HTTP {http_code}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
