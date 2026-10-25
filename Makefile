# spinup Makefile — the check contract for lanes and the milestone gate.
# Only the main loop edits this file (see CLAUDE.md, "Shared files").

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

# Pinned toolchain: never download a Go toolchain during a build.
export GOTOOLCHAIN := local
# Node 24 from Homebrew. The interactive shell "node" is an nvm function that does not work here.
# scripts/bin holds pnpm (through corepack, pinned in stackversions.yaml).
export PATH := $(CURDIR)/scripts/bin:/opt/homebrew/opt/node@24/bin:$(PATH)

MODULES := . ./kit ./plugin-sdk
PKGS ?= ./...

.PHONY: help build test lint vuln golden script app-gate doctest e2e dind tools-check bump-stack install

help: ## List the targets
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

build: ## Build every module
	@for m in $(MODULES); do (cd $$m && go build ./...) || exit 1; done

test: ## Unit tests with the race detector (PKGS=./internal/x/... to narrow the root module)
	@go test -race $(PKGS)
	@cd kit && go test -race ./...
	@cd plugin-sdk && go test -race ./...

lint: ## golangci-lint on every module (offline)
	@for m in $(MODULES); do (cd $$m && golangci-lint run ./...) || exit 1; done

vuln: ## govulncheck on every module (needs network; milestone gate only)
	@for m in $(MODULES); do (cd $$m && go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...) || exit 1; done

golden: ## Golden-tree tests. PACK=<name> narrows, UPDATE=1 rewrites
	@SPINUP_GOLDEN_PACK='$(or $(PACK),$(SPINUP_GOLDEN_PACK))' SPINUP_GOLDEN_UPDATE='$(or $(UPDATE),$(SPINUP_GOLDEN_UPDATE))' go test ./... -run 'TestGolden'

script: ## testscript CLI scripts. RUN=<glob> narrows (app_gate_* is skipped here)
	@SPINUP_SCRIPT_RUN='$(or $(RUN),$(SPINUP_SCRIPT_RUN))' go test ./internal/testutil/scripttest -run 'TestScripts' -count=1 -timeout 30m

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

# ---- Rust (docs/design/rust-plan.md §3, ADR 0020) --------------------------------------------
# Lanes run only rs-lint and rs-test for their own crates (CLAUDE.md, check forms RT, RG, RS).
# Lanes set CARGO_NET_OFFLINE=true, CARGO_BUILD_JOBS=2, and their own CARGO_TARGET_DIR.
# Parameters: CRATES="-p a -p b" (default: the whole workspace), TEST='<nextest filterset>',
# PACK=<pack>, FILTER='<filterset>', UPDATE=1, RUN=<glob>.

CARGO ?= cargo
CRATES ?= --workspace
RS_TARGET := $(or $(CARGO_TARGET_DIR),$(CURDIR)/target)
RS_BIN := $(RS_TARGET)/debug/spinup
RS_PACK_TREE := $(RS_TARGET)/pack-tree
RS_STACK := $(CURDIR)/crates/spinup-stack/stackversions.yaml
# The packs_digest test is a gate check (make rs-digest), never a lane check.
RS_FILTER = $(if $(TEST),($(TEST)) and not test(packs_digest),not test(packs_digest))
RS_GOLDEN_FILTER = test(/^golden_/)$(if $(FILTER), and ($(FILTER)))
COVER_CRATES ?= -p spinup-merge -p spinup-manifest -p spinup-migrate
COVER_FLOOR ?= 70
DEV_TEMPLATES_RS ?= $(HOME)/.cache/spinup/dev-templates-rs

.PHONY: rs-lint rs-test rs-build rs-deny rs-golden rs-digest rs-script rs-doctest rs-fetch rs-vuln rs-cover rs-tools-check rs-convert-packs rs-convert-check rs-pack-tree rs-ref-check rs-install oracle parity

