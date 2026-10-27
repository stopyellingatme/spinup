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
RS_KIT_DIR := $(CURDIR)/crates/spinup-kit
RS_APP_TARGET := $(RS_TARGET)/apps
RS_PACK_LOCK := crates/spinup-packs/overlay/api/files/api/Cargo.lock
# Each word is one feature set (comma list). The first ten words are the features alone. The
# rest are the sets that Cargo.toml.tmpl renders (ADR 0021 §2). FEATURES='<words>' narrows.
RS_APP_BASE := db,telemetry,apidocs,devtools
RS_KIT_FEATURES := db auth oidc mail jobs telemetry devtools embedweb apidocs testkit \
	auth,mail auth,jobs jobs,mail auth,oidc,mail \
	$(RS_APP_BASE) $(RS_APP_BASE),embedweb $(RS_APP_BASE),auth,mail $(RS_APP_BASE),auth,oidc,jobs,mail,embedweb
# NOPG=1 skips the tests that need Postgres (names that start with pg_, ADR 0021 §2).
RS_NOPG = $(if $(NOPG), and not test(/(^|::)pg_/))
# The packs_digest test is a gate check (make rs-digest), never a lane check.
RS_FILTER = $(if $(TEST),($(TEST)) and not test(packs_digest),not test(packs_digest))$(RS_NOPG)
RS_GOLDEN_FILTER = test(/^golden_/)$(if $(FILTER), and ($(FILTER)))
COVER_CRATES ?= -p spinup-merge -p spinup-manifest -p spinup-migrate -p spinup-upgrade
COVER_NAMES = $(filter-out -p,$(COVER_CRATES))
COVER_FLOOR ?= 70
DEV_TEMPLATES_RS ?= $(HOME)/.cache/spinup/dev-templates-rs
# Generated apps share one target dir and a cheap release profile in scripts and doc pages.
# Every app package is "api", so two scripts on one target dir race on target/release/api:
# scripts run one at a time (ADR 0021 §11).
RS_SCRIPT_ENV = SPINUP_BIN='$(RS_BIN)' SPINUP_PACKS_SRC='$(RS_PACK_TREE)' SPINUP_STACK_SRC='$(RS_STACK)' \
	SPINUP_KIT_DIR='$(RS_KIT_DIR)' CARGO_TARGET_DIR='$(RS_APP_TARGET)' CARGO_BUILD_JOBS="$${CARGO_BUILD_JOBS:-4}" \
	CARGO_PROFILE_RELEASE_LTO=false CARGO_PROFILE_RELEASE_CODEGEN_UNITS=16 \
	SPINUP_SCRIPT_PARALLEL=1

.PHONY: rs-lint rs-test rs-build rs-deny rs-golden rs-digest rs-script rs-doctest rs-fetch rs-vuln rs-cover rs-tools-check rs-convert-packs rs-convert-check rs-pack-tree rs-ref-check rs-install oracle parity \
	rs-kit-features rs-app-check rs-shellcheck rs-pack-lock rs-app-gate rs-twin-check rs-e2e rs-devloop rs-disk rs-corpus-freeze

rs-lint: ## Rust: cargo fmt --check and clippy -D warnings (the lane lint). CRATES narrows
	@$(CARGO) fmt $(if $(filter --workspace,$(CRATES)),--all,$(CRATES)) --check
	@$(CARGO) clippy --locked $(CRATES) --all-targets -- -D warnings

rs-test: ## Rust: nextest plus doc tests. CRATES narrows, TEST='<filterset>' filters (never packs_digest), NOPG=1 skips pg_ tests
	@$(CARGO) nextest run --locked $(CRATES) --no-tests=warn -E '$(RS_FILTER)'
	@$(CARGO) test --locked $(CRATES) --doc

# Build the spinup binary with -p spinup only: a workspace build unifies the rustls features
# and links aws-lc into the binary (ADR 0021 §9). rs-install and rs-script use -p spinup.
rs-build: ## Rust: build every target (gate only)
	@$(CARGO) build --locked --workspace --all-targets

rs-deny: ## Rust: cargo deny bans, licenses, and sources, offline, then the CLI graph check (gate only)
	@$(CARGO) deny --offline check bans licenses sources
	@scripts/rust/cli-graph-check.sh

