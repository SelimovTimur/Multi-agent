# Сборка Go: сервер, миграции и демо-данные.
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /server ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /migrate ./cmd/migrate \
 && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /seed ./cmd/seed

FROM alpine:3.22
# ffmpeg приводит запись ученика к WAV 16 кГц перед распознаванием.
RUN apk add --no-cache ffmpeg ca-certificates tzdata
WORKDIR /app
COPY --from=builder /server /migrate /seed /app/
RUN mkdir -p /app/tmp
EXPOSE 8080
ENTRYPOINT ["/app/server"]
