# GophProfile

Микросервис для хранения и раздачи аватарок пользователей. Пользователь загружает фото один раз — любая платформа получает его по `user_id`. Если пользователь не найден, возвращается заглушка.

## Стек

- **Go 1.21+** · Chi · pgx · minio-go · amqp091-go
- **PostgreSQL** — метаданные аватарок
- **MinIO** — файлы (оригиналы + миниатюры)
- **RabbitMQ** — асинхронная обработка миниатюр
- **Prometheus + Grafana + Loki + Jaeger** — observability
- **Kubernetes + Helm** — деплой

## Предварительные требования

| Инструмент | Версия |
|---|---|
| Go | 1.21+ |
| Docker + Docker Compose | 24+ |
| kubectl | 1.28+ |
| Helm | 3.14+ |
| Rancher Desktop / kind / minikube | любая |

## Локальный запуск (Docker Compose)

```bash
# поднять инфру + сервис
docker compose up -d

# применить миграции
docker compose exec server /migrate -path /migrations -database "$POSTGRES_DSN" up

# остановить
docker compose down
```

| Сервис | URL |
|---|---|
| API | http://localhost:8080 |
| Swagger UI | http://localhost:8080/swagger/ |
| Метрики (server) | http://localhost:8080/metrics |
| Grafana | http://localhost:3000 (admin / admin) |
| Prometheus | http://localhost:9090 |
| Jaeger | http://localhost:16686 |
| MinIO Console | http://localhost:9001 |
| RabbitMQ Management | http://localhost:15672 |

## API

```
POST   /api/v1/avatars                   загрузка (multipart, X-User-ID header)
GET    /api/v1/avatars/{id}              получить файл
GET    /api/v1/avatars/{id}/metadata     метаданные
DELETE /api/v1/avatars/{id}              удалить (X-User-ID header)
GET    /api/v1/users/{user_id}/avatar    последняя аватарка пользователя
GET    /api/v1/users/{user_id}/avatars   список аватарок

GET    /health                           liveness
GET    /health/ready                     readiness (проверяет DB + S3 + RabbitMQ)
GET    /metrics                          Prometheus scrape
```

Полная спецификация: [`docs/openapi.yaml`](docs/openapi.yaml)

## Деплой в Kubernetes

### Предварительно

```bash
# добавить gophprofile.local в /etc/hosts
echo "127.0.0.1 gophprofile.local" | sudo tee -a /etc/hosts

# убедиться, что kubectl смотрит на нужный кластер
kubectl config current-context
```

### Секреты

`values.yaml` и `k8s/base/secret.yaml` содержат плейсхолдеры `PLACEHOLDER` вместо реальных паролей — **не заменяй их в репозитории**.

Передать настоящие значения можно тремя способами:

**1. `--set` при установке Helm:**
```bash
helm install avatar-service ./helm/avatar-service \
  --set secrets.postgresDSN="postgres://gophprofile:MYPASSWORD@postgres:5432/gophprofile?sslmode=disable" \
  --set secrets.s3AccessKey=MYKEY \
  --set secrets.s3SecretKey=MYSECRET \
  --set secrets.rabbitURL="amqp://myuser:MYPASSWORD@rabbitmq:5672/"
```

**2. Отдельный `values-secrets.yaml` (не коммитить, добавить в `.gitignore`):**
```bash
helm install avatar-service ./helm/avatar-service \
  -f helm/avatar-service/values-dev.yaml \
  -f values-secrets.yaml   # только локально, вне репозитория
```

