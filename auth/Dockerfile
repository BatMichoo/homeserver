# --- Build ---
FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/auth ./cmd/auth

# --- Run ---
FROM alpine:3.20
COPY --from=builder /out/auth /usr/local/bin/auth
# Templates, translations, theme files and migrations are built into the
# binary, so nothing else is copied.
RUN addgroup -S app && adduser -S -G app -H app
USER app
EXPOSE 8080
CMD ["auth"]
