FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/synced_app .

FROM alpine:3.21

WORKDIR /app

COPY --from=builder /out/synced_app /app/synced_app

EXPOSE 5000

ENTRYPOINT ["/app/synced_app"]