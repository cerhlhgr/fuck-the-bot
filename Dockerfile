FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 go build -trimpath -o /bot ./cmd/bot

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -H bot
COPY --from=build /bot /usr/local/bin/bot
USER bot
ENTRYPOINT ["/usr/local/bin/bot"]
