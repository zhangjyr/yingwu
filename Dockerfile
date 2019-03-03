FROM golang:1.9.7 as build

RUN mkdir -p /go/src/github.com/zhangjyr/yingwu
WORKDIR /go/src/github.com/zhangjyr/yingwu

COPY yingwu.go              .
COPY handler.go             .
COPY k8s                    k8s
COPY gs                     gs
COPY vendor                 vendor

# Run a gofmt and exclude all vendored code.
# RUN test -z "$(gofmt -l $(find . -type f -name '*.go' -not -path "./vendor/*"))"

RUN go test -v ./...
# Stripping via -ldflags "-s -w"
RUN CGO_ENABLED=0 GOOS=linux go build -a -ldflags "-s -w \
        -installsuffix cgo -o ics .

FROM alpine:3.7

COPY --from=build /go/src/github.com/zhangjyr/yingwu  /yingwu
COPY --from=build /go/src/github.com/zhangjyr/k8s     /k8s

CMD ["/yingwu"]
