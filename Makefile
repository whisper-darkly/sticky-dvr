VERSION := $(shell cat VERSION)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

BACKEND_IMAGE  := sticky-backend
FRONTEND_IMAGE := sticky-frontend
PROXY_IMAGE    := sticky-proxy

DIST := dist

.PHONY: all build-backend build-initdb build-all \
        image-backend image-frontend image-proxy image-all \
        run-backend run-postgres run-all \
        export-backend export-frontend export-proxy \
        bootstrap logs-all restart down \
        dev dev-down \
        dev-certs clean

# ---- build ----

all: build-all

build-backend:
	mkdir -p $(DIST)
	cd backend && go build \
		-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" \
		-o ../$(DIST)/sticky-backend \
		.

build-initdb:
	mkdir -p $(DIST)
	cd backend && go build \
		-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" \
		-o ../$(DIST)/sticky-initdb \
		./cmd/initdb/

build-all: build-backend build-initdb

# ---- docker images ----

image-backend:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-f Dockerfile.backend \
		-t $(BACKEND_IMAGE):$(VERSION) \
		-t $(BACKEND_IMAGE):latest \
		.

image-frontend:
	docker build \
		-f Dockerfile.frontend \
		-t $(FRONTEND_IMAGE):$(VERSION) \
		-t $(FRONTEND_IMAGE):latest \
		.

image-proxy:
	docker build \
		-f Dockerfile.proxy \
		-t $(PROXY_IMAGE):$(VERSION) \
		-t $(PROXY_IMAGE):latest \
		.

image-all: image-backend image-frontend image-proxy

# ---- bootstrap (zero-to-running) ----

bootstrap: ## Generate certs (if needed), build all images, and start the stack
	@if [ ! -f certs/tls.crt ]; then \
		echo "Generating self-signed TLS certs…"; \
		$(MAKE) dev-certs; \
	else \
		echo "TLS certs already present, skipping cert generation."; \
	fi
	$(MAKE) image-all
	docker compose up -d

# ---- run (docker compose) ----

run-postgres:
	docker compose up -d postgres

run-all:
	docker compose up -d

stop:
	docker compose down

down: ## Stop and remove all compose services
	docker compose down

restart: ## Restart all compose services without rebuilding
	docker compose restart

logs: ## Follow backend logs
	docker compose logs -f backend

logs-all: ## Follow logs from all compose services
	docker compose logs -f

# ---- dev (HTTP-only, no TLS, no JWT) ----

dev: ## Start stack in HTTP-only dev mode (no TLS, no JWT, port 8080)
	docker compose -f compose.yaml -f compose.override.http.yaml up -d

dev-down: ## Stop HTTP-only dev stack
	docker compose -f compose.yaml -f compose.override.http.yaml down

# ---- export (save images to tar for airgapped deploy) ----

export-backend:
	mkdir -p $(DIST)
	docker save $(BACKEND_IMAGE):$(VERSION) | gzip > $(DIST)/$(BACKEND_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DIST)/$(BACKEND_IMAGE)-$(VERSION).tar.gz"

export-frontend:
	mkdir -p $(DIST)
	docker save $(FRONTEND_IMAGE):$(VERSION) | gzip > $(DIST)/$(FRONTEND_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DIST)/$(FRONTEND_IMAGE)-$(VERSION).tar.gz"

export-proxy:
	mkdir -p $(DIST)
	docker save $(PROXY_IMAGE):$(VERSION) | gzip > $(DIST)/$(PROXY_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DIST)/$(PROXY_IMAGE)-$(VERSION).tar.gz"

export-all: export-backend export-frontend export-proxy

# ---- dev TLS certs (self-signed, localhost only) ----

dev-certs:
	mkdir -p certs
	openssl req -x509 -newkey rsa:4096 -nodes -days 365 \
		-keyout certs/tls.key -out certs/tls.crt \
		-subj '/CN=localhost'

# ---- clean ----

clean:
	rm -rf $(DIST)/sticky-backend $(DIST)/sticky-initdb