rs-golden: ## Rust golden trees (packs and generators). PACK=<name> narrows, FILTER='<filterset>' narrows inside a pack, UPDATE=1 rewrites
	@SPINUP_GOLDEN_PACK='$(or $(PACK),$(SPINUP_GOLDEN_PACK))' SPINUP_GOLDEN_UPDATE='$(or $(UPDATE),$(SPINUP_GOLDEN_UPDATE))' \
		$(CARGO) nextest run --locked -p spinup-packs -p spinup-gen-web -p spinup-gen-app --no-tests=warn -E '$(RS_GOLDEN_FILTER)'

rs-digest: ## Rust: the packs_digest test (gate only). UPDATE=1 rewrites crates/spinup-packs/packs.digest
	@SPINUP_GOLDEN_UPDATE='$(or $(UPDATE),$(SPINUP_GOLDEN_UPDATE))' $(CARGO) nextest run --locked -p spinup-packs -E 'test(packs_digest)'

rs-pack-tree: ## Rust: write the merged pack tree (converted plus overlay) to $(RS_PACK_TREE)
	@scripts/rust/pack-tree.sh '$(RS_PACK_TREE)'

rs-script: rs-pack-tree ## Rust: the Go script harness with the Rust binary (SPINUP_BIN, parity.txt). RUN=<glob> narrows
	@$(CARGO) build --locked -p spinup
	@mkdir -p '$(RS_APP_TARGET)'; rc=0; $(RS_SCRIPT_ENV) SPINUP_SCRIPT_RUN='$(or $(RUN),$(SPINUP_SCRIPT_RUN))' \
		go test ./internal/testutil/scripttest -run 'TestScripts' -count=1 -timeout 60m -v > '$(RS_TARGET)/rs-script.log' 2>&1 || rc=$$?; \
	scripts/rust/script-log.sh $$rc '$(RS_TARGET)/rs-script.log'

rs-doctest: rs-pack-tree ## Rust: the Go doc tests with the Rust binary for the pages in testdata/script/doc-parity.txt, until R5. PAGE='<pages>' narrows
	@$(CARGO) build --locked -p spinup
	@mkdir -p '$(RS_APP_TARGET)'; \
	listed=$$(sed -e 's/#.*//' -e 's/[[:space:]]*$$//' -e '/^[[:space:]]*$$/d' testdata/script/doc-parity.txt); \
	pages='$(PAGE)'; if [[ -z $$pages ]]; then pages=$$listed; fi; \
	n=0; for page in $$pages; do \
		n=$$((n + 1)); \
		if ! grep -qxF -- "$$page" <<<"$$listed"; then \
			echo "rs-doctest: $$page is not in testdata/script/doc-parity.txt"; echo "Try: list the page in doc-parity.txt"; exit 1; fi; \
		scripts/rust/doc-blocks.sh "$$page" || exit 1; \
		pat=$$(printf '%s' "$$page" | sed -e 's/[.]/[.]/g' -e 's#/#$$/^#g'); \
		db=; if [[ -n "$${DATABASE_URL:-}" ]]; then db=$$(scripts/rust/page-db.sh create $$$$_$$n) || exit 1; fi; \
		rc=0; out=$$($(RS_SCRIPT_ENV) env $${db:+"DATABASE_URL=$$db"} \
			go test ./internal/doctest/... -run "^TestDocs$$/^$$pat$$" -count=1 -timeout 30m -v 2>&1) || rc=$$?; \
		if [[ -n $$db ]]; then scripts/rust/page-db.sh drop $$$$_$$n || true; fi; \
		if [ $$rc -ne 0 ]; then printf '%s\n' "$$out"; echo "rs-doctest: $$page failed"; exit 1; fi; \
		if ! printf '%s\n' "$$out" | grep -q -- "--- PASS: TestDocs/$$page "; then \
			echo "rs-doctest: $$page did not run"; echo "Try: check the path in testdata/script/doc-parity.txt"; exit 1; fi; \
		echo "ok $$page"; \
	done

rs-fetch: ## Rust: fetch every locked crate into CARGO_HOME (network; main loop only)
	@CARGO_NET_OFFLINE=false $(CARGO) fetch --locked

rs-vuln: ## Rust: cargo deny advisories (network for the advisory database; gate only)
	@$(CARGO) deny check advisories

