FROM golang:1.11-alpine AS builder

RUN apk --no-cache add git gcc musl-dev

WORKDIR /usr/src/app
COPY . .

RUN cd server; go build
RUN cd client; go build

FROM golang:1.11-alpine

COPY --from=builder /usr/src/app/server/server /usr/local/bin/server
COPY --from=builder /usr/src/app/client/client /usr/local/bin/client
