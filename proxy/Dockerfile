# syntax=docker/dockerfile:1
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 go build -o /out/codeterminal-proxy .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/codeterminal-proxy /usr/local/bin/codeterminal-proxy
EXPOSE 8080
ENTRYPOINT ["codeterminal-proxy"]
