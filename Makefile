# GophProfile — команды разработки.  `make` без аргументов покажет список.

MODULE      := go-avatar-service
COMPOSE     := docker compose -f docker/docker-compose.yml
GOOSE       := go run github.com/pressly/goose/v3/cmd/goose@latest
MIGRATIONS  := ./migrations
DB_DSN      ?= postgres://avatars:avatars@localhost:5432/avatars?sslmode=disable

.DEFAULT_GOAL := help
.PHONY: help run-server run-worker build up up-all down down-v logs ps image lint lint-fix fmt tidy test test-short cover cover-html migrate-up migrate-down migrate-status migrate-new check

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

down: ## Остановить окружение (данные сохраняются)
	$(COMPOSE) --profile app down

down-v: ## Остановить окружение и удалить тома с данными
	$(COMPOSE) --profile app down -v

logs: ## Логи окружения (Ctrl+C для выхода)
	$(COMPOSE) --profile app logs -f

ps: ## Статус контейнеров
	$(COMPOSE) --profile app ps

image: ## Собрать образ приложения
	docker build -f docker/Dockerfile -t gophprofile:dev .

## --- Качество ---

lint: ## golangci-lint
	golangci-lint run ./...

lint-fix: ## golangci-lint с автоисправлением
	golangci-lint run --fix ./...

fmt: ## Форматирование (gofmt + goimports через golangci-lint)
	golangci-lint fmt ./...

tidy: ## go mod tidy
	go mod tidy

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
