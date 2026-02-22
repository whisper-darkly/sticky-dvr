VERSION := $(shell cat VERSION)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

BACKEND_IMAGE    := sticky-backend
FRONTEND_IMAGE   := sticky-frontend
PROXY_IMAGE      := sticky-proxy
RECORDER_IMAGE   := sticky-recorder
THUMBNAILER_IMAGE := sticky-thumbnailer
CONVERTER_IMAGE  := sticky-converter

DIST := dist
DEPLOY_DIST := dist/deploy

.PHONY: all build-backend build-initdb build-all \
        image-backend image-frontend image-proxy image-all \
        image-poc-recorder image-poc-thumbnailer image-poc-converter image-poc-all \
        run-backend run-postgres run-all \
        export-backend export-frontend export-proxy \
        export-poc-recorder export-poc-thumbnailer export-poc-converter export-poc-all \
        dist-deploy \
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
	docker save $(BACKEND_IMAGE):$(VERSION) $(BACKEND_IMAGE):latest | gzip > $(DIST)/$(BACKEND_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DIST)/$(BACKEND_IMAGE)-$(VERSION).tar.gz (tags: $(VERSION), latest)"

export-frontend:
	mkdir -p $(DIST)
	docker save $(FRONTEND_IMAGE):$(VERSION) $(FRONTEND_IMAGE):latest | gzip > $(DIST)/$(FRONTEND_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DIST)/$(FRONTEND_IMAGE)-$(VERSION).tar.gz (tags: $(VERSION), latest)"

export-proxy:
	mkdir -p $(DIST)
	docker save $(PROXY_IMAGE):$(VERSION) $(PROXY_IMAGE):latest | gzip > $(DIST)/$(PROXY_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DIST)/$(PROXY_IMAGE)-$(VERSION).tar.gz (tags: $(VERSION), latest)"

export-all: export-backend export-frontend export-proxy

# ---- dev TLS certs (self-signed, includes localhost + all host LAN IPs as SANs) ----

dev-certs:
	mkdir -p certs
	@IPS=$$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -v '^$$' | sed 's/^/IP:/' | tr '\n' ',' | sed 's/,$$//'); \
	SANS="DNS:localhost,IP:127.0.0.1"; \
	if [ -n "$$IPS" ]; then SANS="$$SANS,$$IPS"; fi; \
	echo "Generating cert with SANs: $$SANS"; \
	openssl req -x509 -newkey rsa:4096 -nodes -days 365 \
		-keyout certs/tls.key -out certs/tls.crt \
		-subj '/CN=sticky-dvr' \
		-addext "subjectAltName=$$SANS"

# ---- PoC images (recorder + thumbnailer + converter) ----
# Build context is the parent directory (../) so all sibling repos are accessible.

image-poc-recorder: ## Build sticky-recorder:poc image (recorder + ffmpeg)
	docker build \
		-f poc/Dockerfile.recorder \
		-t $(RECORDER_IMAGE):$(VERSION) \
		-t $(RECORDER_IMAGE):poc \
		-t $(RECORDER_IMAGE):latest \
		..

image-poc-thumbnailer: ## Build sticky-thumbnailer:poc image
	docker build \
		-f poc/Dockerfile.thumbnailer \
		-t $(THUMBNAILER_IMAGE):$(VERSION) \
		-t $(THUMBNAILER_IMAGE):poc \
		-t $(THUMBNAILER_IMAGE):latest \
		..

image-poc-converter: ## Build sticky-converter:poc image (linuxserver/ffmpeg + NVENC)
	docker build \
		-f poc/Dockerfile.converter \
		-t $(CONVERTER_IMAGE):$(VERSION) \
		-t $(CONVERTER_IMAGE):poc \
		-t $(CONVERTER_IMAGE):latest \
		..

image-poc-all: image-poc-recorder image-poc-thumbnailer image-poc-converter ## Build all PoC images

# ---- export PoC images ----

export-poc-recorder:
	mkdir -p $(DEPLOY_DIST)
	docker save $(RECORDER_IMAGE):$(VERSION) $(RECORDER_IMAGE):latest | gzip > $(DEPLOY_DIST)/$(RECORDER_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DEPLOY_DIST)/$(RECORDER_IMAGE)-$(VERSION).tar.gz (tags: $(VERSION), latest)"

export-poc-thumbnailer:
	mkdir -p $(DEPLOY_DIST)
	docker save $(THUMBNAILER_IMAGE):$(VERSION) $(THUMBNAILER_IMAGE):latest | gzip > $(DEPLOY_DIST)/$(THUMBNAILER_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DEPLOY_DIST)/$(THUMBNAILER_IMAGE)-$(VERSION).tar.gz (tags: $(VERSION), latest)"

