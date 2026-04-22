BIN_DIR        = bin
BINARY_HARNESS = $(BIN_DIR)/uptime-bench-harness
BINARY_TARGET  = $(BIN_DIR)/uptime-bench-target
BINARY_DNS     = $(BIN_DIR)/uptime-bench-dns

.DEFAULT_GOAL := build

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

.PHONY: build
build: $(BINARY_HARNESS) $(BINARY_TARGET) $(BINARY_DNS)

$(BINARY_HARNESS): $(shell find cmd/harness internal -name '*.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	go build -o $@ ./cmd/harness

$(BINARY_TARGET): $(shell find cmd/target internal -name '*.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	go build -o $@ ./cmd/target

$(BINARY_DNS): $(shell find cmd/dns internal -name '*.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	go build -o $@ ./cmd/dns

.PHONY: clean
clean:
	rm -rf $(BIN_DIR)

# ---------------------------------------------------------------------------
# Test
# ---------------------------------------------------------------------------

.PHONY: test
test:
	go test ./...

.PHONY: test-integration
test-integration:
	go test -tags integration ./...

# ---------------------------------------------------------------------------
# Local development
# ---------------------------------------------------------------------------

.PHONY: dev
dev:
	docker compose up -d
	@echo ""
	@echo "  MySQL:   localhost:$${MYSQL_PORT:-3306}"
	@echo "  Adminer: http://localhost:$${ADMINER_PORT:-8081}"
	@echo ""
	@echo "  System:   uptime_bench"
	@echo "  Username: uptime_bench"
	@echo "  Password: (see .env or .env.example)"

.PHONY: dev-down
dev-down:
	docker compose down

.PHONY: dev-reset
dev-reset:
	docker compose down -v
	docker compose up -d

.PHONY: dev-fleet
dev-fleet:
	docker compose --profile fleet up -d
	@echo ""
	@echo "  MySQL:    localhost:$${MYSQL_PORT:-3306}"
	@echo "  Adminer:  http://localhost:$${ADMINER_PORT:-8081}"
	@echo "  Target:   http://localhost:8080  /  https://localhost:8443"
	@echo ""

.PHONY: dev-fleet-down
dev-fleet-down:
	docker compose --profile fleet down

.PHONY: dev-fleet-reset
dev-fleet-reset:
	docker compose --profile fleet down -v
	docker compose --profile fleet up -d --build

# Run a single scenario via the harness container.
# Prerequisites: `make dev-fleet` must be running; JETMON_BRIDGE_URL and
# JETMON_BRIDGE_TOKEN must be set in .env or the environment.
# Usage: make run-scenario SCENARIO=scenarios/http-503.toml
SCENARIO ?= scenarios/http-503.toml

.PHONY: run-scenario
run-scenario:
	docker compose run --rm harness \
	  uptime-bench-harness \
	    -fleet=/etc/uptime-bench/fleet.toml \
	    -services=/etc/uptime-bench/services.toml \
	    -scenario=/scenarios/$(notdir $(SCENARIO))

.PHONY: logs
logs:
	docker compose logs -f

# ---------------------------------------------------------------------------
# Provision (first-time setup or re-provisioning of fleet hosts)
# ---------------------------------------------------------------------------
# Requires SSH access as the admin user (default: ubuntu) with sudo.
# Run provision before deploy on a new host.
#
# Optional: set HARNESS_IP to restrict the control port to the harness IP.
#   make provision-target TARGET_HOST=203.0.113.20 HARNESS_IP=203.0.113.5

HARNESS_HOST ?= $(error set HARNESS_HOST)
TARGET_HOST  ?= $(error set TARGET_HOST)
DNS_HOST     ?= $(error set DNS_HOST)
HARNESS_IP   ?=
DEPLOY_USER  ?= ubuntu

PROVISION_ARGS = $(if $(HARNESS_IP),--harness-ip $(HARNESS_IP))

.PHONY: provision-harness
provision-harness:
	./deploy/provision.sh --type harness --host $(HARNESS_HOST) --user $(DEPLOY_USER) $(PROVISION_ARGS)

.PHONY: provision-target
provision-target:
	./deploy/provision.sh --type target --host $(TARGET_HOST) --user $(DEPLOY_USER) $(PROVISION_ARGS)

.PHONY: provision-dns
provision-dns:
	./deploy/provision.sh --type dns --host $(DNS_HOST) --user $(DEPLOY_USER) $(PROVISION_ARGS)

# ---------------------------------------------------------------------------
# Deploy (requires SSH access and pre-provisioned hosts)
# ---------------------------------------------------------------------------

.PHONY: deploy-harness
deploy-harness:
	./deploy/deploy.sh harness $(HARNESS_HOST) $(DEPLOY_USER)

.PHONY: deploy-target
deploy-target:
	./deploy/deploy.sh target $(TARGET_HOST) $(DEPLOY_USER)

.PHONY: deploy-dns
deploy-dns:
	./deploy/deploy.sh dns $(DNS_HOST) $(DEPLOY_USER)

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

.PHONY: help
help:
	@echo "uptime-bench"
	@echo ""
	@echo "Build:"
	@echo "  make build            Build all binaries into bin/"
	@echo "  make clean            Remove bin/"
	@echo ""
	@echo "Test:"
	@echo "  make test             Run unit tests"
	@echo "  make test-integration Run integration tests (requires dev services)"
	@echo ""
	@echo "Local dev (MySQL + Adminer only):"
	@echo "  make dev              Start MySQL + Adminer"
	@echo "  make dev-down         Stop dev services"
	@echo "  make dev-reset        Wipe dev DB and restart"
	@echo ""
	@echo "Local dev (full fleet):"
	@echo "  make dev-fleet        Build images and start full fleet (MySQL + fleet components)"
	@echo "  make dev-fleet-down   Stop all fleet services"
	@echo "  make dev-fleet-reset  Wipe volumes, rebuild images, restart full fleet"
	@echo "  make logs             Tail Docker Compose logs"
	@echo ""
	@echo "  make run-scenario     Run a scenario (requires dev-fleet running)"
	@echo "    SCENARIO=scenarios/http-503.toml (default)"
	@echo ""
	@echo "Provision (first-time host setup — run before deploy):"
	@echo "  make provision-harness HARNESS_HOST=host [HARNESS_IP=ip] [DEPLOY_USER=ubuntu]"
	@echo "  make provision-target  TARGET_HOST=host  [HARNESS_IP=ip] [DEPLOY_USER=ubuntu]"
	@echo "  make provision-dns     DNS_HOST=host     [HARNESS_IP=ip] [DEPLOY_USER=ubuntu]"
	@echo ""
	@echo "Deploy (push updated binary and restart service):"
	@echo "  make deploy-harness HARNESS_HOST=host [DEPLOY_USER=ubuntu]"
	@echo "  make deploy-target  TARGET_HOST=host  [DEPLOY_USER=ubuntu]"
	@echo "  make deploy-dns     DNS_HOST=host     [DEPLOY_USER=ubuntu]"
