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

### Установка через Helm

```bash
# dev-окружение (1 реплика, HPA/NetworkPolicy выключены)
helm install avatar-service ./helm/avatar-service \
  -f helm/avatar-service/values-dev.yaml

# prod-окружение
helm install avatar-service ./helm/avatar-service

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
| `POSTGRES_DSN` | см. config | строка подключения к БД |
| `MINIO_ENDPOINT` | `localhost:9000` | адрес MinIO |
| `RABBITMQ_DSN` | `amqp://guest:guest@localhost:5672/` | адрес RabbitMQ |
| `LOG_LEVEL` | `info` | уровень логирования |
| `OTEL_ENDPOINT` | `localhost:4317` | адрес OTel Collector |
