# Reviewer and operator entry points.
#
# Track A (laptop, no wallet/host): verify-source, build, test, explain.
# Track B (prepared Linux host): install, configure, setup — operator only.
# Track B needs Linux, a Devnet node on loopback, and pinned Node; the Track A
# targets deliberately require none of that.

SHELL := /bin/bash
.DEFAULT_GOAL := help

BIN_DIR := bin
CMDS := mithril-agent mithril-agent-policy mithril-agent-signer \
		mithril-agent-submitter mithril-agent-quote mithril-agent-telegram \
		mithril-agent-status-bridge mithril-agent-paper-status-bridge \
		mithril-agent-paper-dashboard
ADAPTER_DIR := adapters/orca
MANIFEST ?= mithril-agent-source.sha256
# Public watch-only owner whose canonical Mainnet USDC account already exists.
# These checks never load a key, sign, or submit for this address.
JUPITER_WATCH_ADDRESS := HvFdDWS3RqymRAVx1ZdoL2RjiC38r5dtt19Z8op5jqDK
ROUTE_GUARD_DIR := programs/mithril-route-guard
ROUTE_GUARD_OUT ?=
MITHRIL_SOURCE ?= ../Mithril

PREFIX ?= /usr/local/libexec/mithril-agent

.PHONY: help
help:
	@echo "Track A — on your own machine, nothing else required:"
	@echo "  make prereqs         check the default walletless build prerequisites"
	@echo "  make prereqs-trading also check the optional trading runtime"
	@echo "  make verify-source   confirm every source file matches the manifest"
	@echo "  make manifest        regenerate that manifest (deliberate, not automatic)"
	@echo "  make check-shadow-isolation  prove shadow mode cannot reach signing code"
	@echo "  make check-walletless-isolation prove program/index packages cannot reach optional execution modules"
	@echo "  make check-funding-isolation prove the funding boundary can only be read"
	@echo "  make check-private-files     refuse private runtime artifacts in this checkout"
	@echo "  make test-account-free       run reliable no-account setup checks below"
	@echo "  make test-walletless         test the default program/index/MCP path offline"
	@echo "  make test-rooted-contract    ingest the sibling public Mithril wire fixture"
	@echo "  make test-free-rehearsal     rehearse the complete unfunded Mainnet boundary offline"
	@echo "  make test-free-custody       qualify local custody without accounts or network calls"
	@echo "  make test-free-policy        verify offline matching Mainnet policy generation"
	@echo "  make test-free-market-data   check public Pyth and Kraken reads"
	@echo "  make test-free-jupiter       check keyless Jupiter build and pinned program reads"
	@echo "  make test-free-evidence      check two public origins retain identical history"
	@echo "  make test-prometheus         validate monitoring rules and alert scenarios"
	@echo "  make test-route-guard        test the isolated keyless Jupiter deployment guard"
	@echo "  make build-route-guard ROUTE_GUARD_OUT=/private/path"
	@echo "                         build SBF outside the checkout (needs Agave CLI 4.2+)"
	@echo "  make build           build all nine binaries into ./$(BIN_DIR)"
	@echo "  make adapter         install the Orca quote adapter (needs Node 24.18+)"
	@echo "  make test            full test suite, race detector, vet, format check"
	@echo "  make explain         print what this software can and cannot do"
	@echo "  make walkthrough     watch the real machinery run (live prices, audit chain)"
	@echo ""
	@echo "Track B — prepared Linux host, operator only:"
	@echo "  make install|configure|setup   prints the runbook; installs nothing."
	@echo "  Privileged steps are written out in QUICKSTART.md so an operator reads"
	@echo "  each change before making it. There is no silent installer."
	@echo ""
	@echo "A reviewer needs only Track A to judge the code."

