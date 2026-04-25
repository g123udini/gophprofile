.DEFAULT_GOAL := help

COMPOSE    ?= docker compose
SERVER     := gophprofile-server-1
WORKER     := gophprofile-worker-1
POSTGRES   := gophprofile-postgres-1
MINIO      := gophprofile-minio-1
RABBIT     := gophprofile-rabbitmq-1

PG_USER    := gophprofile
PG_DB      := gophprofile

# ---------- help ----------

.PHONY: help
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_.-]+:.*?## / {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ---------- compose lifecycle ----------

.PHONY: up
up: ## Start the full stack (postgres, minio, rabbit, server, worker)
	$(COMPOSE) up -d

.PHONY: up-infra
up-infra: ## Start only the infrastructure (postgres, minio, rabbit)
	$(COMPOSE) up -d postgres minio rabbitmq

.PHONY: up-obs
up-obs: ## Start only the observability stack (prometheus, jaeger, otel, loki, promtail, grafana)
	$(COMPOSE) up -d prometheus jaeger otel-collector loki promtail grafana

.PHONY: down
down: ## Stop and remove containers (volumes kept)
	$(COMPOSE) down

.PHONY: down-volumes
down-volumes: ## Stop and remove containers + volumes (wipes data)
	$(COMPOSE) down -v

.PHONY: restart
restart: ## Restart server and worker
	$(COMPOSE) restart server worker

.PHONY: rebuild
rebuild: ## Rebuild and restart server + worker images
	$(COMPOSE) build server worker
	$(COMPOSE) up -d server worker

.PHONY: ps
ps: ## Show container status
	$(COMPOSE) ps

# ---------- logs ----------

.PHONY: logs
logs: ## Tail logs of all services
	$(COMPOSE) logs -f --tail=100

.PHONY: logs-server
logs-server: ## Tail server logs
	$(COMPOSE) logs -f --tail=100 server

.PHONY: logs-worker
logs-worker: ## Tail worker logs
	$(COMPOSE) logs -f --tail=100 worker

.PHONY: logs-rabbit
logs-rabbit: ## Tail rabbitmq logs
	$(COMPOSE) logs -f --tail=100 rabbitmq

# ---------- shells ----------

.PHONY: sh-server
sh-server: ## Shell into the server container
	docker exec -it $(SERVER) sh

.PHONY: sh-worker
sh-worker: ## Shell into the worker container
	docker exec -it $(WORKER) sh

.PHONY: sh-postgres
sh-postgres: ## Shell into the postgres container
	docker exec -it $(POSTGRES) sh

.PHONY: sh-minio
sh-minio: ## Shell into the minio container
	docker exec -it $(MINIO) sh

.PHONY: sh-rabbit
sh-rabbit: ## Shell into the rabbitmq container
	docker exec -it $(RABBIT) sh

# ---------- postgres ----------

.PHONY: db
db: ## Open psql inside the postgres container
	docker exec -it $(POSTGRES) psql -U $(PG_USER) -d $(PG_DB)

.PHONY: psql
psql: db ## Alias for db

.PHONY: db-reset
db-reset: ## Drop all data from the avatars table
	docker exec -it $(POSTGRES) psql -U $(PG_USER) -d $(PG_DB) -c "TRUNCATE avatars"

.PHONY: db-dump-avatars
db-dump-avatars: ## Show all rows in the avatars table
	docker exec -it $(POSTGRES) psql -U $(PG_USER) -d $(PG_DB) -c "SELECT id, user_id, processing_status, upload_status, s3_key, created_at FROM avatars ORDER BY created_at DESC"

# ---------- minio ----------

.PHONY: mc-alias
mc-alias: ## Register local alias inside the minio container
	docker exec $(MINIO) mc alias set local http://localhost:9000 minioadmin minioadmin

.PHONY: mc-ls
mc-ls: mc-alias ## List all objects in the avatars bucket
	docker exec $(MINIO) mc ls --recursive local/avatars/

.PHONY: mc-wipe
mc-wipe: mc-alias ## Delete every object in the avatars bucket
	docker exec $(MINIO) mc rm --recursive --force local/avatars/ || true

.PHONY: minio-ui
minio-ui: ## Print the MinIO console URL and credentials
	@echo "MinIO console: http://localhost:9001"
	@echo "login: minioadmin"
	@echo "password: minioadmin"

# ---------- rabbitmq ----------

.PHONY: rabbit-ui
rabbit-ui: ## Print the RabbitMQ management URL and credentials
	@echo "RabbitMQ mgmt: http://localhost:15673"
	@echo "login: guest"
	@echo "password: guest"

.PHONY: rabbit-queues
rabbit-queues: ## List rabbitmq queues with message counts
	docker exec $(RABBIT) rabbitmqctl list_queues name messages consumers

# ---------- observability ----------

.PHONY: obs-ui
obs-ui: ## Print observability UI URLs
	@echo "Grafana:     http://localhost:3000  (admin/admin, anonymous viewer enabled)"
	@echo "Prometheus:  http://localhost:9090"
	@echo "Jaeger:      http://localhost:16686"
	@echo "Loki API:    http://localhost:3100"

# ---------- go ----------

.PHONY: build
build: ## go build ./...
	go build ./...

.PHONY: test
test: ## Run the full test suite
	go test ./...

.PHONY: test-v
test-v: ## Run tests with verbose output
	go test -v ./...

.PHONY: test-cover
test-cover: ## Run tests with coverage report per package
	go test -cover ./...

.PHONY: test-cover-html
test-cover-html: ## Generate an HTML coverage report and open it
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

.PHONY: fmt
fmt: ## go fmt
	go fmt ./...

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy

.PHONY: run-server
run-server: ## Run server locally (requires up-infra)
	HTTP_PORT=8082 go run ./cmd/server

.PHONY: run-worker
run-worker: ## Run worker locally (requires up-infra)
	go run ./cmd/worker

# ---------- smoke ----------

.PHONY: health
health: ## Hit /health on the compose server
	@curl -s http://localhost:8080/health | python3 -m json.tool || true
