REGISTRY ?= zhangjyr
IMAGE = $(REGISTRY)/yingwu

TAG = b1

all: container push

build:
	CGO_ENABLED=0 GOOS=darwin go build -a -ldflags "-s -w" -installsuffix cgo -o yingwu

container:
	docker build --pull -t $(IMAGE):$(TAG) .

push:
	docker push $(IMAGE):$(TAG)

deploy:
	kubectl create -f k8s/hyperfaas.json
	kubectl create -f k8s/rbac.yaml

log:
	kubectl logs -f yingwu -n hyperfaas

run:
	kubectl create -f yingwu.yaml

stop:
	kubectl exec yingwu -c yingwu -n hyperfaas -- kill -2 1

clean:
	kubectl delete pod yingwu -n hyperfaas

cleanall:
	kubectl delete namespace hyperfaas

describe:
	kubectl describe pod yingwu -n hyperfaas

pods:
	kubectl get pods -n hyperfaas

output:
	kubectl exec yingwu -c yingwu -n hyperfaas -- cat /data.txt > data/data.txt
