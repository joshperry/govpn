FROM golang:1.11-alpine AS builder

RUN apk --no-cache add git gcc musl-dev

WORKDIR /usr/src/app

# Download module deps (for caching)
COPY  go.mod go.sum ./
RUN go mod download

# Build
COPY . .

RUN cd server; go build
RUN cd client; go build

# Make dist container
FROM alpine:3.9

RUN apk --no-cache add iperf tcpdump

WORKDIR /

COPY --from=builder /usr/src/app/server/server /server
COPY --from=builder /usr/src/app/client/client /client

COPY ./server/server-config.yaml ./client/client.crt ./client/client.key ./server/server.crt ./server/server.key ./client/ca.pem /

