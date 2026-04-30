BIN_DIR         = bin
BINARY_HARNESS  = $(BIN_DIR)/uptime-bench-harness
BINARY_TARGET   = $(BIN_DIR)/uptime-bench-target
BINARY_DNS      = $(BIN_DIR)/uptime-bench-dns
BINARY_CERTMINT = $(BIN_DIR)/uptime-bench-certmint
BINARY_REPORT   = $(BIN_DIR)/uptime-bench-report
BINARY_CAPACITY = $(BIN_DIR)/uptime-bench-capacity
BINARY_DOCKERSTATS_EXPORTER = $(BIN_DIR)/uptime-bench-dockerstats-exporter
BINARY_TARGETLOAD = $(BIN_DIR)/uptime-bench-targetload
BINARY_PROBE_IPS_REFRESH = $(BIN_DIR)/probe-ips-refresh

.DEFAULT_GOAL := build

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

.PHONY: build
build: $(BINARY_HARNESS) $(BINARY_TARGET) $(BINARY_DNS) $(BINARY_CERTMINT) $(BINARY_REPORT) $(BINARY_CAPACITY) $(BINARY_DOCKERSTATS_EXPORTER) $(BINARY_TARGETLOAD) $(BINARY_PROBE_IPS_REFRESH)

$(BINARY_HARNESS): $(shell find cmd/harness internal -name '*.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	go build -o $@ ./cmd/harness

$(BINARY_TARGET): $(shell find cmd/target internal -name '*.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	go build -o $@ ./cmd/target

$(BINARY_DNS): $(shell find cmd/dns internal -name '*.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	go build -o $@ ./cmd/dns

$(BINARY_CERTMINT): $(shell find cmd/certmint internal -name '*.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	go build -o $@ ./cmd/certmint

$(BINARY_REPORT): $(shell find cmd/uptime-bench-report internal -name '*.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	go build -o $@ ./cmd/uptime-bench-report

$(BINARY_CAPACITY): $(shell find cmd/uptime-bench-capacity internal/capacitybench -name '*.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	go build -o $@ ./cmd/uptime-bench-capacity

$(BINARY_DOCKERSTATS_EXPORTER): $(shell find cmd/uptime-bench-dockerstats-exporter internal/dockerstats -name '*.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -o $@ ./cmd/uptime-bench-dockerstats-exporter

$(BINARY_TARGETLOAD): $(shell find cmd/uptime-bench-targetload -name '*.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	go build -o $@ ./cmd/uptime-bench-targetload

$(BINARY_PROBE_IPS_REFRESH): $(shell find cmd/probe-ips-refresh internal/probeips -name '*.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	go build -o $@ ./cmd/probe-ips-refresh

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
# Prerequisites: `make dev-fleet` must be running; services.toml must exist
# and have at least one service enabled that matches the scenario's monitors list.
# Usage: make run-scenario SCENARIO=scenarios/http-503.toml
SCENARIO ?= scenarios/http-503.toml
CAMPAIGN_CONFIG ?= configs/campaign/example.toml

.PHONY: run-scenario
run-scenario:
	docker compose run --rm harness \
	  uptime-bench-harness \
	    -fleet=/etc/uptime-bench/fleet.toml \
	    -services=/etc/uptime-bench/services.toml \
	    -scenario=/scenarios/$(notdir $(SCENARIO))

.PHONY: run-campaign
run-campaign:
	docker compose run --rm harness \
	  uptime-bench-harness \
	    -fleet=/etc/uptime-bench/fleet.toml \
	    -services=/etc/uptime-bench/services.toml \
	    -campaign=/campaigns/$(notdir $(CAMPAIGN_CONFIG))

CAMPAIGN ?= $(error set CAMPAIGN)
REPORT_FORMAT ?= table

.PHONY: report-campaign
report-campaign: $(BINARY_REPORT)
	$(BINARY_REPORT) -campaign=$(CAMPAIGN) -format=$(REPORT_FORMAT)

PROMETHEUS_URL ?= http://paprika.internal:9091
CAPACITY_INSTANCES ?= jetmon-v1,jetmon-v2
CAPACITY_DURATION ?= 15m
CAPACITY_FORMAT ?= table
CAPACITY_RUN_DIR ?=
CAPACITY_OUTPUT_DIR ?=
CAPACITY_POSTRUN_DURATION ?= 15m

.PHONY: capacity-metrics
capacity-metrics: $(BINARY_CAPACITY)
	$(BINARY_CAPACITY) \
	  -prometheus-url=$(PROMETHEUS_URL) \
	  -instances=$(CAPACITY_INSTANCES) \
	  -duration=$(CAPACITY_DURATION) \
	  -format=$(CAPACITY_FORMAT)

.PHONY: capacity-capture-run
capacity-capture-run: $(BINARY_CAPACITY)
	@test -n "$(CAPACITY_RUN_DIR)" || (echo "set CAPACITY_RUN_DIR=reports/<run-tag>" >&2; exit 1)
	CAPACITY_BIN=$(BINARY_CAPACITY) \
	  PROMETHEUS_URL=$(PROMETHEUS_URL) \
	  CAPACITY_INSTANCES=$(CAPACITY_INSTANCES) \
	  CAPACITY_POSTRUN_DURATION=$(CAPACITY_POSTRUN_DURATION) \
	  deploy/capture-capacity-window.sh "$(CAPACITY_RUN_DIR)" "$(CAPACITY_OUTPUT_DIR)"

PROBE_IPS_ARGS ?=

.PHONY: refresh-probe-ips
refresh-probe-ips: $(BINARY_PROBE_IPS_REFRESH)
	$(BINARY_PROBE_IPS_REFRESH) $(PROBE_IPS_ARGS)

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

HARNESS_HOST  ?= $(error set HARNESS_HOST)
TARGET_HOST   ?= $(error set TARGET_HOST)
DNS_HOST      ?= $(error set DNS_HOST)
CERTMINT_HOST ?= $(error set CERTMINT_HOST)
HARNESS_IP    ?=
DEPLOY_USER   ?= ubuntu

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

.PHONY: provision-certmint
provision-certmint:
	./deploy/provision.sh --type certmint --host $(CERTMINT_HOST) --user $(DEPLOY_USER) $(PROVISION_ARGS)

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

.PHONY: deploy-certmint
deploy-certmint:
	./deploy/deploy.sh certmint $(CERTMINT_HOST) $(DEPLOY_USER)

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
	@echo "  make report-campaign  Summarize a campaign"
	@echo "    CAMPAIGN=<campaign-run-id-or-config-id> [REPORT_FORMAT=table|tsv|json]"
	@echo "  make capacity-metrics Summarize Jetmon v1/v2 Prometheus capacity metrics"
	@echo "    [PROMETHEUS_URL=http://paprika.internal:9091] [CAPACITY_DURATION=15m] [CAPACITY_FORMAT=table|json]"
	@echo "  make capacity-capture-run CAPACITY_RUN_DIR=reports/<run-tag>"
	@echo "    Capture the exact run.meta.tsv window into reports/<run-tag>/capacity/"
	@echo "  deploy/dockerstats-exporter.sh HOST [USER]  Deploy per-container Prometheus exporter"
	@echo "  bin/uptime-bench-targetload -url-pattern=...  Probe generated target/DNS capacity"
	@echo ""
	@echo "Provision (first-time host setup — run before deploy):"
	@echo "  make provision-harness  HARNESS_HOST=host  [HARNESS_IP=ip] [DEPLOY_USER=ubuntu]"
	@echo "  make provision-target   TARGET_HOST=host   [HARNESS_IP=ip] [DEPLOY_USER=ubuntu]"
	@echo "  make provision-dns      DNS_HOST=host      [HARNESS_IP=ip] [DEPLOY_USER=ubuntu]"
	@echo "  make provision-certmint CERTMINT_HOST=host                     [DEPLOY_USER=ubuntu]"
	@echo ""
	@echo "Deploy (push updated binary and restart service):"
	@echo "  make deploy-harness  HARNESS_HOST=host  [DEPLOY_USER=ubuntu]"
	@echo "  make deploy-target   TARGET_HOST=host   [DEPLOY_USER=ubuntu]"
	@echo "  make deploy-dns      DNS_HOST=host      [DEPLOY_USER=ubuntu]"
	@echo "  make deploy-certmint CERTMINT_HOST=host [DEPLOY_USER=ubuntu]"
