VERSION := $(shell cat VERSION)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

BACKEND_IMAGE     := sticky-backend
FRONTEND_IMAGE    := sticky-frontend
PROXY_IMAGE       := sticky-proxy
RECORDER_IMAGE    := sticky-recorder
THUMBNAILER_IMAGE := sticky-thumbnailer
CONVERTER_IMAGE   := sticky-converter

OUTPUT ?= dist
DIST   := $(OUTPUT)/docker
BIN    := $(OUTPUT)/bin

CSS_SRC := frontend/css/src
CSS_OUT := frontend/css/style.css

.PHONY: all build-backend build-initdb build-all build-css \
        image-backend image-frontend image-proxy \
        image-recorder image-thumbnailer image-converter \
        build \
        run-postgres run-all stop down restart logs logs-all \
        export-backend export-frontend export-proxy \
        export-recorder export-thumbnailer export-converter \
        export compose configure config certs dist install \
        bootstrap dev dev-down dev-certs \
        test unit-test smoke-test \
        clean

# ---- Go binaries (dist/bin/) ----

all: build-all

build-css: ## Concatenate CSS source files into frontend/css/style.css
	cat \
	  $(CSS_SRC)/00-variables.css \
	  $(CSS_SRC)/01-reset.css \
	  $(CSS_SRC)/02-layout.css \
	  $(CSS_SRC)/03-cards.css \
	  $(CSS_SRC)/04-buttons.css \
	  $(CSS_SRC)/05-forms.css \
	  $(CSS_SRC)/06-tables.css \
	  $(CSS_SRC)/07-badges.css \
	  $(CSS_SRC)/08-skeleton.css \
	  $(CSS_SRC)/09-alerts.css \
	  $(CSS_SRC)/10-pages.css \
	  $(CSS_SRC)/11-components.css \
	  $(CSS_SRC)/12-theme-picker.css \
	  $(CSS_SRC)/13-responsive.css \
	  $(CSS_SRC)/themes/dark.css \
	  $(CSS_SRC)/themes/fiesta.css \
	  $(CSS_SRC)/themes/twilight.css \
	  $(CSS_SRC)/themes/erotic.css \
	  > $(CSS_OUT)
	@echo "Built $(CSS_OUT)"

build-backend:
	mkdir -p $(BIN)
	cd backend && go build \
		-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" \
		-o ../$(BIN)/sticky-backend \
		.

build-initdb:
	mkdir -p $(BIN)
	cd backend && go build \
		-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" \
		-o ../$(BIN)/sticky-initdb \
		./cmd/initdb/

build-all: build-backend build-initdb

# ---- Docker images ----

image-backend:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-f Dockerfile.backend \
		-t $(BACKEND_IMAGE):$(VERSION) \
		-t $(BACKEND_IMAGE):latest \
		.

image-frontend: build-css
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

image-recorder:
	$(MAKE) -C sticky-recorder/docker build

image-thumbnailer:
	docker build \
		-f Dockerfile.thumbnailer \
		-t $(THUMBNAILER_IMAGE):$(VERSION) \
		-t $(THUMBNAILER_IMAGE):latest \
		.

image-converter:
	$(MAKE) -C sticky-converter/docker build

build: image-backend image-frontend image-proxy image-recorder image-thumbnailer image-converter ## Build all Docker images

# ---- docker compose operations ----

run-postgres:
	docker compose up -d postgres

run-all:
	docker compose up -d

stop:
	docker compose down

down: stop

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

# ---- individual image exports ----

export-backend:
	mkdir -p $(DIST)
	docker save $(BACKEND_IMAGE):$(VERSION) $(BACKEND_IMAGE):latest | gzip > $(DIST)/$(BACKEND_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DIST)/$(BACKEND_IMAGE)-$(VERSION).tar.gz"

export-frontend:
	mkdir -p $(DIST)
	docker save $(FRONTEND_IMAGE):$(VERSION) $(FRONTEND_IMAGE):latest | gzip > $(DIST)/$(FRONTEND_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DIST)/$(FRONTEND_IMAGE)-$(VERSION).tar.gz"

export-proxy:
	mkdir -p $(DIST)
	docker save $(PROXY_IMAGE):$(VERSION) $(PROXY_IMAGE):latest | gzip > $(DIST)/$(PROXY_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DIST)/$(PROXY_IMAGE)-$(VERSION).tar.gz"

