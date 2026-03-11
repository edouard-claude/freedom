# -- Build stage --
FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /freedom ./cmd/freedom

# -- Runtime stage --
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /freedom /usr/local/bin/freedom
COPY creole_lexicon.jsonl /data/creole_lexicon.jsonl
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
