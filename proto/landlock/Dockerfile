FROM golang:1.24 AS build

WORKDIR /src
COPY proto/landlock/go.mod proto/landlock/go.sum* ./
RUN go mod download 2>/dev/null || true
COPY proto/landlock/main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /proto .

FROM ubuntu:24.04
RUN useradd -m -s /bin/bash testuser
COPY --from=build /proto /proto
USER testuser
CMD ["/proto"]
