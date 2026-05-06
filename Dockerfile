FROM golang:1.26.2-alpine3.22 AS builder
WORKDIR /go/src/github.com/gliderlabs/registrator/
COPY . .
RUN \
	apk add --no-cache git \
	&& CGO_ENABLED=0 GOOS=linux go build \
		-a -installsuffix cgo \
		-ldflags "-X main.Version=$(cat VERSION)" \
		-o bin/registrator \
		.

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=builder /go/src/github.com/gliderlabs/registrator/bin/registrator /usr/local/bin/registrator

ENTRYPOINT ["registrator"]
