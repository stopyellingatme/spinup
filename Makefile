# spinup Makefile — the check contract for lanes and the milestone gate.
# Only the main loop edits this file (see CLAUDE.md, "Shared files").

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

# Pinned toolchain: never download a Go toolchain during a build.
export GOTOOLCHAIN := local
# Node 24 from Homebrew. The interactive shell "node" is an nvm function that does not work here.
# scripts/bin holds pnpm (through corepack, pinned in stackversions.yaml).
export PATH := $(CURDIR)/scripts/bin:/opt/homebrew/opt/node@24/bin:$(PATH)

MODULES := . ./kit
PKGS ?= ./...

.PHONY: help build test lint vuln golden script app-gate doctest e2e dind tools-check bump-stack install

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

e2e: ## Playwright smoke on a generated app with auth (needs DATABASE_URL and the Chromium browser)
	@if [[ -z "$${DATABASE_URL:-}" ]]; then echo "e2e: DATABASE_URL is empty"; echo "Try: export DATABASE_URL=postgres://... (an empty database server you own)"; exit 1; fi
	@bash testdata/e2e/web/run.sh

dind: ## Docker-in-Docker deploy suite: bootstrap, deploy, auto rollback, rollback, lock, secrets (DIND_WEB=1 adds the web app)
	@bash testdata/dind/run.sh

tools-check: ## Fail when a required tool is missing or has the wrong version
	@scripts/tools-check.sh

bump-stack: ## Regenerate the template-pack lockfiles from stackversions.yaml (main loop only)
	@scripts/bump-stack.sh

GOVULNCHECK_VERSION ?= v1.9.0

# Local alpha builds for the dogfood app. Each commit gets a distinct, ordered template version,
# and its templates are kept, so `spinup upgrade` has a real base for the 3-way merge.
DEV_TEMPLATES ?= $(HOME)/.cache/spinup/dev-templates

install: ## Build bin/spinup as 0.1.0-alpha.<commit count> and keep its templates in DEV_TEMPLATES
	@version=0.1.0-alpha.$$(git rev-list --count HEAD); \
	if [[ -n "$$(git status --porcelain)" ]]; then echo "install: commit your changes first"; echo "Try: git commit, then make install"; exit 1; fi; \
	go build -ldflags "-X github.com/stopyellingatme/spinup/internal/cli.Version=$$version" -o bin/spinup ./cmd/spinup; \
	dest="$(DEV_TEMPLATES)/$$version"; rm -rf "$$dest"; mkdir -p "$$dest"; \
	cp -R internal/templates/packs "$$dest/packs"; cp internal/stackversions/stackversions.yaml "$$dest/"; \
	echo "Installed bin/spinup $$version (templates in $$dest)"; \
	echo "Upgrade an app with: SPINUP_TEMPLATES_DIR=$(DEV_TEMPLATES) bin/spinup upgrade"
