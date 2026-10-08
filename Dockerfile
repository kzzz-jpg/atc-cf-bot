FROM golang:1.26.0-alpine3.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/atc-cf-bot ./cmd/atc-cf-bot

FROM alpine:3.23.0
RUN apk add --no-cache ca-certificates tzdata && addgroup -S bot && adduser -S -G bot bot
COPY --from=build /out/atc-cf-bot /usr/local/bin/atc-cf-bot
USER bot
ENTRYPOINT ["atc-cf-bot"]
CMD ["run"]
