FROM node:22-alpine AS web

WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build

WORKDIR /app
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/notion-manager ./cmd/notion-manager

FROM alpine:3.21

WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata && mkdir -p /app/accounts
COPY --from=build /out/notion-manager /app/notion-manager

ENV ACCOUNTS_DIR=/app/accounts

CMD ["/app/notion-manager"]