export-poc-converter:
	mkdir -p $(DEPLOY_DIST)
	docker save $(CONVERTER_IMAGE):$(VERSION) $(CONVERTER_IMAGE):latest | gzip > $(DEPLOY_DIST)/$(CONVERTER_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DEPLOY_DIST)/$(CONVERTER_IMAGE)-$(VERSION).tar.gz (tags: $(VERSION), latest)"

export-poc-all: export-poc-recorder export-poc-thumbnailer export-poc-converter ## Export all PoC images to dist/deploy/

# ---- dist-deploy: build + export everything + copy config ----

dist-deploy: image-all image-poc-all ## Build all images, export to dist/deploy/, copy deploy configs
	mkdir -p $(DEPLOY_DIST)/config $(DEPLOY_DIST)/compose
	# Export core images (version + latest tags in each tar)
	docker save $(BACKEND_IMAGE):$(VERSION)  $(BACKEND_IMAGE):latest  | gzip > $(DEPLOY_DIST)/$(BACKEND_IMAGE)-$(VERSION).tar.gz
	docker save $(FRONTEND_IMAGE):$(VERSION) $(FRONTEND_IMAGE):latest | gzip > $(DEPLOY_DIST)/$(FRONTEND_IMAGE)-$(VERSION).tar.gz
	docker save $(PROXY_IMAGE):$(VERSION)    $(PROXY_IMAGE):latest    | gzip > $(DEPLOY_DIST)/$(PROXY_IMAGE)-$(VERSION).tar.gz
	# Export PoC images (version + latest tags in each tar)
	docker save $(RECORDER_IMAGE):$(VERSION)   $(RECORDER_IMAGE):latest   | gzip > $(DEPLOY_DIST)/$(RECORDER_IMAGE)-$(VERSION).tar.gz
	docker save $(THUMBNAILER_IMAGE):$(VERSION) $(THUMBNAILER_IMAGE):latest | gzip > $(DEPLOY_DIST)/$(THUMBNAILER_IMAGE)-$(VERSION).tar.gz
	docker save $(CONVERTER_IMAGE):$(VERSION)  $(CONVERTER_IMAGE):latest  | gzip > $(DEPLOY_DIST)/$(CONVERTER_IMAGE)-$(VERSION).tar.gz
	# Copy deploy configs
	cp deploy/config/* $(DEPLOY_DIST)/config/
	cp deploy/compose/* $(DEPLOY_DIST)/compose/
	cp deploy/.env.example $(DEPLOY_DIST)/.env.example
	@echo ""
	@echo "Deploy package ready at $(DEPLOY_DIST)/"
	@echo "  Images:  $(DEPLOY_DIST)/*.tar.gz"
	@echo "  Configs: $(DEPLOY_DIST)/config/"
	@echo "  Compose: $(DEPLOY_DIST)/compose/"
	@echo ""
	@echo "On the target host:"
	@echo "  docker network create sticky-dvr"
	@echo "  for f in $(DEPLOY_DIST)/*.tar.gz; do docker load -i \$$f; done"
	@echo "  cp $(DEPLOY_DIST)/.env.example /opt/sticky-dvr/.env  # edit values"
	@echo "  docker compose -f $(DEPLOY_DIST)/compose/postgres.yaml    --env-file /opt/sticky-dvr/.env up -d"
	@echo "  docker compose -f $(DEPLOY_DIST)/compose/backend.yaml     --env-file /opt/sticky-dvr/.env up -d"
	@echo "  docker compose -f $(DEPLOY_DIST)/compose/frontend.yaml    --env-file /opt/sticky-dvr/.env up -d"
	@echo "  docker compose -f $(DEPLOY_DIST)/compose/fileserver.yaml  --env-file /opt/sticky-dvr/.env up -d"
	@echo "  docker compose -f $(DEPLOY_DIST)/compose/overseer.yaml    --env-file /opt/sticky-dvr/.env up -d"
	@echo "  docker compose -f $(DEPLOY_DIST)/compose/thumbnailer.yaml --env-file /opt/sticky-dvr/.env up -d"
	@echo "  docker compose -f $(DEPLOY_DIST)/compose/converter.yaml   --env-file /opt/sticky-dvr/.env up -d"
	@echo "  docker compose -f $(DEPLOY_DIST)/compose/proxy.yaml       --env-file /opt/sticky-dvr/.env up -d"

# ---- clean ----

clean:
	rm -rf $(DIST)/sticky-backend $(DIST)/sticky-initdb