export-recorder:
	mkdir -p $(DIST)
	docker save $(RECORDER_IMAGE):$(VERSION) $(RECORDER_IMAGE):latest | gzip > $(DIST)/$(RECORDER_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DIST)/$(RECORDER_IMAGE)-$(VERSION).tar.gz"

export-thumbnailer:
	mkdir -p $(DIST)
	docker save $(THUMBNAILER_IMAGE):$(VERSION) $(THUMBNAILER_IMAGE):latest | gzip > $(DIST)/$(THUMBNAILER_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DIST)/$(THUMBNAILER_IMAGE)-$(VERSION).tar.gz"

export-converter:
	mkdir -p $(DIST)
	docker save $(CONVERTER_IMAGE):$(VERSION) $(CONVERTER_IMAGE):latest | gzip > $(DIST)/$(CONVERTER_IMAGE)-$(VERSION).tar.gz
	@echo "Saved $(DIST)/$(CONVERTER_IMAGE)-$(VERSION).tar.gz"

export: build ## Build and export all images to dist/docker/
	mkdir -p $(DIST)
	docker save $(BACKEND_IMAGE):$(VERSION)     $(BACKEND_IMAGE):latest     | gzip > $(DIST)/$(BACKEND_IMAGE)-$(VERSION).tar.gz
	docker save $(FRONTEND_IMAGE):$(VERSION)    $(FRONTEND_IMAGE):latest    | gzip > $(DIST)/$(FRONTEND_IMAGE)-$(VERSION).tar.gz
	docker save $(PROXY_IMAGE):$(VERSION)       $(PROXY_IMAGE):latest       | gzip > $(DIST)/$(PROXY_IMAGE)-$(VERSION).tar.gz
	docker save $(RECORDER_IMAGE):$(VERSION)    $(RECORDER_IMAGE):latest    | gzip > $(DIST)/$(RECORDER_IMAGE)-$(VERSION).tar.gz
	docker save $(THUMBNAILER_IMAGE):$(VERSION) $(THUMBNAILER_IMAGE):latest | gzip > $(DIST)/$(THUMBNAILER_IMAGE)-$(VERSION).tar.gz
	docker save $(CONVERTER_IMAGE):$(VERSION)   $(CONVERTER_IMAGE):latest   | gzip > $(DIST)/$(CONVERTER_IMAGE)-$(VERSION).tar.gz

# ---- dist sub-targets ----

certs: ## Generate self-signed TLS cert into dist/docker/certs/ (skips if present)
	@if [ ! -f $(DIST)/certs/tls.crt ]; then \
		mkdir -p $(DIST)/certs; \
		IPS=$$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -v '^$$' | sed 's/^/IP:/' | tr '\n' ',' | sed 's/,$$//'); \
		SANS="DNS:localhost,IP:127.0.0.1"; \
		if [ -n "$$IPS" ]; then SANS="$$SANS,$$IPS"; fi; \
		echo "Generating cert with SANs: $$SANS"; \
		openssl req -x509 -newkey rsa:4096 -nodes -days 365 \
			-keyout $(DIST)/certs/tls.key -out $(DIST)/certs/tls.crt \
			-subj '/CN=sticky-dvr' \
			-addext "subjectAltName=$$SANS"; \
	else \
		echo "Certs already present at $(DIST)/certs/, skipping."; \
	fi

compose: ## Copy compose.yaml to dist/docker/
	mkdir -p $(DIST)
	cp compose.yaml $(DIST)/compose.yaml

configure: ## Render config-templates into dist/docker/ (add MERGE_LOCAL=1 to include config.local.yaml)
	mkdir -p $(DIST)/config
	python3 scripts/configure.py --out $(DIST) $(if $(MERGE_LOCAL),--merge-local,)

config: configure ## alias

# ---- dist: full deploy package in dist/docker/ ----

dist: export configure certs ## Build, export, and package everything needed to deploy
	@echo ""
	@echo "Deploy package ready at $(DIST)/"
	@echo "  Images:  $(DIST)/*.tar.gz"
	@echo "  Compose: $(DIST)/compose.yaml"
	@echo "  Configs: $(DIST)/config/"
	@echo ""
	@echo "On the target host:"
	@echo "  for f in *.tar.gz; do docker load -i \$$f; done"
	@echo "  cp .env.example .env  # edit values"
	@echo "  docker compose --env-file .env up -d"

# ---- install: build images and start the stack locally ----

