FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /gateway cmd/gateway/main.go

FROM alpine:3.20

RUN addgroup -S appgroup && adduser -S appuser -G appgroup
COPY --from=builder /gateway /gateway
USER appuser

EXPOSE 8080
CMD ["/gateway"]