rs-cover: ## Rust: line coverage with cargo llvm-cov, $(COVER_FLOOR) percent floor for each crate of COVER_CRATES
	@$(CARGO) llvm-cov nextest --locked $(COVER_CRATES) -E 'not test(packs_digest)' \
		--json --summary-only --output-path '$(RS_TARGET)/cover.json'
	@jq -r --argjson floor $(COVER_FLOOR) --arg names '$(COVER_NAMES)' ' \
		($$names | split(" ")) as $$n \
		| [.data[0].files[] | {c: ((.filename | (try capture("/crates/(?<c>[^/]+)/src/").c catch "")) // ""), l: .summary.lines} \
		   | select(.c as $$x | $$n | index($$x))] \
		| group_by(.c)[] | (map(.l.covered) | add) as $$cov | (map(.l.count) | add) as $$all \
		| "\(.[0].c) \($$cov * 1000 / $$all | floor / 10) \(if $$cov * 100 < $$floor * $$all then "FAIL" else "ok" end)"' \
		'$(RS_TARGET)/cover.json' | tee '$(RS_TARGET)/cover.txt'
	@if grep -q ' FAIL$$' '$(RS_TARGET)/cover.txt'; then echo "rs-cover: a crate is under $(COVER_FLOOR) percent"; \
		echo "Try: add tests to the crate that the FAIL line names"; exit 1; fi

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

rs-ref-check: ## Rust: cargo check of the frozen reference app testdata/reference/todo-rs, with its own target dir
	@if [[ ! -d testdata/reference/todo-rs ]]; then echo "rs-ref-check: testdata/reference/todo-rs is missing"; echo "Try: run the R2 pre-seed"; exit 1; fi
	@cd testdata/reference/todo-rs && SQLX_OFFLINE=true CARGO_TARGET_DIR='$(RS_TARGET)/ref' $(CARGO) check --offline --locked --all-targets

rs-kit-features: ## Rust: the kit without default features, then each set of RS_KIT_FEATURES (FEATURES='<sets>' narrows)
	@$(CARGO) check --locked -p spinup-kit --no-default-features
	@for f in $(or $(FEATURES),$(RS_KIT_FEATURES)); do echo "spinup-kit --features $$f"; \
		$(CARGO) check --locked -p spinup-kit --no-default-features --features $$f || exit 1; done

rs-app-check: rs-pack-tree ## Rust: compile proof of one generated app: SET=small|embed|full|auth|jobs|mail|auth-mail, RESOURCE=1 adds g resource todo
	@$(CARGO) build --locked -p spinup
	@mkdir -p '$(RS_APP_TARGET)'; $(RS_SCRIPT_ENV) scripts/rust/app-check.sh '$(SET)' '$(RESOURCE)'

rs-shellcheck: ## Rust: shellcheck and bash -n on FILES='<paths>'
	@if [[ -z '$(FILES)' ]]; then echo "rs-shellcheck: FILES is empty"; echo "Try: make rs-shellcheck FILES='testdata/e2e/web/run.sh'"; exit 1; fi
	@shellcheck $(FILES)
	@for f in $(FILES); do bash -n "$$f" || exit 1; done

rs-pack-lock: rs-pack-tree ## Rust (main loop): make the api pack Cargo.lock again (FETCH=1 allows the network) and prove it offline
	@$(CARGO) build --locked -p spinup
	@scripts/rust/pack-lock.sh '$(RS_BIN)' '$(RS_PACK_TREE)' '$(RS_STACK)' '$(RS_KIT_DIR)' '$(CURDIR)/$(RS_PACK_LOCK)' '$(FETCH)'

rs-twin-check: ## Rust: fail when a Go script changed after its Rust twin recorded it
	@scripts/rust/twin-check.sh

rs-app-gate: rs-pack-tree ## Rust: generate the gate apps, then build, test, migrate, and serve them († Postgres, Docker, network)
	@if [[ -z "$${DATABASE_URL:-}" ]]; then echo "rs-app-gate: DATABASE_URL is empty"; \
		echo "Try: export DATABASE_URL=postgres://... (a server you own, for example 127.0.0.1:55433)"; exit 1; fi
	@$(CARGO) build --locked -p spinup
	@mkdir -p '$(RS_APP_TARGET)'; rc=0; $(RS_SCRIPT_ENV) SPINUP_APP_GATE=1 SPINUP_SCRIPT_RUN='app_gate_*' \
		go test ./internal/testutil/scripttest -run 'TestScripts' -count=1 -timeout 90m -v > '$(RS_TARGET)/rs-app-gate.log' 2>&1 || rc=$$?; \
	scripts/rust/script-log.sh $$rc '$(RS_TARGET)/rs-app-gate.log' app_gate_api app_gate_web
	@$(MAKE) --no-print-directory rs-ref-check

