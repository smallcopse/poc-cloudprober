# poc-cloudprober

OpenStack API の正常性を [cloudprober](https://cloudprober.org/) で監視する PoC リポジトリです。
probe スクリプトを **Go / Python / Bash** の 3 バリアントで実装し、メトリクスを Prometheus 形式（`:9313/metrics`）で公開します。

## リポジトリ構成

```
cloudprober/
├── go/
│   ├── Dockerfile
│   ├── cloudprober.cfg
│   └── scripts/probe.go
├── python/
│   ├── Dockerfile
│   ├── cloudprober.cfg
│   ├── requirements.txt
│   └── scripts/probe.py
└── bash/
    ├── Dockerfile
    ├── cloudprober.cfg
    └── scripts/probe.sh
```

## コンテナイメージ

GitHub Actions / GitLab CI によって GHCR にプッシュされます。

| バリアント | イメージ |
|-----------|---------|
| Go        | `ghcr.io/<owner>/poc-cloudprober-go:latest`     |
| Python    | `ghcr.io/<owner>/poc-cloudprober-python:latest` |
| Bash      | `ghcr.io/<owner>/poc-cloudprober-bash:latest`   |

## 環境変数

### 必須

| 変数名 | 説明 |
|--------|------|
| `OS_AUTH_URL` | Keystone v3 エンドポイント（例: `http://keystone:5000/v3`） |
| `OS_USERNAME` | サービスアカウントのユーザー名 |
| `OS_PASSWORD` | サービスアカウントのパスワード |
| `OS_PROJECT_NAME` | スコープするプロジェクト名 |
| `OS_USER_DOMAIN_NAME` | ユーザードメイン（デフォルト: `Default`） |
| `OS_PROJECT_DOMAIN_NAME` | プロジェクトドメイン（デフォルト: `Default`） |
| `OS_REGION_NAME` | メトリクスに付与するリージョン名（デフォルト: `RegionOne`） |

### オプション（未設定のサービスはスキップ）

| 変数名 | 説明 |
|--------|------|
| `OS_NOVA_URL` | Nova API ベース URL（例: `http://nova:8774/v2.1`） |
| `OS_NEUTRON_URL` | Neutron API ベース URL（例: `http://neutron:9696/v2.0`） |
| `OS_GLANCE_URL` | Glance API ベース URL（例: `http://glance:9292/v2`） |
| `OS_CINDER_URL` | Cinder API ベース URL（例: `http://cinder:8776/v3`） |

### HTTP プロキシ（任意）

プロキシ環境変数を設定しない場合は直接接続になります。設定した場合は各実装が自動的に参照します。

| 変数名 | 説明 |
|--------|------|
| `HTTPS_PROXY` | HTTPS リクエストに使うプロキシ URL（例: `http://proxy.example.com:3128`） |
| `HTTP_PROXY` | HTTP リクエストに使うプロキシ URL |
| `NO_PROXY` | プロキシを経由しないホストのカンマ区切りリスト（例: `keystone.internal,.example.com`） |

> **実装別の参照方法**
> | バリアント | 仕組み |
> |-----------|--------|
> | Go | `http.DefaultTransport` が `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` を自動参照 |
> | Python | `requests` ライブラリが同変数を自動参照 |
> | Bash | `curl` が `http_proxy` / `https_proxy` / `no_proxy`（大小文字両対応）を自動参照 |

---

## OpenShift へのデプロイ

以下のマニフェスト例は **Go バリアント**を前提としています。Python / Bash バリアントを使う場合はイメージ名を変更してください。

### 1. Secret（OpenStack 認証情報）

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: openstack-credentials
  namespace: monitoring
type: Opaque
stringData:
  OS_AUTH_URL: "http://keystone.example.com:5000/v3"
  OS_USERNAME: "cloudprober"
  OS_PASSWORD: "changeme"
  OS_PROJECT_NAME: "service"
  OS_USER_DOMAIN_NAME: "Default"
  OS_PROJECT_DOMAIN_NAME: "Default"
  OS_REGION_NAME: "RegionOne"
  # 監視したいサービスの URL を追記する
  OS_NOVA_URL: "http://nova.example.com:8774/v2.1"
  OS_NEUTRON_URL: "http://neutron.example.com:9696/v2.0"
  OS_GLANCE_URL: "http://glance.example.com:9292/v2"
  OS_CINDER_URL: "http://cinder.example.com:8776/v3"
```

### 2. Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cloudprober-go
  namespace: monitoring
  labels:
    app: cloudprober-go
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cloudprober-go
  template:
    metadata:
      labels:
        app: cloudprober-go
    spec:
      # UID 65534 (nobody) で実行するため nonroot SCC が必要
      # oc adm policy add-scc-to-serviceaccount nonroot -z default -n monitoring
      securityContext:
        runAsNonRoot: true
        runAsUser: 65534
        runAsGroup: 65534
      containers:
        - name: cloudprober
          image: ghcr.io/<owner>/poc-cloudprober-go:latest
          ports:
            - name: metrics
              containerPort: 9313
              protocol: TCP
          envFrom:
            - secretRef:
                name: openstack-credentials
          # プロキシが不要な環境では env ブロックごと省略できる
          env:
            - name: HTTPS_PROXY
              value: "http://proxy.example.com:3128"
            - name: HTTP_PROXY
              value: "http://proxy.example.com:3128"
            - name: NO_PROXY
              value: "keystone.internal,nova.internal,.example.com"
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 200m
              memory: 128Mi
          readinessProbe:
            httpGet:
              path: /metrics
              port: metrics
            initialDelaySeconds: 5
            periodSeconds: 15
          livenessProbe:
            httpGet:
              path: /metrics
              port: metrics
            initialDelaySeconds: 10
            periodSeconds: 30
```

> **SCC について**
> OpenShift のデフォルト SCC（`restricted`）は任意の UID を拒否します。
> コンテナが UID 65534 固定で動作するため、対象 ServiceAccount に `nonroot` SCC を付与してください。
>
> ```bash
> oc adm policy add-scc-to-serviceaccount nonroot -z default -n monitoring
> ```

### 3. Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: cloudprober-go
  namespace: monitoring
  labels:
    app: cloudprober-go
spec:
  selector:
    app: cloudprober-go
  ports:
    - name: metrics
      port: 9313
      targetPort: metrics
      protocol: TCP
```

---

## Prometheus による監視

OpenShift の [User Workload Monitoring](https://docs.openshift.com/container-platform/latest/observability/monitoring/enabling-monitoring-for-user-defined-projects.html) または Prometheus Operator を使用します。

### ServiceMonitor

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: cloudprober-go
  namespace: monitoring
  labels:
    # User Workload Monitoring が検出するラベル
    app: cloudprober-go
spec:
  selector:
    matchLabels:
      app: cloudprober-go
  endpoints:
    - port: metrics
      path: /metrics
      interval: 60s
      scrapeTimeout: 15s
```

### 動作確認

```bash
# Pod が Running であることを確認
oc get pod -n monitoring -l app=cloudprober-go

# メトリクスエンドポイントへの疎通確認
oc exec -n monitoring deploy/cloudprober-go -- wget -qO- http://localhost:9313/metrics | grep openstack

# ServiceMonitor が認識されているか確認
oc get servicemonitor -n monitoring
```

---

## 公開メトリクス

cloudprober が `:9313/metrics` に公開するメトリクスの例です。

```
# probe の成否（1=成功, 0=失敗）
probe_success{probe="openstack_identity",dst="keystone",service="identity",region="RegionOne"} 1

# HTTP ステータスコード
probe_openstack_identity_http_code{probe="openstack_identity",dst="keystone",...} 200

# 認証エラー時のみ出力
probe_openstack_compute_auth_failed{...} 1

# サービス URL 未設定時のみ出力
probe_openstack_volume_not_configured{...} 1
```

### PromQL クエリ例

```promql
# 直近5分間で1回でも失敗したサービス
min_over_time(probe_success{probe=~"openstack_.*"}[5m]) == 0

# HTTP ステータスコードが 2xx でないサービス
probe_success{probe=~"openstack_.*"} == 0

# 認証エラーが発生しているか
sum by (probe, region) (probe_openstack_identity_auth_failed) > 0
```