install: build configure ## Build images, render config, and start the stack
	@if [ ! -f certs/tls.crt ]; then \
		echo "No certs found — generating self-signed certs…"; \
		$(MAKE) dev-certs; \
	fi
	docker compose up -d

# ---- bootstrap: first-time setup (certs + install) ----

bootstrap: ## Generate certs if needed, then install
	@if [ ! -f certs/tls.crt ]; then \
		echo "Generating self-signed TLS certs…"; \
		$(MAKE) dev-certs; \
	else \
		echo "TLS certs already present, skipping cert generation."; \
	fi
	$(MAKE) install

# ---- dev TLS certs ----

dev-certs: ## Generate self-signed TLS cert with localhost + LAN IP SANs
	mkdir -p certs
	@IPS=$$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -v '^$$' | sed 's/^/IP:/' | tr '\n' ',' | sed 's/,$$//'); \
	SANS="DNS:localhost,IP:127.0.0.1"; \
	if [ -n "$$IPS" ]; then SANS="$$SANS,$$IPS"; fi; \
	echo "Generating cert with SANs: $$SANS"; \
	openssl req -x509 -newkey rsa:4096 -nodes -days 365 \
		-keyout certs/tls.key -out certs/tls.crt \
		-subj '/CN=sticky-dvr' \
		-addext "subjectAltName=$$SANS"

# ---- tests ----

SMOKE_PROJECT ?= sticky-smoke-$(shell date +%s)

unit-test: ## Run Go unit tests (no containers)
	cd backend && go test ./...

test: build configure ## Build, launch stack, verify health, run tests, tear down
	@echo "Starting test stack (project: $(SMOKE_PROJECT))…"
	docker compose -p $(SMOKE_PROJECT) -f $(DIST)/compose.yaml up -d
	@TIMEOUT=120; ELAPSED=0; INTERVAL=5; \
	while [ $$ELAPSED -lt $$TIMEOUT ]; do \
		UNHEALTHY=$$(docker compose -p $(SMOKE_PROJECT) -f $(DIST)/compose.yaml ps --format json \
			| jq -r 'select(.Health != "" and .Health != "healthy") | .Name' \
			| wc -l | tr -d ' '); \
		if [ "$$UNHEALTHY" = "0" ]; then \
			echo "All services healthy after $$ELAPSED s"; \
			break; \
		fi; \
		echo "Waiting… ($$ELAPSED s elapsed, $$UNHEALTHY services not yet healthy)"; \
		sleep $$INTERVAL; ELAPSED=$$((ELAPSED + INTERVAL)); \
	done; \
	if [ $$ELAPSED -ge $$TIMEOUT ]; then \
		echo "TIMEOUT: not all services became healthy in $$TIMEOUT s"; \
		docker compose -p $(SMOKE_PROJECT) -f $(DIST)/compose.yaml down -v; \
		exit 1; \
	fi
	@echo "--- unit tests ---"
	cd backend && go test ./...
	@echo "--- api tests ---"
	cd backend && TEST_ADDR=http://localhost go test -tags integration -v ./tests/integration/...
	@echo "--- teardown ---"
	docker compose -p $(SMOKE_PROJECT) -f $(DIST)/compose.yaml down -v

smoke-test: build configure certs ## Launch stack, verify health, tear down
	@echo "Starting smoke test (project: $(SMOKE_PROJECT))…"
	docker compose -p $(SMOKE_PROJECT) up -d
	@TIMEOUT=120; ELAPSED=0; INTERVAL=5; \
	while [ $$ELAPSED -lt $$TIMEOUT ]; do \
		UNHEALTHY=$$(docker compose -p $(SMOKE_PROJECT) ps --format json \
			| jq -r 'select(.Health != "" and .Health != "healthy") | .Name' \
			| wc -l | tr -d ' '); \
		if [ "$$UNHEALTHY" = "0" ]; then \
			echo "All services healthy after $$ELAPSED s"; \
			docker compose -p $(SMOKE_PROJECT) down -v; \
			exit 0; \
		fi; \
		echo "Waiting… ($$ELAPSED s elapsed, $$UNHEALTHY services not yet healthy)"; \
		sleep $$INTERVAL; ELAPSED=$$((ELAPSED + INTERVAL)); \
	done; \
	echo "TIMEOUT: not all services became healthy in $$TIMEOUT s"; \
	docker compose -p $(SMOKE_PROJECT) down -v; \
	exit 1

# ---- clean ----

clean:
	rm -rf $(OUTPUT)/
