# Спринт 2 — Observability GophProfile

## Контекст

К концу первого спринта сервис `GophProfile` уже умеет загружать, хранить и отдавать аватарки: есть REST API, PostgreSQL для метаданных, MinIO для файлов и RabbitMQ + worker для асинхронной обработки миниатюр.

Этого достаточно, чтобы доказать работоспособность, но не достаточно для продакшена. Если сервис начнёт тормозить или отдавать 500-е, без инструментов наблюдаемости причину не найти.

## Цель спринта 2

Сделать сервис наблюдаемым: настроить метрики (Prometheus), распределённый трейсинг (Jaeger), централизованное логирование (Grafana Loki **или** OpenSearch/ELK — на выбор), корреляцию логов и трейсов, а также дашборды в Grafana. Бонус — алертинг через Alertmanager.

---

## 1. Инструментирование кода

Инструментировать приложение с помощью OpenTelemetry: распределённый трейсинг (HTTP, БД, S3, брокер), сбор технических и бизнес-метрик, структурированное логирование (`slog`) с корреляцией логов и трейсов.

### 1.1 Трейсинг

- Инструментирование входящих HTTP-запросов
- Трейсы для работы с БД (pgx)
- Трейсы для S3-операций (minio-go)
- Трейсы для брокера сообщений (RabbitMQ publisher + consumer)
- Context propagation между `server` и `worker` через заголовки в AMQP-сообщении

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/trace"
)

func (s *AvatarService) UploadAvatar(ctx context.Context, req *UploadRequest) error {
    ctx, span := otel.Tracer("avatar-service").Start(ctx, "upload_avatar")
    defer span.End()

    span.SetAttributes(
        attribute.String("user_id", req.UserID),
        attribute.String("file_name", req.FileName),
        attribute.Int64("file_size", req.Size),
    )
    // ...
}
```

### 1.2 Метрики

```go
var (
    uploadsTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "avatars_uploads_total",
            Help: "Total number of avatar uploads",
        },
        []string{"status", "user_id"},
    )

    uploadDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "avatars_upload_duration_seconds",
            Help: "Avatar upload duration",
        },
        []string{"status"},
    )

    storageUsage = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "avatars_storage_bytes",
            Help: "Total storage used by avatars",
        },
        []string{"user_id"},
    )
)
```

### 1.3 Логирование

- Структурированные логи в JSON (`slog.JSONHandler`)
- Корреляция с `trace_id`, `span_id`
- Уровни логирования через `slog.LevelVar`
- Единый логгер на весь сервис, пробрасывается через `context`

```go
import "log/slog"

logger := slog.With(
    "service", "avatar-service",
    "trace_id", trace.SpanFromContext(ctx).SpanContext().TraceID(),
)

logger.Info("uploading avatar",
    "user_id", userID,
    "file_size", fileSize,
    "mime_type", mimeType,
)
```

---

## 2. Инфраструктура мониторинга и логирования

Развернуть внешний стек observability в docker-compose:

### 2.1 Метрики — Prometheus

- HTTP-метрики: requests, duration, errors (RED)
- Бизнес-метрики: uploads, storage usage
- Инфраструктурные метрики: DB connections, queue depth
- Endpoint `/metrics` на `server` и `worker`

### 2.2 Трейсинг — Jaeger

- Distributed tracing между `server` и `worker`
- Performance-анализ
- Dependency mapping

### 2.3 Логирование — Grafana Loki **или** OpenSearch/ELK

- Централизованное логирование
- Алерты на ошибки (опционально)
- Log aggregation и поиск

> **Решение по проекту:** по умолчанию берём **Grafana Loki + Promtail** — легче по ресурсам и нативно дружит с Grafana. Если есть интерес именно к ELK-стеку, можно заменить на OpenSearch + Logstash + Filebeat.

---

## 3. Визуализация — Grafana

Создать информативные дашборды:

- **Service overview**: состояние server и worker
- **RED metrics**: request rate, error rate, duration (p50/p95/p99)
- **Resource utilization**: использование CPU, памяти, горутин
- **Business KPI**: количество загрузок, средний размер файла, доля completed/failed в processing_status
- Дашборды провижинятся конфигом, не руками через UI — чтобы поднималось одной командой `docker compose up`

---

## 4. Бонус — алертинг

Настроить правила для Prometheus Alertmanager. Примеры:

```yaml
groups:
- name: avatar-service
  rules:
  - alert: HighErrorRate
    expr: rate(avatars_uploads_total{status="error"}[5m]) / rate(avatars_uploads_total[5m]) > 0.1
    for: 5m
    labels:
      severity: warning

  - alert: HighResponseTime
    expr: histogram_quantile(0.95, avatars_upload_duration_seconds) > 5
    for: 2m
    labels:
      severity: critical