rs-e2e: rs-pack-tree ## Rust: the e2e scripts with the Rust binary († Postgres, Docker, browsers)
	@$(CARGO) build --locked -p spinup
	@mkdir -p '$(RS_APP_TARGET)'
	@SPINUP_BIN='$(RS_BIN)' SPINUP_KIT_DIR='$(RS_KIT_DIR)' CARGO_TARGET_DIR='$(RS_APP_TARGET)' bash testdata/e2e/web/run.sh
	@SPINUP_BIN='$(RS_BIN)' SPINUP_KIT_DIR='$(RS_KIT_DIR)' CARGO_TARGET_DIR='$(RS_APP_TARGET)' bash testdata/e2e/trace/run.sh

rs-devloop: ## Rust: re-measure the R0.11 dev loop on a generated app with the real kit († Postgres; gate only)
	@if [[ -z "$${DATABASE_URL:-}" ]]; then echo "rs-devloop: DATABASE_URL is empty"; echo "Try: export DATABASE_URL=postgres://..."; exit 1; fi
	@if [[ ! -f scripts/rust/devloop.py ]]; then echo "rs-devloop: scripts/rust/devloop.py is missing"; echo "Try: the main loop writes it before the R2 gate"; exit 1; fi
	@$(CARGO) build --locked -p spinup
	@CARGO_TARGET_DIR='$(RS_TARGET)/devloop' python3 -I scripts/rust/devloop.py \
		--spinup '$(RS_BIN)' --kit '$(RS_KIT_DIR)' \
		--resources spikes-rs/r0-11-devloop/gen/resources.txt --edits 10 --series 2 --limit-ms 5000 \
		--out docs/design/rust-spike/r2-devloop.json

RS_DISK_DIRS ?=
rs-disk: ## Rust: disk use of the target dirs and CARGO_HOME against the 40 GB limit (R0.13 units: KiB x 1024 / 10^9)
	@du -sk '$(RS_TARGET)' "$${CARGO_HOME:-$$HOME/.cargo}" $(RS_DISK_DIRS) | awk \
		'{ s += $$1; printf "%10.2f GB  %s\n", $$1 * 1024 / 1e9, $$2 } \
		 END { t = s * 1024 / 1e9; printf "%10.2f GB  total (limit 40)\n", t; if (t > 40) exit 1 }'
	@df -k . | awk 'NR == 2 { printf "free: %.2f GB\n", $$4 * 1024 / 1e9 }'

RS_CORPUS_VERSION ?= 0.1.0
RS_CORPUS_SET ?= full
rs-corpus-freeze: rs-pack-tree ## Rust (main loop): freeze a generated app as testdata/corpus-rs/<version>/<set>/ (R2 gate item 8)
	@dest=testdata/corpus-rs/$(RS_CORPUS_VERSION)/$(RS_CORPUS_SET); \
	if [[ -e $$dest ]]; then echo "rs-corpus-freeze: $$dest exists"; echo "Try: never regenerate a frozen corpus; choose a new version"; exit 1; fi; \
	SPINUP_VERSION=$(RS_CORPUS_VERSION) $(CARGO) build --locked -p spinup --target-dir '$(RS_TARGET)/corpus'; \
	tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	(cd "$$tmp" && SPINUP_KIT_REPLACE= SPINUP_PACKS_SRC='$(RS_PACK_TREE)' SPINUP_STACK_SRC='$(RS_STACK)' \
		'$(RS_TARGET)/corpus/debug/spinup' new app --module example.com/app \
		--modules ci,auth,jobs,mail --no-git --non-interactive); \
	rm -f "$$tmp/app/.env"; rm -rf "$$tmp/app/target" "$$tmp/app/web/node_modules"; \
	mkdir -p "$$dest/tree" "$$dest/templates/$(RS_CORPUS_VERSION)"; cp -R "$$tmp/app/." "$$dest/tree/"; \
	cp -R '$(RS_PACK_TREE)' "$$dest/templates/$(RS_CORPUS_VERSION)/packs"; cp '$(RS_STACK)' "$$dest/templates/$(RS_CORPUS_VERSION)/stackversions.yaml"; \
	grep -q '"template_version": "$(RS_CORPUS_VERSION)"' "$$dest/tree/spinup.lock" || { echo "rs-corpus-freeze: the lock does not record $(RS_CORPUS_VERSION)"; exit 1; }
	@echo "Next: add edits/ and expect.toml as testdata/corpus/README.md describes"

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
	@mkdir -p '$(RS_APP_TARGET)'; $(RS_SCRIPT_ENV) RUN='$(RUN)' scripts/rust/parity.sh
