REGISTRY ?= zhangjyr
IMAGE = $(REGISTRY)/yingwu

TAG = b1

all: linux

linux:
	CGO_ENABLED=0 GOOS=darwin go build -a -ldflags "-s -w" -installsuffix cgo -o yingwu

container:
	docker build --pull -t $(IMAGE):$(TAG) .

push:
	docker push $(IMAGE):$(TAG)