```

---

## Критерии приёмки

- [ ] HTTP-запросы, работа с БД, S3 и брокером покрыты трейсами OpenTelemetry
- [ ] Контекст трейсинга пробрасывается от `server` через RabbitMQ в `worker`
- [ ] На `/metrics` отдаются технические (HTTP RED) и бизнес-метрики
- [ ] Логи в JSON, в каждом логе есть `trace_id` и `span_id` (если есть активный span)
- [ ] `docker compose up` поднимает весь observability-стек
- [ ] В Grafana есть минимум два дашборда: Service overview (RED) и Business KPI
- [ ] README или этот документ описывает, как открыть Grafana, Prometheus и Jaeger локально
- [ ] Код проходит `go vet` и `golangci-lint`, тесты зелёные

---

## План реализации (этапы)

Спринт разбит на этапы.

### Этап 1. Observability infra в docker-compose

- Добавить в `docker-compose.yml` сервисы: `otel-collector`, `prometheus`, `jaeger`, `grafana`, `loki`, `promtail` (или ELK-аналог)
- Базовые конфиги в `observability/`:
  - `prometheus/prometheus.yml` — scrape-таргеты на `server:8080/metrics`, `worker:8081/metrics`
  - `otel-collector/config.yaml` — receivers OTLP, exporters в Jaeger + Prometheus
  - `loki/loki-config.yaml`, `promtail/promtail-config.yaml`
  - `grafana/provisioning/datasources/*.yml` — Prometheus, Jaeger, Loki как datasource'ы
- Убедиться, что всё поднимается и UI доступны: Grafana `:3000`, Prometheus `:9090`, Jaeger `:16686`

### Этап 2. Структурированное логирование

- Заменить стандартный `slog.TextHandler` на `slog.JSONHandler`
- Middleware `request_id`: генерит UUID, кладёт в context и header ответа
- Базовый логгер с полями `service`, `version` пробрасывается через ctx
- Promtail собирает stdout контейнеров, логи видны в Grafana Explore → Loki

### Этап 3. Метрики Prometheus

- Подключить `prometheus/client_golang`
- HTTP middleware для RED: `http_requests_total{method,route,status}`, `http_request_duration_seconds{method,route}`
- Бизнес-метрики: `avatars_uploads_total`, `avatars_upload_duration_seconds`, `avatars_storage_bytes`, `avatar_processing_status_total{status}`
- Инфра-метрики: `db_connections_in_use` через `pgxpool.Stat()`, `rabbit_consumer_prefetch`
- Endpoint `/metrics` на server и worker, `pprof` — опционально

### Этап 4. Трейсинг через OpenTelemetry

- Инициализация OTel SDK: TracerProvider → OTLP exporter → Collector
- Middleware `otelhttp` на входящие запросы
- Инструментирование pgx через `otelpgx`
- Обёртки над minio-go методами с ручными spans (нет готового otel-пакета — делаем сами)
- RabbitMQ: в publisher добавляем headers с `traceparent`, в consumer извлекаем и продолжаем trace
- Коррелируем логи: в JSON handler добавить hook, который читает `trace_id`/`span_id` из ctx и кладёт их в запись

### Этап 5. Дашборды Grafana

- Provisioning JSON-дашбордов через `grafana/provisioning/dashboards/`
- Dashboard `service-overview.json`: RED по HTTP, uptime, версии
- Dashboard `business-kpi.json`: uploads/min, processing status breakdown, средний размер, storage usage
- Сделать скриншоты и добавить в `docs/sprint-02-screenshots/` (опционально, для защиты)

### Этап 6. Бонус — Alertmanager

- Добавить `alertmanager` в compose
- Правила в `observability/prometheus/alerts.yml`: HighErrorRate, HighResponseTime, WorkerQueueBacklog
- Ресивер — stdout или webhook в httpbin для демонстрации

---

## Что отдельно обсудить до старта

- **Log stack:** Loki или ELK? (рекомендация — Loki по ресурсам)
- **OTel collector vs direct export:** прокидывать трейсы через collector или сразу в Jaeger OTLP? (рекомендация — через collector, это ближе к продакшену)
- **Version bump go.mod:** нужен ли? (OTel-пакеты современные, но с Go 1.25 проблем быть не должно)
- **CI:** на этапе 1 стек в docker-compose поднимать в CI не обязательно — достаточно валидировать yaml'ы. Интеграционные тесты на observability не пишем.
