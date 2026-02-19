VERSION  := $(shell cat VERSION)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
IMAGE    := sticky-backend

.PHONY: build install image clean

build:
	go build -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o dist/sticky-backend .

install:
	go install -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" .

image:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		.

clean:
	rm -rf dist/