rs-lint: ## Rust: cargo fmt --check and clippy -D warnings (the lane lint). CRATES narrows
	@$(CARGO) fmt $(if $(filter --workspace,$(CRATES)),--all,$(CRATES)) --check
	@$(CARGO) clippy --locked $(CRATES) --all-targets -- -D warnings

rs-test: ## Rust: nextest plus doc tests. CRATES narrows, TEST='<filterset>' filters (never packs_digest)
	@$(CARGO) nextest run --locked $(CRATES) --no-tests=warn -E '$(RS_FILTER)'
	@$(CARGO) test --locked $(CRATES) --doc

rs-build: ## Rust: build every target (gate only)
	@$(CARGO) build --locked --workspace --all-targets

rs-deny: ## Rust: cargo deny bans, licenses, and sources, offline (gate only)
	@$(CARGO) deny --offline check bans licenses sources

rs-golden: ## Rust golden trees. PACK=<name> narrows, FILTER='<filterset>' narrows inside a pack, UPDATE=1 rewrites
	@SPINUP_GOLDEN_PACK='$(or $(PACK),$(SPINUP_GOLDEN_PACK))' SPINUP_GOLDEN_UPDATE='$(or $(UPDATE),$(SPINUP_GOLDEN_UPDATE))' \
		$(CARGO) nextest run --locked -p spinup-packs --no-tests=warn -E '$(RS_GOLDEN_FILTER)'

rs-digest: ## Rust: the packs_digest test (gate only). UPDATE=1 rewrites crates/spinup-packs/packs.digest
	@SPINUP_GOLDEN_UPDATE='$(or $(UPDATE),$(SPINUP_GOLDEN_UPDATE))' $(CARGO) nextest run --locked -p spinup-packs -E 'test(packs_digest)'

rs-pack-tree: ## Rust: write the merged pack tree (converted plus overlay) to $(RS_PACK_TREE)
	@scripts/rust/pack-tree.sh '$(RS_PACK_TREE)'

rs-script: rs-pack-tree ## Rust: the Go script harness with the Rust binary (SPINUP_BIN, parity.txt). RUN=<glob> narrows
	@$(CARGO) build --locked -p spinup
	@SPINUP_BIN='$(RS_BIN)' SPINUP_PACKS_SRC='$(RS_PACK_TREE)' SPINUP_STACK_SRC='$(RS_STACK)' \
		SPINUP_SCRIPT_RUN='$(or $(RUN),$(SPINUP_SCRIPT_RUN))' go test ./internal/testutil/scripttest -run 'TestScripts' -count=1 -timeout 30m -v \
		> '$(RS_TARGET)/rs-script.log' 2>&1; rc=$$?; grep -E -- '--- (PASS|FAIL|SKIP): TestScripts/' '$(RS_TARGET)/rs-script.log'; \
	if [ $$rc -ne 0 ]; then tail -60 '$(RS_TARGET)/rs-script.log'; exit $$rc; fi; \
	if ! grep -q -- '--- PASS: TestScripts/' '$(RS_TARGET)/rs-script.log'; then \
		echo "rs-script: no script ran"; echo "Try: list the script in testdata/script/parity.txt, or check RUN"; exit 1; fi

rs-doctest: ## Rust: the Go doc tests with the Rust binary for the pages in testdata/script/doc-parity.txt, until R5
	@$(CARGO) build --locked -p spinup
	@pages=$$(sed -e 's/#.*//' -e '/^[[:space:]]*$$/d' testdata/script/doc-parity.txt); \
	for page in $$pages; do \
		pat=$$(printf '%s' "$$page" | sed -e 's/[.]/[.]/g' -e 's#/#$$/^#g'); \
		out=$$(SPINUP_BIN='$(RS_BIN)' go test ./internal/doctest/... -run "^TestDocs$$/^$$pat$$" -count=1 -v 2>&1); rc=$$?; \
		if [ $$rc -ne 0 ]; then printf '%s\n' "$$out"; echo "rs-doctest: $$page failed"; exit 1; fi; \
		if ! printf '%s\n' "$$out" | grep -q -- "--- PASS: TestDocs/$$page "; then \
			echo "rs-doctest: $$page did not run"; echo "Try: check the path in testdata/script/doc-parity.txt"; exit 1; fi; \
		echo "ok $$page"; \
	done

