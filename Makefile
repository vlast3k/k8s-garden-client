
KIND ?= kind
GOVERSION ?= $(shell go version | awk '{print $$3}' | sed 's/\.[0-9]*$$//')
KIND_CLUSTER ?= cfk8s
VERSION ?= 0.7.1-phase1.1
IMAGE_REPOSITORY ?= k8s-rep
IMAGE_TAG ?= $(VERSION)

build:
	@mkdir -p bin
	GOFLAGS="-gcflags=all=-lang=$(GOVERSION)" CGO_ENABLED=0 GOOS=linux go build -ldflags "-w -s" -trimpath -o bin/rep ./cmd/rep
	GOFLAGS="-gcflags=all=-lang=$(GOVERSION)" CGO_ENABLED=0 GOOS=linux go build -ldflags "-w -s" -trimpath -o bin/watcher ./cmd/watch
	GOFLAGS="-gcflags=all=-lang=$(GOVERSION)" CGO_ENABLED=0 GOOS=linux go build -ldflags "-w -s" -trimpath -o bin/untar ./cmd/untar

image:
	docker build -t $(IMAGE_REPOSITORY):$(IMAGE_TAG) .

unit:
	GOFLAGS="-gcflags=all=-lang=$(GOVERSION)" go test -count=1 ./... -vet=off -cover -coverprofile=coverage.out

lint:
	GOFLAGS="-gcflags=all=-lang=$(GOVERSION)" golangci-lint run

generate:
	GOFLAGS="-gcflags=all=-lang=$(GOVERSION)" go generate ./...

kind:
	rm -rf kind && git clone https://github.com/cloudfoundry/kind-deployment.git kind
	make -C kind create-kind

delete-kind:
	make -C kind down

load-kind: image
	$(KIND) load docker-image $(IMAGE_REPOSITORY):$(IMAGE_TAG) --name $(KIND_CLUSTER)
	
install:
	IMAGE_REPOSITORY=$(IMAGE_REPOSITORY) yq -e -i '.image.repository = strenv(IMAGE_REPOSITORY)' helm/values.yaml
	IMAGE_TAG=$(IMAGE_TAG) yq -e -i '.image.tag = strenv(IMAGE_TAG)' helm/values.yaml
	yq -e -i '.charts.k8sRep.url = strenv(PWD) + "/helm"' kind/versions.yaml

	make -C kind init install login bootstrap-complete

integration: kind load-kind install
	make -C kind smoke

.PHONY: run build image integration unit generate lint certs load-kind install kind delete-kind
