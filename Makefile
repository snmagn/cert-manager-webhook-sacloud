IMAGE_NAME := "snmagn/sacloud-dns-webhook"
IMAGE_TAG := "dirty"
PLATFORM := linux/amd64
GO_VERSION := 1.13
PACKAGE :=
SACLOUD_API_TOKEN := api-token
SACLOUD_API_SECRET := api-secret
SACLOUD_API_ZONE := api-zone
TEST_ZONE_NAME := "example.com."
CLEAN_WITH_IMAGE :=
HELM_NAMESPACE := cert-manager
HELM_LOGLEVEL := 2
HELM_REPOSITORY_OUT := $(shell pwd)/docs
HELM_REPOSITORY_URL := https://snmagn.github.io/cert-manager-webhook-sacloud/

OUT := $(shell pwd)/_out

$(shell mkdir -p "$(OUT)")

.PHONY: fetch-test-binaries

fetch-test-binaries: _out/kubebuilder/bin/kube-apiserver
_out/kubebuilder/bin/kube-apiserver:
	./scripts/fetch-test-binaries.sh

_testdata/my-custom-solver/api-key.yml:
	test -f testdata/my-custom-solver/api-key.yml || (echo "please execute: make generate-api-key SACLOUD_API_TOKEN=api-token SACLOUD_API_SECRET=api-secret SACLOUD_API_ZONE=api-zone" && exit 1)

help:
	@echo "  commands: clean push build test verify generate-api-key rendered-manifest.yaml"
	@echo "  make clean:"
	@echo "    description: clean build cache and image"
	@echo "    example: make clean or make clean CLEAN_WITH_IMAGE=true"
	@echo "  make push:"
	@echo "    description: push docker image with build image"
	@echo "    example: make push IMAGE_TAG=dirty PLATFORM=linux/amd64,linux/arm64"
	@echo "  make build:"
	@echo "    description: build docker image"
	@echo "    example: make build IMAGE_TAG=dirty"
	@echo "  make test:"
	@echo "    description: test after build docker image"
	@echo "    example: make test TEST_ZONE_NAME=example.net."
	@echo "  make verify:"
	@echo "    description: test for golang"
	@echo "    example: make verify TEST_ZONE_NAME=example.net."
	@echo "  make generate-api-key:"
	@echo "    description: generate api-key.yml"
	@echo "    example: make generate-api-key SACLOUD_API_TOKEN=api-token SACLOUD_API_SECRET=api-secret SACLOUD_API_ZONE=api-zone"
	@echo "  make rendered-manifest.yaml:"
	@echo "    description: generate helm chart"
	@echo "    example: make rendered-manifest.yaml HELM_NAMESPACE=example-namespace HELM_LOGLEVEL=2"
	@echo "  make helm-repository:"
	@echo "    description: update helm repository"
	@echo "    example: make helm-repository HELM_REPOSITORY_URL=https://repo.example.com"

clean:
	rm -rf _out testdata/my-custom-solver/api-key.yml
	test "$(CLEAN_WITH_IMAGE)" = "" || docker image rm -f "$(IMAGE_NAME):$(IMAGE_TAG)"

local-push:
	#docker image push "$(IMAGE_NAME):$(IMAGE_TAG)"
	DOCKER_BUILDKIT=1 docker buildx build \
	       --push \
	       --platform $(PLATFORM) \
	       -t "$(IMAGE_NAME):$(IMAGE_TAG)" .
	DOCKER_BUILDKIT=1 docker buildx build \
	       --push \
	       --platform $(PLATFORM) \
	       -t "$(IMAGE_NAME):latest" .

push:
	DOCKER_BUILDKIT=1 docker buildx build \
	       --cache-from "type=local,src=/tmp/.buildx-cache" \
	       --cache-to "type=local,dest=/tmp/.buildx-cache" \
	       --push \
	       --platform $(PLATFORM) \
	       -t "$(IMAGE_NAME):$(IMAGE_TAG)" .
	DOCKER_BUILDKIT=1 docker buildx build \
	       --cache-from "type=local,src=/tmp/.buildx-cache" \
	       --push \
	       --platform $(PLATFORM) \
	       -t "$(IMAGE_NAME):latest" .
build:
	DOCKER_BUILDKIT=1 docker image build \
	       -t "$(IMAGE_NAME):$(IMAGE_TAG)" .

test: _testdata/my-custom-solver/api-key.yml
	DOCKER_BUILDKIT=1 docker image build \
	       --secret id=api-key,src=./testdata/my-custom-solver/api-key.yml \
	       --build-arg SKIP_VERIFY=false \
	       --build-arg TEST_ZONE_NAME=$(TEST_ZONE_NAME) \
	       -t "$(IMAGE_NAME):$(IMAGE_TAG)" .

verify: _testdata/my-custom-solver/api-key.yml _out/kubebuilder/bin/kube-apiserver
	CGO_ENABLED=0 TEST_ZONE_NAME=$(TEST_ZONE_NAME) go test -v .

generate-api-key:
	cp testdata/my-custom-solver/api-key.yml.sample testdata/my-custom-solver/api-key.yml
	sed -i.bak -e s/SACLOUD_API_TOKEN/$(SACLOUD_API_TOKEN)/ testdata/my-custom-solver/api-key.yml
	sed -i.bak -e s/SACLOUD_API_SECRET/$(SACLOUD_API_SECRET)/ testdata/my-custom-solver/api-key.yml
	sed -i.bak -e s/SACLOUD_API_ZONE/$(SACLOUD_API_ZONE)/ testdata/my-custom-solver/api-key.yml
	rm -f testdata/my-custom-solver/api-key.yml.bak

rendered-manifest.yaml:
	helm template \
	     cert-manager-webhook-sacloud \
	     deploy/sacloud-webhook \
	     -n $(HELM_NAMESPACE) \
	     --set logLevel=$(HELM_LOGLEVEL) \
	     --set image.repository=$(IMAGE_NAME) \
	     --set image.tag=$(IMAGE_TAG) \
	     > "$(OUT)/rendered-manifest.yaml"

helm-repository:
	helm package \
	     deploy/sacloud-webhook \
	     -d $(HELM_REPOSITORY_OUT)
	helm repo index \
	     $(HELM_REPOSITORY_OUT) \
	     --url $(HELM_REPOSITORY_URL)