# The default program/index/MCP path needs only Go and a checksum tool. Keep
# trading-only JavaScript and provider requirements out of this first gate.
.PHONY: prereqs
prereqs:
	@ok=1; \
	 if command -v go >/dev/null; then \
	   have=$$(go version 2>/dev/null | awk '{print $$3}'); v=$${have#go}; \
	   if [[ "$$v" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+) ]] && \
	      (( $${BASH_REMATCH[1]} > 1 || \
	         ($${BASH_REMATCH[1]} == 1 && $${BASH_REMATCH[2]} > 26) || \
	         ($${BASH_REMATCH[1]} == 1 && $${BASH_REMATCH[2]} == 26 && $${BASH_REMATCH[3]} >= 6) )); then \
	     echo "  go       $$have  ok"; \
	   else echo "  go       $${have:-UNAVAILABLE} — need 1.26.6+"; ok=0; fi; \
	 else echo "  go       MISSING — needed to build; https://go.dev/dl/"; ok=0; fi; \
	 if command -v sha256sum >/dev/null || command -v shasum >/dev/null; then \
	   echo "  checksum present  ok"; \
	 else echo "  checksum MISSING — needed by make verify-source"; ok=0; fi; \
	 echo ""; \
	 if [[ "$$ok" -eq 1 ]]; then \
	   echo "Walletless prerequisites are ready. Next: make test-walletless"; \
	 else \
	   echo "Install what is marked above, then re-run: make prereqs"; \
	 fi; \
	 exit $$((1-ok))

# Optional trading needs the pinned JavaScript runtime and four separately
# purposed RPC settings. Report every missing item in one pass.
.PHONY: prereqs-trading
prereqs-trading: prereqs
	@ok=1; \
	 if command -v node >/dev/null; then \
	   v=$$(node --version | sed 's/^v//'); maj=$${v%%.*}; r=$${v#*.}; min=$${r%%.*}; \
	   if [[ "$$maj" -eq 24 && "$$min" -ge 18 ]]; then echo "  node     v$$v  ok"; \
	   else echo "  node     v$$v  UNSUPPORTED — the quote adapter needs 24.18+ in the 24.x line"; ok=0; fi; \
	 else echo "  node     MISSING — needed for live quotes; https://nodejs.org/en/download"; ok=0; fi; \
	 if command -v npm >/dev/null; then \
	   have=$$(npm --version 2>/dev/null || true); \
	   if [[ "$$have" =~ ^11\.([0-9]+)\. ]] && (( $${BASH_REMATCH[1]} >= 16 )); then \
	     echo "  npm      $$have  ok"; \
	   else echo "  npm      $${have:-UNAVAILABLE} — need 11.16+ in the 11.x line"; ok=0; fi; \
	 else echo "  npm      MISSING — ships with Node"; ok=0; fi; \
	 miss=""; \
	 [[ -z "$$MITHRIL_AGENT_MITHRIL_RPC_URL" ]] && miss="$$miss MITHRIL_AGENT_MITHRIL_RPC_URL"; \
	 [[ -z "$$MITHRIL_AGENT_QUOTE_RPC_URL" ]] && miss="$$miss MITHRIL_AGENT_QUOTE_RPC_URL"; \
	 [[ -z "$$MITHRIL_AGENT_PRIMARY_RPC_URL" ]] && miss="$$miss MITHRIL_AGENT_PRIMARY_RPC_URL"; \
	 [[ -z "$$MITHRIL_AGENT_SECONDARY_RPC_URL" ]] && miss="$$miss MITHRIL_AGENT_SECONDARY_RPC_URL"; \
	 if [[ -z "$$miss" ]]; then \
	   echo "  RPC endpoints all four set  ok"; \
	 else \
	   ok=0; \
	   echo "  RPC endpoints NOT SET:$$miss"; \
	   echo "                FOUR are needed to configure a trade:"; \
	   echo "                  _MITHRIL_  your own node (http on loopback is fine)"; \
	   echo "                  _QUOTE_  isolated quote-sidecar reads"; \
	   echo "                  _PRIMARY_ and _SECONDARY_  two https endpoints from"; \
	   echo "                  DIFFERENT providers, so no single provider is the"; \
	   echo "                  only witness to what happened."; \
	   echo "                Not needed to build or explore."; \
	 fi; \
	 echo ""; \
	 if [[ "$$ok" -eq 1 ]]; then \
	   echo "Everything needed is present. Next: make build && make adapter"; \
	 else \
	   echo "Install what is marked above, then re-run: make prereqs-trading"; \
	   echo "The walletless path remains available through make prereqs and make test-walletless."; \
	 fi; \
	 exit $$((1-ok))

.PHONY: verify-source
verify-source:
	@if [[ ! -f "$(MANIFEST)" ]]; then \
		echo "No manifest at $(MANIFEST); pass MANIFEST=<path>." >&2; exit 1; \
	fi
	@expected=$$(mktemp); listed=$$(mktemp); \
	 trap 'rm -f "$$expected" "$$listed"' EXIT; \
	find . -type f \( -name '*.go' -o -name '*.rs' -o -name '*.js' -o -name '*.mjs' -o -name '*.html' -o -name '*.json' \
	      -o -name '*.svg' -o -name '*.woff2' \
	      -o -name '*.yaml' -o -name '*.sh' -o -name '*.example' -o -name 'Dockerfile' -o -name '.dockerignore' \
	      -o -name '*.md' -o -name 'Makefile' -o -name '*.service' \
	      -o -name '*.socket' -o -name '*.path' -o -name '*.timer' -o -name '*.conf' -o -name '*.yml' \
	      -o -name 'go.mod' -o -name 'go.sum' -o -name 'Cargo.toml' \
	      -o -name 'Cargo.lock' -o -name '.gitignore' -o -name 'LICENSE*' -o -name 'NOTICE*' \) \
	    -not -path './.git/*' -not -path './bin/*' \
	    -not -path '*/node_modules/*' -not -path '*/target/*' \
	    -not -path './deploy/hermes-research/state/*' \
	    -not -path './deploy/hermes-research/secrets/*' \
	    -not -name '$(MANIFEST)' \
	    | LC_ALL=C sort > "$$expected"; \
	 awk '{print $$2}' "$(MANIFEST)" | LC_ALL=C sort > "$$listed"; \
	 if ! cmp -s "$$expected" "$$listed"; then \
	   echo "MISMATCH: the manifest does not list exactly the current source files. Do not run it." >&2; \
	   exit 1; \
	 fi
	@sum=$$(command -v sha256sum || command -v shasum); \
	 if [[ -z "$$sum" ]]; then \
	   echo "Cannot verify: neither sha256sum nor shasum is installed." >&2; \
	   echo "This is a missing tool, NOT a failed check. Install one, then retry." >&2; \
	   exit 3; \
	 fi; \
	 case "$$sum" in *shasum) sum="$$sum -a 256";; esac; \
	 if $$sum -c "$(MANIFEST)" >/dev/null; then \
	   echo "OK: every file listed in $(MANIFEST) matches."; \
	 else \
	   echo "MISMATCH: the source does not match the manifest. Do not run it." >&2; \
	   exit 1; \
	 fi

