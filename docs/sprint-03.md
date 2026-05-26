# Спринт 3 — Kubernetes и Helm для GophProfile

## Контекст

К концу второго спринта сервис `GophProfile` полностью наблюдаем: есть метрики Prometheus, распределённый трейсинг через Jaeger и централизованное логирование через Grafana Loki. Всё это работает локально через `docker compose up`.

Следующий шаг — подготовить сервис к реальным нагрузкам: перенести инфраструктуру в Kubernetes, обеспечить масштабируемость и безопасность, упаковать всё в Helm Chart и подготовить проект к продакшену.

Работаем с локальным кластером на **Rancher Desktop**. Полученные навыки переносятся в любое K8s-окружение.

## Цель спринта 3

Задеплоить GophProfile в Kubernetes: написать манифесты, настроить автомасштабирование и health-пробы, подключить Prometheus через ServiceMonitor, добавить NetworkPolicy и RBAC, упаковать в Helm Chart, обеспечить Graceful Shutdown и итоговую документацию.

---

## 1. Базовая инфраструктура в Kubernetes

Написать манифесты для всех компонентов: приложение, конфиги, секреты, маршрутизация трафика.

### 1.1 Deployment

Манифест `k8s/base/deployment.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: avatar-service
  labels:
    app: avatar-service
spec:
  replicas: 2
  selector:
    matchLabels:
      app: avatar-service
  template:
    metadata:
      labels:
        app: avatar-service
    spec:
      serviceAccountName: avatar-service
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
      containers:
      - name: server
        image: avatar-service:latest
        ports:
        - name: http
          containerPort: 8080
        - name: metrics
          containerPort: 9090
        envFrom:
        - configMapRef:
            name: avatar-service-config
        - secretRef:
            name: avatar-service-secrets
        resources:
          requests:
            cpu: "100m"
            memory: "128Mi"
          limits:
            cpu: "500m"
            memory: "512Mi"
        livenessProbe:
          httpGet:
            path: /health
            port: http
          initialDelaySeconds: 10
          periodSeconds: 30
        readinessProbe:
          httpGet:
            path: /health/ready
            port: http
          initialDelaySeconds: 5
          periodSeconds: 10
```

### 1.2 Service и Ingress

`k8s/base/service.yaml`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: avatar-service
  labels:
    app: avatar-service
spec:
  selector:
    app: avatar-service
  ports:
  - name: http
    port: 80
    targetPort: http
  - name: metrics
    port: 9090
    targetPort: metrics
```

`k8s/base/ingress.yaml`:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: avatar-service
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "10m"
spec:
  rules:
  - host: gophprofile.local
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: avatar-service
            port:
              name: http
```

### 1.3 ConfigMap и Secret

`k8s/base/configmap.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: avatar-service-config
data:
  APP_PORT: "8080"
  METRICS_PORT: "9090"
  LOG_LEVEL: "info"
  DB_HOST: "postgres"
  DB_PORT: "5432"
  DB_NAME: "gophprofile"
  MINIO_ENDPOINT: "minio:9000"
  RABBITMQ_HOST: "rabbitmq"
  RABBITMQ_PORT: "5672"
```

`k8s/base/secret.yaml`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: avatar-service-secrets
type: Opaque
stringData:
  DB_USER: "gophprofile"
  DB_PASSWORD: "changeme"
  MINIO_ACCESS_KEY: "minioadmin"
  MINIO_SECRET_KEY: "minioadmin"
  RABBITMQ_USER: "guest"
  RABBITMQ_PASSWORD: "guest"
```

> В продакшене Secret управляется через Vault или sealed-secrets, не коммитится в репо.

---

## 2. Масштабируемость

Настроить горизонтальное автомасштабирование по CPU/RAM и health-пробы для корректного управления трафиком.

### 2.1 HorizontalPodAutoscaler

`k8s/base/hpa.yaml`:

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: avatar-service-hpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: avatar-service
  minReplicas: 2
  maxReplicas: 10
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
  - type: Resource
    resource:
      name: memory
      target:
        type: Utilization
        averageUtilization: 80
```

### 2.2 Liveness и Readiness Probes

Оба эндпоинта уже реализованы в спринте 1 (`/health`). Для спринта 3 нужно разделить:

- `GET /health` — liveness: сервис жив (процесс не завис)
- `GET /health/ready` — readiness: сервис готов принимать трафик (БД доступна, RabbitMQ подключён)

```go
// internal/handlers/health.go
func (h *HealthHandler) Liveness(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(map[string]string{"status": "alive"})
}

func (h *HealthHandler) Readiness(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
    defer cancel()

    if err := h.db.PingContext(ctx); err != nil {
        w.WriteHeader(http.StatusServiceUnavailable)
        json.NewEncoder(w).Encode(map[string]string{"status": "not ready", "reason": "db"})
        return
    }
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}
```

