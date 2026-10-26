# ---- Build stage ----
FROM golang:1.26.10-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Release builds name themselves; logs and metrics carry these values.
ARG VERSION=dev
ARG COMMIT=
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X github.com/dappnode/dappnode-nexus-gateway/pkg/buildinfo.Version=${VERSION} -X github.com/dappnode/dappnode-nexus-gateway/pkg/buildinfo.Commit=${COMMIT}" \
    -o /gateway ./apps/gateway

# ---- Runtime stage ----
FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /gateway .

EXPOSE 8080 9090

ENTRYPOINT ["/app/gateway"]