# The manifest is what `verify-source` checks against. Generating it is a
# separate, explicit step so that regenerating it is always a deliberate act:
# a manifest that silently regenerates itself proves nothing about the source.
.PHONY: manifest
manifest:
	@find . -type f \( -name '*.go' -o -name '*.rs' -o -name '*.js' -o -name '*.mjs' -o -name '*.html' -o -name '*.json' \
	     -o -name '*.svg' -o -name '*.woff2' \
	     -o -name '*.yaml' -o -name '*.sh' -o -name '*.example' -o -name 'Dockerfile' -o -name '.dockerignore' \
	     -o -name '*.md' -o -name 'Makefile' -o -name '*.service' \
	     -o -name '*.socket' -o -name '*.path' -o -name '*.timer' -o -name '*.conf' -o -name '*.yml' \
	     -o -name 'go.mod' -o -name 'go.sum' -o -name 'Cargo.toml' \
	     -o -name 'Cargo.lock' -o -name '.gitignore' -o -name 'LICENSE*' -o -name 'NOTICE*' \) \
	   -not -path './.git/*' -not -path './bin/*' \
	   -not -path '*/node_modules/*' -not -path '*/target/*' \
	   -not -path './deploy/hermes-research/state/*' \
	   -not -path './deploy/hermes-research/secrets/*' \
	   -not -name '$(MANIFEST)' \
	   | LC_ALL=C sort \
	   | xargs $$(command -v sha256sum || echo "shasum -a 256") > "$(MANIFEST)"
	@echo "Wrote $(MANIFEST): $$(wc -l < "$(MANIFEST)" | tr -d ' ') files."
	@echo "Reviewers confirm it with: make verify-source"