rs-fetch: ## Rust: fetch every locked crate into CARGO_HOME (network; main loop only)
	@CARGO_NET_OFFLINE=false $(CARGO) fetch --locked

rs-vuln: ## Rust: cargo deny advisories (network for the advisory database; gate only)
	@$(CARGO) deny check advisories

rs-cover: ## Rust: line coverage with cargo llvm-cov, $(COVER_FLOOR) percent floor. COVER_CRATES narrows
	@$(CARGO) llvm-cov nextest --locked $(COVER_CRATES) -E 'not test(packs_digest)' --fail-under-lines $(COVER_FLOOR)

rs-tools-check: ## Rust: check the Rust tool versions
	@scripts/rust/tools-check.sh

rs-convert-packs: ## Rust: convert the Go packs into crates/spinup-packs/packs/. PACK=<name>[,<name>] narrows
	@go run ./scripts/rust/convert-templates $(if $(PACK),-pack '$(PACK)')

rs-convert-check: ## Rust: fail when a new conversion differs from crates/spinup-packs/packs/
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	go run ./scripts/rust/convert-templates -out "$$tmp/packs" > /dev/null; \
	if ! diff -r -x .gitkeep crates/spinup-packs/packs "$$tmp/packs"; then \
		echo "rs-convert-check: the converted packs are stale"; echo "Try: make rs-convert-packs"; exit 1; \
	fi

rs-ref-check: ## Rust: cargo check of testdata/reference/todo-rs with SQLX_OFFLINE (from R2)
	@if [[ ! -d testdata/reference/todo-rs ]]; then echo "rs-ref-check: testdata/reference/todo-rs arrives in the R2 pre-seed"; exit 1; fi
	@cd testdata/reference/todo-rs && SQLX_OFFLINE=true $(CARGO) check --offline --locked --all-targets

rs-install: rs-pack-tree ## Rust: build bin/spinup-rs as 0.1.0-alpha.<commit count> and keep its templates in DEV_TEMPLATES_RS
	@version=0.1.0-alpha.$$(git rev-list --count HEAD); \
	if [[ -n "$$(git status --porcelain)" ]]; then echo "rs-install: commit your changes first"; echo "Try: git commit, then make rs-install"; exit 1; fi; \
	SPINUP_VERSION=$$version $(CARGO) build --locked --release -p spinup; \
	mkdir -p bin; cp '$(RS_TARGET)/release/spinup' bin/spinup-rs; \
	dest="$(DEV_TEMPLATES_RS)/$$version"; rm -rf "$$dest"; mkdir -p "$$dest"; \
	cp -R '$(RS_PACK_TREE)' "$$dest/packs"; cp '$(RS_STACK)' "$$dest/stackversions.yaml"; \
	echo "Installed bin/spinup-rs $$version (templates in $$dest)"; \
	echo "Upgrade an app with: SPINUP_TEMPLATES_DIR=$(DEV_TEMPLATES_RS) bin/spinup-rs upgrade"

oracle: ## Regenerate the Go oracle fixtures in testdata/oracle/ (the gate wants no diff)
	@go test ./internal/oracle/... -count=1 -args -update
	@git status --short -- testdata/oracle

parity: rs-pack-tree ## Run the scripts with Go and with Rust, and print one line for each script. RUN=<glob> narrows
	@$(CARGO) build --locked -p spinup
	@SPINUP_BIN='$(RS_BIN)' SPINUP_PACKS_SRC='$(RS_PACK_TREE)' SPINUP_STACK_SRC='$(RS_STACK)' RUN='$(RUN)' scripts/rust/parity.sh
