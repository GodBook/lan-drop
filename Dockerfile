# LAN Drop container image — single static binary, config-free runtime.
#   docker run -d --name landrop -p 8087:8087 -v landrop-data:/data ghcr.io/godbook/lan-drop
FROM golang:1.22-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.AppVersion=${VERSION}" -o /out/landrop .

FROM alpine:3.20
RUN adduser -D -H -u 1000 landrop
COPY --from=build /out/landrop /usr/local/bin/landrop
USER landrop
WORKDIR /home/landrop
VOLUME /data
EXPOSE 8087
ENTRYPOINT ["landrop", "-d", "/data", "-no-browser"]