# Ubuntu logs users in with umask 0002, so a plain `go build` produces
# group-writable binaries — and this software refuses to execute anything a
# group could rewrite between the check and the run. Left alone, the build
# silently produces artifacts the tool rejects, which reads as a broken build.
.PHONY: build
build:
	@umask 022; mkdir -p $(BIN_DIR)
	@ver=$$(git describe --always --dirty 2>/dev/null || echo ""); \
	 for cmd in $(CMDS); do \
		echo "building $$cmd"; \
		( umask 022; go build -ldflags "-X main.version=$$ver" \
		    -o "$(BIN_DIR)/$$cmd" "./cmd/$$cmd" ) || exit 1; \
	 done
	@chmod 755 $(BIN_DIR) && chmod 755 $(BIN_DIR)/* 2>/dev/null || true
	@echo "Built $(words $(CMDS)) binaries into ./$(BIN_DIR)."
	@echo "Note: the Orca quote adapter needs pinned Node and is only used on the host (make adapter)."

# The adapter pins Node 24.18+ and refuses to run on anything else at runtime
# (adapters/orca/quote.mjs requireSupportedNode). Checking here turns a confusing
# runtime refusal into one sentence at install time.
.PHONY: adapter
adapter:
	@if ! command -v node >/dev/null; then \
	  echo "No 'node' on PATH. The Orca quote adapter needs Node 24.18 or newer." >&2; \
	  exit 2; \
	fi
	@have=$$(node --version | sed 's/^v//'); \
	 major=$${have%%.*}; rest=$${have#*.}; minor=$${rest%%.*}; \
	 if [[ "$$major" -ne 24 || "$$minor" -lt 18 ]]; then \
	   echo "Node $$have is not supported: the quote adapter requires 24.18 or newer" >&2; \
	   echo "in the 24.x line, and refuses to run on anything else." >&2; \
	   exit 2; \
	 fi; \
	 echo "Node $$have: supported."
	@cd $(ADAPTER_DIR) && umask 022 && npm ci
	@chmod 644 $(ADAPTER_DIR)/quote.mjs
	@chmod go-w $(ADAPTER_DIR) $(ADAPTER_DIR)/node_modules 2>/dev/null || true
	@echo "Adapter ready at $(ADAPTER_DIR)/quote.mjs — setup will find it."

# Shadow mode is safe to point at Mainnet because it cannot sign. The
# in-package tests check direct imports; this checks the whole transitive
# closure, which is what actually decides whether a signing path exists.
.PHONY: check-shadow-isolation
check-shadow-isolation:
	@deps=$$(go list -deps ./shadow); \
	 banned=$$(echo "$$deps" | grep -Ei 'signer|submit|sealed|riskgrant|policyauthority|txflow|ed25519' || true); \
	 if [[ -n "$$banned" ]]; then \
	   echo "shadow mode can reach signing code:" >&2; echo "$$banned" >&2; exit 1; \
	 fi; \
	 ours=$$(echo "$$deps" | grep 'Overclock-Validator' | grep -v '/shadow$$' || true); \
	 allowed=$$(printf '%s\n' \
	   'github.com/Overclock-Validator/mithril-agent/internal/base58' \
	   'github.com/Overclock-Validator/mithril-agent/pricetrigger'); \
	 if [[ "$$ours" != "$$allowed" ]]; then \
	   echo "shadow mode gained a dependency; re-check it cannot sign:" >&2; \
	   echo "$$ours" >&2; exit 1; \
	 fi
	@echo "shadow mode reaches only keyless base58 validation, pricetrigger, and the standard library."

# Keep the default program/index/MCP boundary independent from every optional
# trading, signer, submission, Telegram, and policy package. The exact internal
# closure makes a new dependency a reviewed change rather than a silent import.
.PHONY: check-walletless-isolation
check-walletless-isolation:
	@deps=$$(go list -deps ./programinterface ./rootedindex ./indexmcp ./internal/mcpstdio ./solana ./solanarpc); \
	 ours=$$(echo "$$deps" | grep 'github.com/Overclock-Validator/mithril-agent' | LC_ALL=C sort); \
	 allowed=$$(printf '%s\n' \
	   'github.com/Overclock-Validator/mithril-agent/indexmcp' \
	   'github.com/Overclock-Validator/mithril-agent/internal/base58' \
	   'github.com/Overclock-Validator/mithril-agent/internal/fileowner' \
	   'github.com/Overclock-Validator/mithril-agent/internal/mcpstdio' \
	   'github.com/Overclock-Validator/mithril-agent/internal/secureexec' \
	   'github.com/Overclock-Validator/mithril-agent/internal/securefile' \
	   'github.com/Overclock-Validator/mithril-agent/internal/strictjson' \
	   'github.com/Overclock-Validator/mithril-agent/journal' \
	   'github.com/Overclock-Validator/mithril-agent/programinterface' \
	   'github.com/Overclock-Validator/mithril-agent/rootedindex' \
	   'github.com/Overclock-Validator/mithril-agent/solana' \
	   'github.com/Overclock-Validator/mithril-agent/solanarpc' | LC_ALL=C sort); \
	 if [[ "$$ours" != "$$allowed" ]]; then \
	   echo "walletless packages gained an internal dependency; review the execution boundary:" >&2; \
	   echo "$$ours" >&2; exit 1; \
	 fi
	@echo "walletless packages cannot reach optional trading, signer, submission, Telegram, or policy modules."

# The funding boundary is only worth having because this software cannot use
# it. If the squads package ever gains the ability to sign, the boundary is
# only as good as our own code — which is precisely what it exists to avoid.
.PHONY: check-funding-isolation
check-funding-isolation:
	@deps=$$(go list -deps ./squads); \
	 banned=$$(echo "$$deps" | grep -Ei 'signer|submit|sealed|riskgrant|policyauthority|txflow' || true); \
	 if [[ -n "$$banned" ]]; then \
	   echo "the squads package can reach signing code:" >&2; echo "$$banned" >&2; exit 1; \
	 fi
	@echo "the funding boundary can only be read, never used."

# Protected runtime material belongs outside the source checkout. Git ignore
# rules prevent ordinary adds; this check also catches files before a build or
# test packages a checkout that contains them.
.PHONY: check-private-files
check-private-files:
	@found=$$(find . -type f \( \
	      -name '*.private' -o -name '*.env' -o -name '*.env.*' \
	      -o -iname '*credential*.json' -o -name '*keypair*.json' \
	      -o -name 'agent-account.json' -o -name 'wallet.json' \
	      -o -name '*-wallet.json' -o -name 'attestor.json' \
	      -o -name '*-attestor.json' -o -name '*-key.json' \
	      -o -name 'transport-key' -o -name '*-transport-key' \
	      -o -name '*-candidate.json' -o -name '*-policy.json' \
	      -o -name 'submission-recovery*.json' -o -name 'operator-approval.json' \
	      -o -name '*-signer-request.json' -o -name '*-signer-response.json' \
	      -o -name '*-check-result.json' -o -name 'control.json*' \
	      -o -name '*.jsonl*' -o -name 'config.json' -o -name 'observation.json' \
	      -o -name 'update-cursor.json*' \
	    \) -not -path './.git/*' -not -path './bin/*' \
	       -not -path '*/node_modules/*' -not -path '*/target/*' -print); \
	 if [[ -n "$$found" ]]; then \
	   echo "Private runtime material is inside the source checkout:" >&2; \
	   echo "$$found" >&2; \
	   echo "Move it to a private directory outside the repository before continuing." >&2; \
	   exit 1; \
	 fi
	@echo "no private runtime artifacts are present in the source checkout."

.PHONY: test
test:
	@echo "== format =="
	@unformatted=$$(gofmt -l . 2>/dev/null | grep -v node_modules || true); \
		if [[ -n "$$unformatted" ]]; then echo "unformatted: $$unformatted" >&2; exit 1; fi
	@echo "== vet =="
	@go vet ./...
	@echo "== tests =="
	@umask 077; go test ./... -count=1
	@echo "== race =="
	@umask 077; go test -race ./... -count=1
	@echo "== shadow isolation =="
	@$(MAKE) --no-print-directory check-shadow-isolation
	@echo "== funding isolation =="
	@$(MAKE) --no-print-directory check-funding-isolation
	@echo "== private runtime files =="
	@$(MAKE) --no-print-directory check-private-files
	@echo "All checks passed."

.PHONY: test-route-guard
test-route-guard:
	@cargo fmt --check --manifest-path $(ROUTE_GUARD_DIR)/Cargo.toml
	@cargo test --locked --manifest-path $(ROUTE_GUARD_DIR)/Cargo.toml
	@cargo clippy --all-targets --locked --manifest-path $(ROUTE_GUARD_DIR)/Cargo.toml -- -D warnings

.PHONY: build-route-guard
build-route-guard:
	@out='$(ROUTE_GUARD_OUT)'; \
	 if [[ -z "$$out" || "$$out" != /* ]]; then \
	   echo "ROUTE_GUARD_OUT must be an absolute protected directory outside the checkout." >&2; exit 1; \
	 fi; \
	 repo=$$(pwd -P); \
	 case "$$out/" in "$$repo/"*) \
	   echo "ROUTE_GUARD_OUT must be outside the source checkout." >&2; exit 1;; \
	 esac; \
	 install -d -m 0700 "$$out" || exit 1; \
	 out=$$(cd "$$out" && pwd -P) || exit 1; \
	 case "$$out/" in "$$repo/"*) \
	   echo "ROUTE_GUARD_OUT must be outside the source checkout." >&2; exit 1;; \
	 esac
	@command -v cargo-build-sbf >/dev/null || { \
		echo "cargo-build-sbf is required; install Agave CLI 4.2+, then retry." >&2; exit 1; \
	}
	@version=$$(cargo-build-sbf --version | head -1 | awk '{print $$NF}'); \
	 if [[ "$$version" =~ ^([0-9]+)\.([0-9]+)\. ]] && \
	    (( $${BASH_REMATCH[1]} > 4 || \
	       ($${BASH_REMATCH[1]} == 4 && $${BASH_REMATCH[2]} >= 1) )); then :; \
	 else echo "cargo-build-sbf $$version is too old; install Agave CLI 4.2+." >&2; exit 1; fi
	@cd $(ROUTE_GUARD_DIR) && cargo-build-sbf \
		--sbf-out-dir '$(ROUTE_GUARD_OUT)' --tools-version v1.54 -- --locked

.PHONY: test-short
test-short:
	@umask 077; go test ./... -count=1 -short

.PHONY: test-prometheus
test-prometheus:
	@command -v promtool >/dev/null || { \
		echo "promtool is required; install Prometheus, then retry." >&2; exit 1; \
	}
	@promtool check rules deploy/prometheus/mithril-agent.rules.yml
	@promtool test rules deploy/prometheus/mithril-agent.rules.test.yml
	@echo "Prometheus rules and alert scenarios passed."

# Exercise the complete self-hosted signer and offline submitter boundaries
# without an RPC endpoint, funded wallet, hosted-custody account, or broadcast.
# This proves our transaction validation and isolation; it deliberately does
# not claim that a third-party provider enforces the same policy.
.PHONY: test-free-custody
test-free-custody:
	@umask 077; go test -race ./signer ./signerclient ./submitter \
		./cmd/mithril-agent-signer ./cmd/mithril-agent-submitter -count=1
	@echo "OK: local custody, pinned SSH signer, and offline submitter qualification passed."
	@echo "No provider account, RPC endpoint, funded wallet, provider call, or broadcast was used."
	@echo "A hosted provider is optional; if selected, it needs its own retained-transaction qualification."

# Prove a non-technical operator can create each non-wallet boundary identity
# locally and never has to hand-author three matching Mainnet policy files.
# Temporary test keys stay inside private test directories.
.PHONY: test-free-policy
test-free-policy:
	@umask 077; go test ./cmd/mithril-agent -run '^TestProposal(KeyCreate|PolicyCreate|BundleCheck|ApprovalCreate|AuthorityCheck|SubmitterCheck|CanaryCheck)' -count=1
	@echo "OK: offline identities, policy generation, exact approval, and candidate bundle checks passed."
	@echo "No provider account, network call, trading wallet, authorization, signature, or send was used."

# Rehearse the complete unfunded Mainnet boundary with the existing exact
# composition and command checks. Keep this separate from the live keyless
# market/Jupiter checks so it is deterministic and cannot reach a network.
.PHONY: test-free-rehearsal
test-free-rehearsal:
	@$(MAKE) --no-print-directory test-free-policy
	@$(MAKE) --no-print-directory test-free-custody
	@echo "Offline Mainnet boundary rehearsal passed; Mainnet remains disabled."

# Check the complete account-free market-evidence baseline against live public
# data. An operator may override the public Mainnet RPC without changing which
# Pyth accounts or independent exchange sources the production code pins.
.PHONY: test-free-market-data
test-free-market-data:
	@umask 077; \
	 MITHRIL_AGENT_LIVE_PRICE_TEST=1 \
	 MITHRIL_AGENT_LIVE_SOLANA_RPC="$${MITHRIL_AGENT_LIVE_SOLANA_RPC:-https://api.mainnet-beta.solana.com}" \
	 go test ./pricesource \
		-run 'TestLive(PythPushMatchesKraken|USDCUSDEvidence)$$' -count=1 -v
	@echo "OK: the account-free Pyth and Kraken evidence path is live."
	@echo "This is a current-data smoke test, not a production availability or SLA qualification."

# Exercise today's Jupiter build contract and pinned on-chain deployment using
# only a watch address. The repository retains no private key for the default
# address, and the empty API-key assignment proves the keyless path.
.PHONY: test-free-jupiter
test-free-jupiter:
	@umask 077; \
	 MITHRIL_AGENT_JUPITER_API_KEY='' \
	 MITHRIL_AGENT_LIVE_JUPITER_TEST=1 \
	 MITHRIL_AGENT_LIVE_JUPITER_TAKER="$${MITHRIL_AGENT_LIVE_JUPITER_TAKER:-$(JUPITER_WATCH_ADDRESS)}" \
	 MITHRIL_AGENT_LIVE_MAINNET_RPC_URL="$${MITHRIL_AGENT_LIVE_MAINNET_RPC_URL:-https://api.mainnet-beta.solana.com}" \
	 go test ./jupiterswap \
		-run 'TestLive(PinnedJupiterDeployment|CurrentJupiterIDL)$$' -count=1 -v
	@umask 077; \
	 MITHRIL_AGENT_JUPITER_API_KEY='' \
	 MITHRIL_AGENT_LIVE_JUPITER_TEST=1 \
	 MITHRIL_AGENT_LIVE_JUPITER_TAKER="$${MITHRIL_AGENT_LIVE_JUPITER_TAKER:-$(JUPITER_WATCH_ADDRESS)}" \
	 go test ./turnkeycustody \
		-run '^TestLiveCurrentJupiterQualificationPolicyFits$$' -count=1 -v
	@echo "OK: both keyless Jupiter routes, deployment, IDL, and hosted-policy shapes passed."
	@echo "Only a watch address was used; no key was loaded and nothing was signed or submitted."

# Prove the two-provider evidence contract against a dated public fixture. Keep
# this strict check separate from the reliable account-free setup target: a
# shared public origin may route to nodes with different archive retention.
# Public endpoints are not a production SLA, and production policy must pin a
# separately qualified pair.
.PHONY: test-free-evidence
test-free-evidence:
	@if ! MITHRIL_AGENT_PRIMARY_RPC_URL='https://api.mainnet-beta.solana.com' \
	 MITHRIL_AGENT_SECONDARY_RPC_URL='https://solana.lava.build' \
	 go run ./cmd/mithril-agent proposal evidence-check \
		--primary-trust-domain solana-foundation-public \
		--secondary-trust-domain lava-network-public \
		--archive-probe-signature 2eLMRUZzCAhF2KjUeD6JJXpWVeMtPYbqNShFbLeKYSdKLNmAKXs2oUN3u5odBJFeZoTEve4huLHAMw8LUJCXzyD; then \
	   echo "The optional public archive drill stopped safely." >&2; \
	   echo "The public endpoints did not complete the protected genesis and archive agreement check." >&2; \
	   echo "The command output above names the exact failed stage." >&2; \
	   echo "This does not invalidate make test-account-free or the local implementation." >&2; \
	   echo "Production still requires two separately qualified archive providers." >&2; \
	   exit 1; \
	 fi
	@echo "OK: two no-signup public origins currently agree on retained finalized history."
	@echo "This does not qualify either public endpoint for production."

.PHONY: test-account-free
test-account-free:
	@$(MAKE) --no-print-directory test-free-custody
	@$(MAKE) --no-print-directory test-free-policy
	@$(MAKE) --no-print-directory test-free-market-data
	@$(MAKE) --no-print-directory test-free-jupiter
	@echo "All reliable account-free setup checks passed."
	@echo "Offline test identities exercised signing; no operator wallet was loaded and nothing was submitted."
	@echo "Run make test-free-evidence separately to test current public archive availability."

.PHONY: test-walletless
test-walletless:
	@$(MAKE) --no-print-directory check-walletless-isolation
	@umask 077; go test ./programinterface ./rootedindex ./indexmcp ./internal/mcpstdio ./solana ./solanarpc -count=1
	@umask 077; go test ./cmd/mithril-agent \
		-run '^(TestProgram|TestDecodeProgram|TestIndex|TestMainHelpMentionsProgram|TestFullStrategyDocumentationMatchesGeneratedLayout)' -count=1
	@echo "Walletless program, rooted-index, and local MCP checks passed."
	@echo "No wallet, account, external RPC, signature, or submission was used."

.PHONY: test-rooted-contract
test-rooted-contract:
	@test -f "$(MITHRIL_SOURCE)/pkg/rootedfeed/testdata/framed-v1.jsonl" || \
	  { echo "Public Mithril fixture not found; set MITHRIL_SOURCE=/absolute/path/to/Mithril" >&2; exit 2; }
	@test -f "$(MITHRIL_SOURCE)/pkg/rootedfeed/testdata/framed-transaction-v1.jsonl" || \
	  { echo "Public Mithril transaction-v1 fixture not found; set MITHRIL_SOURCE=/absolute/path/to/Mithril" >&2; exit 2; }
	@test -f "$(MITHRIL_SOURCE)/pkg/rpcserver/testdata/walletless-provenance-v1.json" || \
	  { echo "Public Mithril provenance fixture not found; set MITHRIL_SOURCE=/absolute/path/to/Mithril" >&2; exit 2; }
	@umask 077; MITHRIL_ROOTED_CONTRACT_FIXTURE="$(abspath $(MITHRIL_SOURCE))/pkg/rootedfeed/testdata/framed-v1.jsonl" \
	  MITHRIL_ROOTED_V1_CONTRACT_FIXTURE="$(abspath $(MITHRIL_SOURCE))/pkg/rootedfeed/testdata/framed-transaction-v1.jsonl" \
	  go test ./rootedindex -run '^TestPublicMithril(Rooted|V1Transaction)ContractFixture$$' -count=1
	@umask 077; MITHRIL_PROVENANCE_CONTRACT_FIXTURE="$(abspath $(MITHRIL_SOURCE))/pkg/rpcserver/testdata/walletless-provenance-v1.json" \
	  go test ./solanarpc -run '^TestPublicMithrilProvenanceContractFixture$$' -count=1
	@echo "Public Mithril framed output, transaction-v1, and provenance RPC contracts are accepted by the agent consumer."

.PHONY: explain
explain:
	@go run ./cmd/mithril-agent explain

.PHONY: walkthrough
walkthrough:
	@go run ./cmd/mithril-agent walkthrough

.PHONY: install configure setup
install configure setup:
	@echo "'$@' runs on the prepared Linux host and is not implemented as a" >&2
	@echo "silent installer: each privileged step is written out so an operator" >&2
	@echo "reviews what changes on their machine before it changes." >&2
	@echo "" >&2
	@echo "Follow QUICKSTART.md from top to bottom." >&2
	@echo "Unit files and users are in deploy/systemd and deploy/sysusers." >&2
	@exit 2