---

## 3. Мониторинг в Kubernetes

Подключить Prometheus Operator: ServiceMonitor автоматически обнаруживает поды и собирает метрики с `/metrics`.

### 3.1 ServiceMonitor

`k8s/base/servicemonitor.yaml`:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: avatar-service
  labels:
    app: avatar-service
spec:
  selector:
    matchLabels:
      app: avatar-service
  endpoints:
  - port: metrics
    interval: 30s
    path: /metrics
```

> ServiceMonitor работает только при установленном Prometheus Operator (`kube-prometheus-stack` Helm chart). Если оператора нет — добавить таргет вручную через `prometheus.yml`.

---

## 4. Безопасность

Ограничить сетевой доступ и права подов по принципу минимальных привилегий.

### 4.1 NetworkPolicy

`k8s/base/networkpolicy.yaml`:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: avatar-service-netpol
spec:
  podSelector:
    matchLabels:
      app: avatar-service
  policyTypes:
  - Ingress
  - Egress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: ingress-nginx
    ports:
    - port: 8080
  - from:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: monitoring
    ports:
    - port: 9090
  egress:
  - to:
    - podSelector:
        matchLabels:
          app: postgres
    ports:
    - port: 5432
  - to:
    - podSelector:
        matchLabels:
          app: minio
    ports:
    - port: 9000
  - to:
    - podSelector:
        matchLabels:
          app: rabbitmq
    ports:
    - port: 5672
```

### 4.2 RBAC и SecurityContext

`k8s/base/rbac.yaml`:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: avatar-service
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: avatar-service
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: avatar-service
subjects:
- kind: ServiceAccount
  name: avatar-service
roleRef:
  kind: Role
  name: avatar-service
  apiGroup: rbac.authorization.k8s.io
```

SecurityContext в Deployment (уже приведён выше):

```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 1000
  readOnlyRootFilesystem: true
  allowPrivilegeEscalation: false
  capabilities:
    drop: ["ALL"]
```

---

## 5. Helm Chart

Упаковать все манифесты в Helm Chart для управления релизами в разных окружениях.

### 5.1 Структура Chart

```
helm/
└── avatar-service/
    ├── Chart.yaml
    ├── values.yaml
    ├── values-dev.yaml
    ├── values-prod.yaml
    └── templates/
        ├── _helpers.tpl
        ├── deployment.yaml
        ├── service.yaml
        ├── ingress.yaml
        ├── configmap.yaml
        ├── secret.yaml
        ├── hpa.yaml
        ├── networkpolicy.yaml
        ├── rbac.yaml
        ├── servicemonitor.yaml
        └── hooks/
            └── migrate.yaml
```

### 5.2 Chart.yaml

```yaml
apiVersion: v2
name: avatar-service
description: GophProfile avatar microservice
type: application
version: 0.1.0
appVersion: "1.0.0"
```

### 5.3 values.yaml (базовые значения)

```yaml
replicaCount: 2

image:
  repository: avatar-service
  tag: latest
  pullPolicy: IfNotPresent

service:
  httpPort: 80
  metricsPort: 9090

ingress:
  enabled: true
  host: gophprofile.local

resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 512Mi

hpa:
  enabled: true
  minReplicas: 2
  maxReplicas: 10
  cpuUtilization: 70
  memoryUtilization: 80

networkPolicy:
  enabled: true

serviceMonitor:
  enabled: false

config:
  logLevel: info
  dbHost: postgres
  dbName: gophprofile
  minioEndpoint: minio:9000
  rabbitmqHost: rabbitmq

secrets:
  dbUser: gophprofile
  dbPassword: changeme
  minioAccessKey: minioadmin
  minioSecretKey: minioadmin
  rabbitmqUser: guest
  rabbitmqPassword: guest
```

### 5.4 Хук миграций

`helm/avatar-service/templates/hooks/migrate.yaml`:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ include "avatar-service.fullname" . }}-migrate
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-weight": "-5"
    "helm.sh/hook-delete-policy": before-hook-creation
spec:
  template:
    spec:
      restartPolicy: OnFailure
      containers:
      - name: migrate
        image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
        command: ["/migrate", "-path", "/migrations", "-database", "$(DATABASE_URL)", "up"]
        envFrom:
        - configMapRef:
            name: {{ include "avatar-service.fullname" . }}-config
        - secretRef:
            name: {{ include "avatar-service.fullname" . }}-secrets
```

### 5.5 Команды деплоя

```bash
# локальный кластер (Rancher Desktop)
helm install avatar-service ./helm/avatar-service -f helm/avatar-service/values-dev.yaml

# обновление
helm upgrade avatar-service ./helm/avatar-service -f helm/avatar-service/values-dev.yaml

# откат
helm rollback avatar-service 1

# просмотр релизов
helm list
```

---

## 6. Подготовка к продакшену

