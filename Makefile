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
        mithril-agent-status-bridge
ADAPTER_DIR := adapters/orca
MANIFEST ?= mithril-agent-source.sha256

PREFIX ?= /usr/local/libexec/mithril-agent

.PHONY: help
help:
	@echo "Track A — on your own machine, nothing else required:"
	@echo "  make prereqs         what this needs, all checked at once"
	@echo "  make verify-source   confirm every source file matches the manifest"
	@echo "  make manifest        regenerate that manifest (deliberate, not automatic)"
	@echo "  make check-shadow-isolation  prove shadow mode cannot reach signing code"
	@echo "  make check-funding-isolation prove the funding boundary can only be read"
	@echo "  make build           build all seven binaries into ./$(BIN_DIR)"
	@echo "  make adapter         install the Orca quote adapter (needs Node 24.18+)"
	@echo "  make test            full test suite, race detector, vet, format check"
	@echo "  make explain         print what this software can and cannot do"
	@echo "  make walkthrough     watch the real machinery run (live prices, audit chain)"
	@echo ""
	@echo "Track B — prepared Linux host, operator only:"
	@echo "  make install|configure|setup   prints the runbook; installs nothing."
	@echo "  Privileged steps are written out in README.md so an operator reads"
	@echo "  each change before making it. There is no silent installer."
	@echo ""
	@echo "A reviewer needs only Track A to judge the code."

