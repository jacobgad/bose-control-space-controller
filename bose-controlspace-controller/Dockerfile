FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY internal ./internal
ARG BUILD_VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${BUILD_VERSION}" -o /out/bose-controlspace-controller .

FROM alpine:3.24

ARG BUILD_VERSION=dev
ARG BUILD_ARCH

RUN apk add --no-cache ca-certificates
COPY --from=build /out/bose-controlspace-controller /usr/local/bin/bose-controlspace-controller

LABEL \
  io.hass.name="Bose ControlSpace Controller" \
  io.hass.description="MQTT bridge for Bose ControlSpace ESP processors and PowerMatch amplifiers" \
  io.hass.version="${BUILD_VERSION}" \
  io.hass.type="app" \
  io.hass.arch="aarch64|amd64"

CMD ["/usr/local/bin/bose-controlspace-controller"]
