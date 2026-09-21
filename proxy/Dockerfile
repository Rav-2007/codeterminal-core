# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /out/mochiii-proxy .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/mochiii-proxy /usr/local/bin/mochiii-proxy

# Run as a non-root user. The proxy needs no privileged capability whatsoever: it
# binds PORT (8080 by default, well above 1024), reads its config from the
# environment, and writes nothing to disk. Running as root was simply the default
# nobody had changed -- this bounds what a remote-code-execution bug in the one
# internet-facing component could reach.
RUN adduser -D -H -u 10001 proxy
USER 10001:10001

EXPOSE 8080
ENTRYPOINT ["mochiii-proxy"]
