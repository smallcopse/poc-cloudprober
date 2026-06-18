#!/usr/bin/env python3
"""
OpenStack API health probe for cloudprober external probe (ONCE mode).

Usage: probe.py <service>
  service: identity | compute | network | image | volume

Exit 0  → probe success (HTTP 2xx)
Exit 1  → probe failure

stdout lines are consumed by cloudprober as additional metrics:
  http_code <value>
  auth_failed 1       (only on auth error)
  not_configured 1    (only when the service URL env var is unset)
"""

import json
import os
import sys
import time

import requests

TOKEN_CACHE = "/tmp/os_token"
TOKEN_TIME = "/tmp/os_token_time"
MAX_TOKEN_AGE = int(os.environ.get("OS_TOKEN_MAX_AGE", "3000"))  # seconds


def log(service: str, msg: str) -> None:
    ts = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    print(f"[{ts}] probe.py [{service}] {msg}", file=sys.stderr)


def get_cached_token() -> str | None:
    try:
        with open(TOKEN_TIME) as f:
            age = int(time.time()) - int(f.read().strip())
        if age < MAX_TOKEN_AGE:
            with open(TOKEN_CACHE) as f:
                return f.read().strip()
    except (FileNotFoundError, ValueError):
        pass
    return None


def refresh_token() -> str:
    payload = {
        "auth": {
            "identity": {
                "methods": ["password"],
                "password": {
                    "user": {
                        "name": os.environ["OS_USERNAME"],
                        "password": os.environ["OS_PASSWORD"],
                        "domain": {"name": os.environ.get("OS_USER_DOMAIN_NAME", "Default")},
                    }
                },
            },
            "scope": {
                "project": {
                    "name": os.environ["OS_PROJECT_NAME"],
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
    resp.raise_for_status()

    token = resp.headers["X-Subject-Token"]
    with open(TOKEN_CACHE, "w") as f:
        f.write(token)
    with open(TOKEN_TIME, "w") as f:
        f.write(str(int(time.time())))
    return token


def get_token() -> str:
    token = get_cached_token()
    return token if token else refresh_token()


def check_endpoint(url: str, token: str | None) -> int:
    timeout = int(os.environ.get("OS_PROBE_TIMEOUT", "10"))
    headers = {"X-Auth-Token": token} if token else {}
    resp = requests.get(url, headers=headers, timeout=timeout)
    return resp.status_code


SERVICE_MAP = {
    "identity": ("OS_AUTH_URL",    False),
    "compute":  ("OS_NOVA_URL",    True),
    "network":  ("OS_NEUTRON_URL", True),
    "image":    ("OS_GLANCE_URL",  True),
    "volume":   ("OS_CINDER_URL",  True),
}


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

    if needs_auth and not url:
        log(service, "URL not configured — skipping")
        print("not_configured 1")
        return 0

    token = None
    if needs_auth:
        try:
            token = get_token()
        except Exception as e:
            log(service, f"authentication error: {e}")
            print("auth_failed 1")
            return 1

    try:
        http_code = check_endpoint(url, token)
    except Exception as e:
        log(service, f"endpoint error: {e}")
        print("http_code 0")
        return 1

    print(f"http_code {http_code}")

    if 200 <= http_code < 300:
        return 0

    log(service, f"endpoint returned HTTP {http_code}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
