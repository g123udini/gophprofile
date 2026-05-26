# Архитектура GophProfile

## Компоненты

### Приложение

| Компонент | Описание |
|---|---|
| `cmd/server` | HTTP API: загрузка, отдача, метаданные аватарок |
| `cmd/worker` | Consumer: создаёт миниатюры из событий RabbitMQ |

### Инфраструктура

| Компонент | Назначение |
|---|---|
| PostgreSQL | Метаданные аватарок (UUID, user_id, s3_key, статус) |
| MinIO | Файлы: оригиналы + миниатюры (S3-совместимый) |
| RabbitMQ | Брокер событий: `avatar.uploaded` → worker |

### Observability

| Компонент | Назначение |
|---|---|
| Prometheus | Сбор метрик с `/metrics` |
| Grafana | Дашборды: RED-метрики, бизнес-KPI |
| Loki + Promtail | Централизованное логирование |
| Jaeger | Распределённый трейсинг (OTel) |
| OTel Collector | Агрегация трейсов, проксирование в Jaeger |

## Поток загрузки аватарки

```
Client
  │
  ▼
POST /api/v1/avatars  (multipart, X-User-ID)
  │
  ▼
Handler
  ├── валидация формата / размера (≤ 10 MB)
  ├── сохранение оригинала в MinIO
  ├── запись метаданных в PostgreSQL (status = processing)
  └── публикация AvatarUploadedEvent в RabbitMQ
          │
          ▼
        Worker
          ├── скачивает оригинал из MinIO
          ├── создаёт миниатюры 100×100, 300×300
          ├── сохраняет миниатюры в MinIO
          └── обновляет статус в PostgreSQL (status = done)
```

## Kubernetes-топология

```
┌─────────────────────────────────────────────────────────────────┐
│  Namespace: default                                             │
│                                                                 │
│  Ingress (nginx)                                                │
│    └─► Service: avatar-service (http:80, metrics:9090)         │
│           └─► Deployment: avatar-service                        │
│                  ├── replicas: 2–10 (HPA по CPU/RAM)           │
│                  ├── liveness:  GET /health                     │
│                  ├── readiness: GET /health/ready               │
│                  ├── securityContext: non-root, readOnly FS     │
│                  └── ServiceAccount: avatar-service (min RBAC) │
│                                                                 │
│  StatefulSets / external:                                       │
│    postgres, minio, rabbitmq, worker                            │
│                                                                 │
│  NetworkPolicy: avatar-service-netpol                          │
│    ingress: только ingress-nginx (8080) + monitoring (9090)    │
│    egress:  только postgres/minio/rabbitmq + DNS               │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  Namespace: monitoring                                          │
│                                                                 │
│  Prometheus ◄── ServiceMonitor (scrape /metrics каждые 30s)   │
│  Grafana    ◄── Prometheus, Loki, Jaeger datasources           │
└─────────────────────────────────────────────────────────────────┘
```

## Helm Chart

```
helm/avatar-service/
├── Chart.yaml
├── values.yaml          — дефолт (prod)
├── values-dev.yaml      — dev (1 реплика, без HPA/NetworkPolicy)
└── templates/
    ├── deployment.yaml
    ├── service.yaml
    ├── ingress.yaml       — условный
    ├── configmap.yaml
    ├── secret.yaml
    ├── hpa.yaml           — условный
    ├── networkpolicy.yaml — условный
    ├── rbac.yaml
    ├── servicemonitor.yaml — условный
    └── hooks/migrate.yaml  — pre-install/pre-upgrade Job
```
