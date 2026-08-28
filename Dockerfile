FROM golang:1.26.5-alpine3.23 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/log-archive ./cmd/log-archive

FROM alpine:3.23
RUN apk add --no-cache ca-certificates
COPY --from=build /out/log-archive /log-archive
EXPOSE 7007 8080
ENTRYPOINT ["/log-archive"]