# Everything this needs, checked at once and reported together. Discovering
# prerequisites one failure at a time is the difference between "five minutes"
# and "an afternoon" for somebody who did not write this.
.PHONY: prereqs
prereqs:
	@ok=1; \
	 if command -v go >/dev/null; then \
	   have=$$(go version 2>/dev/null | awk '{print $$3}'); v=$${have#go}; \
	   if [[ "$$v" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+) ]] && \
	      (( $${BASH_REMATCH[1]} > 1 || \
	         ($${BASH_REMATCH[1]} == 1 && $${BASH_REMATCH[2]} > 25) || \
	         ($${BASH_REMATCH[1]} == 1 && $${BASH_REMATCH[2]} == 25 && $${BASH_REMATCH[3]} >= 12) )); then \
	     echo "  go       $$have  ok"; \
	   else echo "  go       $${have:-UNAVAILABLE} — need 1.25.12+"; ok=0; fi; \
	 else echo "  go       MISSING — needed to build; https://go.dev/dl/"; ok=0; fi; \
	 if command -v node >/dev/null; then \
	   v=$$(node --version | sed 's/^v//'); maj=$${v%%.*}; r=$${v#*.}; min=$${r%%.*}; \
	   if [[ "$$maj" -eq 24 && "$$min" -ge 18 ]]; then echo "  node     v$$v  ok"; \
	   else echo "  node     v$$v  UNSUPPORTED — the quote adapter needs 24.18+ in the 24.x line"; ok=0; fi; \
	 else echo "  node     MISSING — only needed for live quotes; https://nodejs.org/en/download"; ok=0; fi; \
	 if command -v npm >/dev/null; then \
	   have=$$(npm --version 2>/dev/null || true); \
	   if [[ "$$have" =~ ^11\.([0-9]+)\. ]] && (( $${BASH_REMATCH[1]} >= 16 )); then \
	     echo "  npm      $$have  ok"; \
	   else echo "  npm      $${have:-UNAVAILABLE} — need 11.16+ in the 11.x line"; ok=0; fi; \
	 else echo "  npm      MISSING — ships with Node"; ok=0; fi; \
	 if command -v sha256sum >/dev/null || command -v shasum >/dev/null; then \
	   echo "  checksum present  ok"; \
	 else echo "  checksum MISSING — needed by make verify-source"; ok=0; fi; \
	 miss=""; \
	 [[ -z "$$MITHRIL_AGENT_MITHRIL_RPC_URL" ]] && miss="$$miss MITHRIL_AGENT_MITHRIL_RPC_URL"; \
	 [[ -z "$$MITHRIL_AGENT_PRIMARY_RPC_URL" ]] && miss="$$miss MITHRIL_AGENT_PRIMARY_RPC_URL"; \
	 [[ -z "$$MITHRIL_AGENT_SECONDARY_RPC_URL" ]] && miss="$$miss MITHRIL_AGENT_SECONDARY_RPC_URL"; \
	 if [[ -z "$$miss" ]]; then \
	   echo "  RPC endpoints all three set  ok"; \
	 else \
	   echo "  RPC endpoints NOT SET:$$miss"; \
	   echo "                THREE are needed to configure a trade:"; \
	   echo "                  _MITHRIL_  your own node (http on loopback is fine)"; \
	   echo "                  _PRIMARY_ and _SECONDARY_  two https endpoints from"; \
	   echo "                  DIFFERENT providers, so no single provider is the"; \
	   echo "                  only witness to what happened."; \
	   echo "                Not needed to build or explore."; \
	 fi; \
	 echo ""; \
	 if [[ "$$ok" -eq 1 ]]; then \
	   echo "Everything needed is present. Next: make build && make adapter"; \
	 else \
	   echo "Install what is marked above, then re-run: make prereqs"; \
	   echo "Building and exploring need no wallet and no account. Only"; \
	   echo "configuring a trade needs RPC endpoints and Devnet SOL."; \
	 fi; \
	 exit $$((1-ok))

.PHONY: verify-source
verify-source:
	@if [[ ! -f "$(MANIFEST)" ]]; then \
		echo "No manifest at $(MANIFEST); pass MANIFEST=<path>." >&2; exit 1; \
	fi
	@expected=$$(mktemp); listed=$$(mktemp); \
	 trap 'rm -f "$$expected" "$$listed"' EXIT; \
	 find . -type f \( -name '*.go' -o -name '*.mjs' -o -name '*.html' -o -name '*.json' \
	      -o -name '*.md' -o -name 'Makefile' -o -name '*.service' \
	      -o -name '*.socket' -o -name '*.conf' -o -name '*.yml' \
	      -o -name 'go.mod' -o -name 'go.sum' -o -name '.gitignore' \) \
	    -not -path './.git/*' -not -path './bin/*' \
	    -not -path '*/node_modules/*' -not -name '$(MANIFEST)' \
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
	@find . -type f \( -name '*.go' -o -name '*.mjs' -o -name '*.html' -o -name '*.json' \
	     -o -name '*.md' -o -name 'Makefile' -o -name '*.service' \
	     -o -name '*.socket' -o -name '*.conf' -o -name '*.yml' \
	     -o -name 'go.mod' -o -name 'go.sum' -o -name '.gitignore' \) \
	   -not -path './.git/*' -not -path './bin/*' \
	   -not -path '*/node_modules/*' -not -name '$(MANIFEST)' \
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

# Shadow mode is the one component pointed at mainnet, and it is only safe
# there because it cannot sign. The in-package tests check direct imports; this
# checks the whole transitive closure, which is what actually decides whether a
# signing path exists.
.PHONY: check-shadow-isolation
check-shadow-isolation:
	@deps=$$(go list -deps ./shadow); \
	 banned=$$(echo "$$deps" | grep -Ei 'signer|submit|sealed|riskgrant|policyauthority|txflow|ed25519' || true); \
	 if [[ -n "$$banned" ]]; then \
	   echo "shadow mode can reach signing code:" >&2; echo "$$banned" >&2; exit 1; \
	 fi; \
	 ours=$$(echo "$$deps" | grep 'Overclock-Validator' | grep -v '/shadow$$' || true); \
	 if [[ "$$ours" != "github.com/Overclock-Validator/mithril-agent/pricetrigger" ]]; then \
	   echo "shadow mode gained a dependency; re-check it cannot sign:" >&2; \
	   echo "$$ours" >&2; exit 1; \
	 fi
	@echo "shadow mode reaches only pricetrigger and the standard library."

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

.PHONY: test
test:
	@echo "== format =="
	@unformatted=$$(gofmt -l . 2>/dev/null | grep -v node_modules || true); \
		if [[ -n "$$unformatted" ]]; then echo "unformatted: $$unformatted" >&2; exit 1; fi
	@echo "== vet =="
	@go vet ./...
	@echo "== tests =="
	@go test ./... -count=1
	@echo "== race =="
	@go test -race ./... -count=1
	@echo "== shadow isolation =="
	@$(MAKE) --no-print-directory check-shadow-isolation
	@echo "== funding isolation =="
	@$(MAKE) --no-print-directory check-funding-isolation
	@echo "All checks passed."

.PHONY: test-short
test-short:
	@go test ./... -count=1 -short

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
	@echo "Follow README.md, section 'Fresh supervised Linux installation'." >&2
	@echo "Unit files and users are in deploy/systemd and deploy/sysusers." >&2
	@exit 2
