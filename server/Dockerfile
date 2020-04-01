FROM golang:1.14-alpine AS builder

RUN apk --no-cache add git gcc musl-dev ca-certificates \
   	&& update-ca-certificates

WORKDIR /usr/src/app

# Download module deps (for caching)
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY . .
RUN cd server; CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s"

# Dummy /etc/passwd
RUN echo "root:x:0:0:root\nnobody:x:65534:65534:Nobody:/:" > /etc_passwd

# Make dist image
FROM scratch

# Fill with artifacts
COPY --from=builder /etc_passwd /etc/passwd
COPY --from=builder /usr/src/app/server /

# Volume for mounting config files (config yaml, certs)
VOLUME /config

# Point govpn at the config volume path
ENV GOVPN_CONFIG_FILE=/config/config.yaml \
	GOVPN_TLS_CERT=/config/cert.pem \
	GOVPN_TLS_KEY=/config/key.pem \
	GOVPN_TLS_CA=/config/ca.pem

EXPOSE 443

# Run as nobody
USER root
ENTRYPOINT ["/server"]
