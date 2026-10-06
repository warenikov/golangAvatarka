# GophProfile — команды разработки.  `make` без аргументов покажет список.

MODULE      := go-avatar-service
COMPOSE     := docker compose -f docker/docker-compose.yml
GOOSE       := go run github.com/pressly/goose/v3/cmd/goose@latest
MOCKERY     := go run github.com/vektra/mockery/v3@v3.7.0
GOSEC       := go run github.com/securego/gosec/v2/cmd/gosec@latest
GOVULN      := go run golang.org/x/vuln/cmd/govulncheck@latest
MIGRATIONS  := ./migrations
CHART       := deploy/helm/gophprofile
K8S_NS      ?= gophprofile
K8S_RELEASE ?= gp
K8S_VALUES  ?= $(CHART)/values-dev.yaml
DB_DSN      ?= postgres://avatars:avatars@localhost:5432/avatars?sslmode=disable

.DEFAULT_GOAL := help
.PHONY: help run-server run-worker build up up-all down down-v logs ps image lint lint-fix fmt tidy mocks sec test up-obs logs-obs test-short cover cover-html migrate-up migrate-down migrate-status migrate-new check k8s-lint k8s-render k8s-deploy k8s-status k8s-logs k8s-delete

help: ## Показать список команд
	grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## --- Разработка ---

run-server: ## Запустить HTTP-сервер локально
	go run ./cmd/server

run-worker: ## Запустить воркер локально
	go run ./cmd/worker

build: ## Собрать оба бинаря в ./bin
	mkdir -p bin
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/server ./cmd/server
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/worker ./cmd/worker

## --- Окружение ---

up: ## Поднять инфраструктуру: postgres, minio, rabbitmq
	$(COMPOSE) up -d

up-all: ## Поднять всё, включая server и worker в контейнерах
	$(COMPOSE) --profile app up -d --build

up-obs: ## Поднять всё вместе с наблюдаемостью: Jaeger, Prometheus, Grafana, алерты
	TRACING_ENABLED=true $(COMPOSE) --profile app --profile obs up -d --build

logs-obs: ## Логи стека наблюдаемости
	$(COMPOSE) --profile obs logs -f

down: ## Остановить окружение (данные сохраняются)
	# --remove-orphans обязателен: контейнеры, созданные до правки compose-файла,
	# перестают совпадать с текущим описанием, и обычный down молча их пропускает,
	# выходя с кодом 0 — стек наблюдаемости остаётся висеть в памяти.
	$(COMPOSE) --profile app --profile obs down --remove-orphans

down-v: ## Остановить окружение и удалить тома с данными
	$(COMPOSE) --profile app --profile obs down -v --remove-orphans

logs: ## Логи окружения (Ctrl+C для выхода)
	$(COMPOSE) --profile app logs -f

ps: ## Статус контейнеров
	$(COMPOSE) --profile app ps

image: ## Собрать образ приложения
	# --pull обязателен: без него docker берёт закешированный базовый образ,
	# и сборка молча уезжает на непропатченной версии Go. Проверено —
	# на залежавшемся golang:1.25-alpine govulncheck находил 11 уязвимостей
	# стандартной библиотеки, на свежем не находит ни одной.
	docker build --pull -f docker/Dockerfile -t gophprofile:dev .

## --- Качество ---

sec: ## Безопасность: gosec + govulncheck
	$(GOSEC) -quiet ./...
	$(GOVULN) ./...

lint: ## golangci-lint
	golangci-lint run ./...

lint-fix: ## golangci-lint с автоисправлением
	golangci-lint run --fix ./...

fmt: ## Форматирование (gofmt + goimports через golangci-lint)
	golangci-lint fmt ./...

tidy: ## go mod tidy
	go mod tidy

mocks: ## Перегенерировать моки по .mockery.yml
	$(MOCKERY)

test: ## Тесты с детектором гонок
	go test ./... -race -count=1

test-short: ## Быстрые тесты, без testcontainers
	go test ./... -short -count=1

cover: ## Покрытие + итоговый процент (цель спринта: >50%)
	go test ./... -coverprofile=cover.out -covermode=atomic
	go tool cover -func=cover.out | tail -1

cover-html: cover ## Покрытие в браузере
	go tool cover -html=cover.out -o coverage.html
	open coverage.html

check: lint test ## Всё перед коммитом: линт + тесты

## --- Kubernetes ---

k8s-lint: ## Проверить чарт на всех наборах values
	helm lint $(CHART) -f $(CHART)/values-dev.yaml
	helm lint $(CHART) -f $(CHART)/values-prod.yaml --set secrets.existingSecret=gophprofile-secrets
	helm template $(K8S_RELEASE) $(CHART) -f $(CHART)/values-dev.yaml -n $(K8S_NS) >/dev/null
	helm template $(K8S_RELEASE) $(CHART) -f $(CHART)/values-prod.yaml -n $(K8S_NS) \
		--set secrets.existingSecret=gophprofile-secrets >/dev/null

k8s-render: ## Развернуть чарт в плоские манифесты deploy/k8s/rendered
	# Источник истины один — чарт. Плоские манифесты генерируются из него
	# и коммитятся, чтобы их можно было читать и применять без Helm.
	# Дашборд копируется сюда же: Helm читает файлы только внутри каталога
	# чарта, и без копии он разъехался бы с версией из docker/observability.
	cp docker/observability/grafana/dashboards/overview.json $(CHART)/dashboards/overview.json
	rm -rf deploy/k8s/rendered
	mkdir -p deploy/k8s/rendered
	helm template $(K8S_RELEASE) $(CHART) -f $(CHART)/values-dev.yaml \
		-n $(K8S_NS) --output-dir deploy/k8s/rendered
	mv deploy/k8s/rendered/gophprofile/templates/* deploy/k8s/rendered/
	rm -rf deploy/k8s/rendered/gophprofile

k8s-deploy: ## Поставить релиз в кластер (K8S_VALUES задаёт набор значений)
	kubectl apply -f deploy/k8s/namespace.yaml
	helm upgrade --install $(K8S_RELEASE) $(CHART) -f $(K8S_VALUES) \
		--namespace $(K8S_NS) --timeout 8m

k8s-status: ## Состояние релиза
	kubectl -n $(K8S_NS) get pods,hpa,ingress,svc

k8s-logs: ## Логи сервера и воркера
	kubectl -n $(K8S_NS) logs -l app.kubernetes.io/part-of=gophprofile --all-containers -f --tail=50

k8s-delete: ## Удалить релиз (неймспейс и тома остаются)
	helm uninstall $(K8S_RELEASE) --namespace $(K8S_NS)

## --- Миграции ---

migrate-up: ## Накатить миграции
	$(GOOSE) -dir $(MIGRATIONS) postgres "$(DB_DSN)" up

migrate-down: ## Откатить последнюю миграцию
	$(GOOSE) -dir $(MIGRATIONS) postgres "$(DB_DSN)" down

migrate-status: ## Статус миграций
	$(GOOSE) -dir $(MIGRATIONS) postgres "$(DB_DSN)" status

migrate-new: ## Новая миграция: make migrate-new name=add_something
	test -n "$(name)" || (echo "Укажите имя: make migrate-new name=add_something" && exit 1)
	$(GOOSE) -dir $(MIGRATIONS) create $(name) sql
