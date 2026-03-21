# Build web UI
FROM node:22-alpine AS web
WORKDIR /app/web/ui
COPY web/ui/package*.json ./
RUN npm ci
COPY web/ui/ ./
RUN npm run build

# Build Go binary
FROM golang:1.23-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/web/ui/dist ./web/ui/dist
RUN CGO_ENABLED=0 go build -o /hive ./cmd/hive

# Runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=build /hive /usr/local/bin/hive
ENTRYPOINT ["hive"]
