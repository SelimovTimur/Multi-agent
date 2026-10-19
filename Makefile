# Makefile — короткие команды для разработки и проверки.

# Локальная база для разработки. В Docker адрес приходит из compose.
DATABASE_URL ?= postgres://postgres@127.0.0.1:5432/native?sslmode=disable
export DATABASE_URL

.DEFAULT_GOAL := help
.PHONY: help build run seed test fmt vet check check-all migrate-up migrate-down migrate-status lint-boundaries tidy settings settings-check up down

help: ## Показать список команд
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Собрать все бинарники
	go build ./...

run: ## Запустить API локально
	go run ./cmd/server

seed: ## Завести демо-данные
	go run ./cmd/seed

test: ## Прогнать тесты Go
	go test ./...

fmt: ## Отформатировать код Go
	gofmt -w .

vet: ## Статический анализ go vet
	go vet ./...

lint-boundaries: ## Проверить границы модулей и инварианты схемы
	go test ./internal/arch/ -v

settings: ## Синхронизировать общие настройки после правки settings.py
	python3 settings.py --sync

settings-check: ## Проверить согласованность настроек всех сервисов
	python3 settings.py --check
	python3 -m unittest discover -s tests -p 'test_settings.py'

check: settings-check fmt vet lint-boundaries test ## Проверка перед коммитом

check-all: check ## Плюс фронтенд и Python-сервисы
	cd frontend && npm run build && npm test
	cd llm_service && python3 -m pytest -q
	cd asr_service && python3 -m pytest -q tests
	cd tts_service && python3 -m pytest -q

up: ## Поднять всё в Docker
	docker compose up --build -d

down: ## Остановить Docker-стек
	docker compose down

tidy: ## Привести go.mod в порядок
	go mod tidy

migrate-up: ## Применить миграции
	go run ./cmd/migrate up

migrate-down: ## Откатить N последних миграций: make migrate-down N=1
	go run ./cmd/migrate down $(or $(N),1)

migrate-status: ## Показать применённые миграции
	go run ./cmd/migrate status
