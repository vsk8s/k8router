FROM golang:latest

WORKDIR /go/src/github.com/soseth/k8router

COPY ./ ./
ENV GO111MODULE=on
RUN go get -u \
    k8s.io/apimachinery/pkg/apis/meta/v1 \
    k8s.io/client-go/kubernetes \
    k8s.io/client-go/rest
RUN go build cmd/pushd/pushd.go

FROM debian:stretch
COPY --from=0 /go/src/github.com/soseth/k8router/pushd /usr/bin
CMD /usr/bin/pushd
