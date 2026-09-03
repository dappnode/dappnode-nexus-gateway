# ---- Build stage ----
FROM golang:1.26.4-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /gateway ./apps/gateway

# ---- Runtime stage ----
FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /gateway .

EXPOSE 8080 9090

ENTRYPOINT ["/app/gateway"]