**3. Внешний secrets-менеджер** (рекомендуется для прода):
- [External Secrets Operator](https://external-secrets.io) + AWS Secrets Manager / GCP Secret Manager / HashiCorp Vault
- [Sealed Secrets](https://sealed-secrets.netlify.app) — шифрование прямо в GitOps

Для raw-манифестов (`k8s/base/`) отредактируй `secret.yaml` локально перед `kubectl apply`, но не коммить изменённый файл.

### Установка через Helm

```bash
# dev-окружение (1 реплика, HPA/NetworkPolicy выключены)
helm install avatar-service ./helm/avatar-service \
  -f helm/avatar-service/values-dev.yaml \
  --set secrets.postgresDSN="postgres://gophprofile:MYPASSWORD@postgres:5432/gophprofile?sslmode=disable" \
  --set secrets.s3AccessKey=MYKEY \
  --set secrets.s3SecretKey=MYSECRET \
  --set secrets.rabbitURL="amqp://myuser:MYPASSWORD@rabbitmq:5672/"

# prod-окружение
helm install avatar-service ./helm/avatar-service \
  --set secrets.postgresDSN="..." \
  --set secrets.s3AccessKey=... \
  --set secrets.s3SecretKey=... \
  --set secrets.rabbitURL="..."

# обновление
helm upgrade avatar-service ./helm/avatar-service \
  -f helm/avatar-service/values-dev.yaml

# откат
helm rollback avatar-service 1

# удаление
helm uninstall avatar-service
```

Хук `pre-install/pre-upgrade` автоматически запускает миграции перед деплоем.

### Без Helm (raw manifests)

```bash
kubectl apply -f k8s/base/
kubectl rollout status deployment/avatar-service
```

### Проверка

```bash
kubectl get pods
kubectl get hpa
curl http://gophprofile.local/health/ready
```

## Архитектура

```
                        ┌─────────────────────────────────────────┐
                        │              Kubernetes Cluster          │
                        │                                          │
Internet ──► Ingress ──►│  avatar-service (Deployment, HPA 2-10)  │
            (nginx)     │     │                                    │
                        │     ├──► PostgreSQL (метаданные)         │
                        │     ├──► MinIO (файлы)                   │
                        │     └──► RabbitMQ ──► worker             │
                        │                                          │
                        │  monitoring namespace:                   │
                        │     Prometheus ◄── ServiceMonitor        │
                        │     Grafana   ◄── Prometheus, Loki, Jaeger│
                        └─────────────────────────────────────────┘
```

Подробная схема: [`docs/architecture.md`](docs/architecture.md)

## Мониторинг и алерты

### Дашборды Grafana

| Дашборд | Описание |
|---|---|
| Service Overview | RED-метрики (requests/errors/duration), uptime |
| Business KPI | uploads/min, processing status, размер файлов, storage usage |

### Alertmanager

Правила алертов: [`observability/prometheus/alerts.yml`](observability/prometheus/alerts.yml)

| Алерт | Условие | Severity |
|---|---|---|
| `ServiceDown` | Prometheus не может скрейпить таргет > 1 мин | critical |
| `HighErrorRate` | 5xx > 5% за 5 мин | warning |
| `HighLatency` | p95 > 1s за 5 мин | warning |
| `WorkerQueueBacklog` | worker не обрабатывает сообщения > 10 мин | warning |

Alertmanager конфиг: [`observability/alertmanager/config.yml`](observability/alertmanager/config.yml)

## Разработка

```bash
go build ./...
go test ./...
golangci-lint run
```

## Переменные окружения

| Переменная | Дефолт | Описание |
|---|---|---|
| `HTTP_PORT` | `8080` | порт API |
| `HTTP_SHUTDOWN_TIMEOUT` | `30s` | таймаут graceful shutdown |
| `POSTGRES_DSN` | — | строка подключения к PostgreSQL |
| `S3_ENDPOINT` | `localhost:9000` | адрес MinIO/S3 |
| `S3_ACCESS_KEY` | — | access key S3 |
| `S3_SECRET_KEY` | — | secret key S3 |
| `S3_BUCKET` | `avatars` | имя бакета |
| `RABBIT_URL` | — | AMQP URL RabbitMQ |
| `RABBIT_EXCHANGE` | `avatars.exchange` | имя exchange |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | — | адрес OTel Collector (пусто = трейсинг отключён) |
