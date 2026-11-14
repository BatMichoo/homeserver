# --- Build ---
FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/deploy ./cmd/deploy

# --- Run: the docker CLI (with compose) and git ---
FROM docker:28-cli
RUN apk add --no-cache git
COPY --from=builder /out/deploy /usr/local/bin/deploy
ENV HOME=/tmp
ENTRYPOINT ["deploy"]
CMD ["run"]
