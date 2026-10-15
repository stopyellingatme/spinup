# spinup Makefile — the check contract for lanes and the milestone gate.
# Only the main loop edits this file (see CLAUDE.md, "Shared files").

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

# Pinned toolchain: never download a Go toolchain during a build.
export GOTOOLCHAIN := local
# Node 24 from Homebrew. The interactive shell "node" is an nvm function that does not work here.
export PATH := /opt/homebrew/opt/node@24/bin:$(PATH)

MODULES := . ./kit
PKGS ?= ./...

.PHONY: help build test lint vuln golden script app-gate doctest e2e dind tools-check bump-stack

help: ## List the targets
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

build: ## Build every module
	@for m in $(MODULES); do (cd $$m && go build ./...) || exit 1; done

test: ## Unit tests with the race detector (PKGS=./internal/x/... to narrow the root module)
	@go test -race $(PKGS)
	@cd kit && go test -race ./...

lint: ## golangci-lint on every module (offline)
	@for m in $(MODULES); do (cd $$m && golangci-lint run ./...) || exit 1; done

vuln: ## govulncheck on every module (needs network; milestone gate only)
	@for m in $(MODULES); do (cd $$m && go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...) || exit 1; done

golden: ## Golden-tree tests. PACK=<name> narrows, UPDATE=1 rewrites
	@SPINUP_GOLDEN_PACK='$(or $(PACK),$(SPINUP_GOLDEN_PACK))' SPINUP_GOLDEN_UPDATE='$(or $(UPDATE),$(SPINUP_GOLDEN_UPDATE))' go test ./... -run 'TestGolden'

script: ## testscript CLI scripts. RUN=<glob> narrows (app_gate_* is skipped here)
	@SPINUP_SCRIPT_RUN='$(or $(RUN),$(SPINUP_SCRIPT_RUN))' go test ./internal/testutil/scripttest -run 'TestScripts' -count=1

app-gate: ## Generate apps and build/test them (Docker + network; gate check)
	@SPINUP_SCRIPT_RUN='app_gate_*' SPINUP_APP_GATE=1 go test ./internal/testutil/scripttest -run 'TestScripts' -count=1 -timeout 30m

doctest: ## Run the code blocks in docs/
	@if [[ -d internal/doctest ]]; then go test ./internal/doctest/... -run 'TestDocs' -count=1; else echo "doctest: not implemented until M1.8"; fi

e2e: ## Compose + Playwright end-to-end suite (from M4)
	@echo "e2e: not implemented until M4"

dind: ## Docker-in-Docker deploy suite (from M5)
	@echo "dind: not implemented until M5"

tools-check: ## Fail when a required tool is missing or has the wrong version
	@scripts/tools-check.sh

bump-stack: ## Regenerate the template-pack lockfiles from stackversions.yaml (main loop only)
	@scripts/bump-stack.sh

GOVULNCHECK_VERSION ?= v1.9.0