### 6.1 Graceful Shutdown

Убедиться, что сервер корректно завершает работу при получении SIGTERM: дожидается завершения активных запросов и закрывает соединения.

```go
// cmd/server/main.go
func run() error {
    srv := &http.Server{Addr: cfg.Port, Handler: router}

    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

    go func() {
        if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
            log.Fatal(err)
        }
    }()

    <-quit
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    if err := srv.Shutdown(ctx); err != nil {
        return fmt.Errorf("server forced shutdown: %w", err)
    }
    // закрыть DB pool, RabbitMQ соединение, flush OTel exporter
    return nil
}
```

> В Deployment нужно добавить `terminationGracePeriodSeconds: 40` — чуть больше таймаута shutdown, чтобы k8s дал процессу время завершиться корректно.

### 6.2 README.md

Обновить `README.md`:

- Краткое описание сервиса
- Предварительные требования (Go 1.21+, Docker, Rancher Desktop / kubectl / helm)
- Локальный запуск: `docker compose up`
- Деплой в K8s: установка через Helm
- Порты и эндпоинты: API, метрики, Grafana, Jaeger
- Схема архитектуры

### 6.3 Swagger / OpenAPI

Убедиться, что спецификация актуальна. Варианты:

- Аннотации `swaggo/swag` + `swag init` → `docs/swagger.json`
- Или вручную поддерживаемый `docs/openapi.yaml`

Добавить эндпоинт `/swagger/` (через `swaggo/http-swagger`) в dev-режиме.

### 6.4 Схема архитектуры

Добавить `docs/architecture.md` или PNG со схемой:

```
Ingress (nginx)
    └─ avatar-service (Deployment, HPA 2-10)
            ├─ PostgreSQL (StatefulSet / external)
            ├─ MinIO (StatefulSet / external)
            └─ RabbitMQ (StatefulSet / external)
                    └─ worker (Deployment)

Monitoring namespace:
    Prometheus ← ServiceMonitor ← avatar-service:9090/metrics
    Grafana ← Prometheus, Loki, Jaeger
```

---

## Критерии приёмки

- [ ] `kubectl apply -f k8s/base/` разворачивает сервис в кластере
- [ ] HPA настроен, поды масштабируются при нагрузке
- [ ] `/health` (liveness) и `/health/ready` (readiness) отвечают корректно
- [ ] ServiceMonitor создан, Prometheus собирает метрики из K8s
- [ ] NetworkPolicy ограничивает трафик до явно разрешённых соединений
- [ ] ServiceAccount с минимальными правами, контейнер запускается от non-root
- [ ] `helm install` разворачивает весь сервис одной командой
- [ ] `helm upgrade` не роняет БД (pre-upgrade миграция через Job)
- [ ] Graceful Shutdown корректно завершает запросы в полёте при SIGTERM
- [ ] `README.md` описывает локальный запуск и деплой в K8s
- [ ] Swagger/OpenAPI спецификация актуальна
- [ ] Схема архитектуры добавлена в документацию

---

## План реализации (этапы)

### Этап 1. Базовые манифесты K8s

- Создать директорию `k8s/base/`
- Написать: `deployment.yaml`, `service.yaml`, `ingress.yaml`, `configmap.yaml`, `secret.yaml`
- Запустить локально через Rancher Desktop: `kubectl apply -f k8s/base/`
- Убедиться, что поды поднимаются и `curl gophprofile.local/health` отвечает

### Этап 2. Масштабируемость

- Разделить `/health` на liveness и readiness в handler'е
- Добавить `k8s/base/hpa.yaml`
- Проверить через `kubectl describe hpa avatar-service-hpa`

### Этап 3. Мониторинг в K8s

- Установить `kube-prometheus-stack` через Helm (или подтвердить, что Prometheus Operator уже есть)
- Добавить `k8s/base/servicemonitor.yaml`
- Убедиться, что таргет появился в Prometheus UI

### Этап 4. Безопасность

- Добавить `k8s/base/networkpolicy.yaml`
- Добавить `k8s/base/rbac.yaml` (ServiceAccount, Role, RoleBinding)
- Добавить SecurityContext в Deployment
- Проверить: поды запущены от non-root, трафик вне политики заблокирован

### Этап 5. Helm Chart

- Создать структуру `helm/avatar-service/`
- Перенести все манифесты в шаблоны с `{{ .Values.* }}`
- Написать `values.yaml`, `values-dev.yaml`
- Добавить Job-хук для миграций
- Проверить: `helm install` → `helm upgrade` с rollback

### Этап 6. Graceful Shutdown и документация

- Убедиться / доработать Graceful Shutdown в `cmd/server` и `cmd/worker`
- Добавить `terminationGracePeriodSeconds: 40` в Deployment
- Обновить `README.md`
- Актуализировать/создать Swagger спецификацию
- Добавить схему архитектуры в `docs/`
