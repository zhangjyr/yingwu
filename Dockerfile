FROM golang:1.12 as build

WORKDIR /go/src/github.com/zhangjyr/yingwu

COPY k8s                    k8s
COPY gs                     gs
COPY vendor                 vendor
COPY yingwu.go              .

# RUN go test -v ./...
# Stripping via -ldflags "-s -w"
RUN CGO_ENABLED=0 GOOS=linux go build -a -ldflags "-s -w" \
        -installsuffix cgo -o yingwu .

FROM alpine:3.7

COPY --from=build /go/src/github.com/zhangjyr/yingwu/yingwu  /yingwu
COPY --from=build /go/src/github.com/zhangjyr/yingwu/k8s     /k8s

CMD ["/yingwu"]
