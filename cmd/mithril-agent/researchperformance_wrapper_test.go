package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResearchPerformanceWrapperChecksBothBoundaries(t *testing.T) {
	runner := readDocumentation(t, "../../deploy/hermes-research/run-market-scout.sh")
	start := strings.Index(runner, "allocation_diagnostic() (")
	end := strings.Index(runner, "\nreplay_rejection_hint()")
	blockStart := strings.Index(runner, "  sol_performance=unavailable")
	blockEnd := strings.Index(runner, "  sol_behavior=unavailable")
	collect := strings.Index(runner, "collect_research_packet() (")
	if start < 0 || end < start || blockStart < collect || blockEnd < blockStart {
		t.Fatal("performance helper or per-attempt prompt block missing")
	}
	helper := strings.NewReplacer("/usr/bin/readlink", "fake_readlink", "/usr/bin/systemctl", "fake_systemctl", "/usr/sbin/runuser", "fake_runuser").Replace(runner[start:end])
	for _, mode := range []string{"pre-champion", "champion", "inactive", "conflict", "failed", "selector-drift", "marker-drift", "service-drift", "command-error", "symlink-marker"} {
		for _, diagnostic := range []string{"performance", "quotes"} {
			t.Run(mode+"/"+diagnostic, func(t *testing.T) {
				block := runner[blockStart:blockEnd]
				if diagnostic == "quotes" {
					from := strings.Index(runner, "  sol_quotes=unavailable")
					through := strings.Index(runner, "  /usr/bin/chmod 0644 \"$research_query\"")
					if from < collect || through < from {
						t.Fatal("per-attempt quote block missing")
					}
					block = runner[from:through]
				}
				root := t.TempDir()
				ratePath := filepath.Join(root, "rate-state")
				if err := os.WriteFile(ratePath, []byte("12345678"), 0600); err != nil {
					t.Fatal(err)
				}
				for _, market := range []string{"sol", "jup"} {
					dir := filepath.Join(root, "status", market)
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					if mode == "champion" {
						if err := os.WriteFile(filepath.Join(dir, "champion-owned"), nil, 0600); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "symlink-marker" {
						if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(dir, "champion-owned")); err != nil {
							t.Fatal(err)
						}
					}
				}
				script := `set -eu
generation=$1
mode=$2
requested_diagnostic=$3
selector=fixed-selector
research_query=$generation/prompt
unset MITHRIL_AGENT_JUPITER_API_KEY
jupiter_api_key=test-quote-credential
export MITHRIL_AGENT_JUPITER_RATE_STATE="$generation/rate-state"
fake_readlink() {
  if [ "$mode" = selector-drift ] && [ -e "$generation/called" ]; then printf different; else printf '%s' "$generation"; fi
}
fake_systemctl() {
  case "$*" in *-pre-champion.service) selected=yes;; *) selected=no;; esac
  [ "$mode" != champion ] || { if [ "$selected" = yes ]; then selected=no; else selected=yes; fi; }
  if [ "$mode" = inactive ] || { [ "$mode" = service-drift ] && [ -e "$generation/called" ]; }; then printf inactive
  elif [ "$mode" = failed ]; then printf failed
  elif [ "$mode" = conflict ] || [ "$selected" = yes ]; then printf active
  else printf inactive; fi
}
fake_runuser() {
  expected_binary=/usr/local/libexec/mithril-agent/mithril-agent
  [ "$requested_diagnostic" != quotes ] || expected_binary=/opt/mithril-hermes-research/mithril-agent-quotes
  if [ "$1" != -u ] || [ "$2" != mithril-agent-research ] || [ "$3" != -- ] || [ "$4" != "$expected_binary" ] ||
     [ "${MITHRIL_AGENT_JUPITER_RATE_STATE-}" != "$generation/rate-state" ]; then
    printf routing > "$generation/violation"; exit 9
  fi
  if [ "$requested_diagnostic" = performance ]; then
    if [ "${MITHRIL_AGENT_JUPITER_API_KEY+x}" = x ]; then printf credential > "$generation/violation"; exit 9; fi
  elif [ "${MITHRIL_AGENT_JUPITER_API_KEY-}" != test-quote-credential ]; then
    printf credential > "$generation/violation"; exit 9
  fi
  age=2m
  [ "$requested_diagnostic" != quotes ] || age=30s
  case "$*" in *"research allocation-$requested_diagnostic --generation "*" --market "*" --role "*" --max-age $age") ;; *) exit 9;; esac
  printf called > "$generation/called"
  if [ "$mode" = marker-drift ]; then touch "$generation/status/sol/champion-owned" "$generation/status/jup/champion-owned"; fi
  if [ "$mode" = command-error ]; then printf 'PRIVATE_TOKEN/partial-output'; printf 'PRIVATE_TOKEN/path' >&2; exit 1; fi
  if [ "$requested_diagnostic" = quotes ]; then
    printf '{"size_basis":"initial_policy_lot","round_trip_route_loss_bps":0,"role_binding_verified":true,"process_health_verified":false}'
  else
    printf '{"realized_micros":-100,"unrealized_micros":-200,"fees_micros":10,"role_binding_verified":true,"process_health_verified":false,"performance":{"active_role_verified":false}}'
  fi
}
` + helper + "\n" + block + "\ncat \"$research_query\"\n"
				output, err := exec.Command("/bin/sh", "-c", script, "test", root, mode, diagnostic).CombinedOutput()
				if err != nil {
					t.Fatalf("wrapper exercise: %v: %s", err, output)
				}
				if _, err := os.Stat(filepath.Join(root, "violation")); !os.IsNotExist(err) {
					t.Fatal("wrapper changed binary routing, rate-state environment or credential isolation")
				}
				if rate, err := os.ReadFile(ratePath); err != nil || string(rate) != "12345678" {
					t.Fatal("wrapper reset shared rate-state bytes")
				}
				text := string(output)
				if strings.Contains(text, "PRIVATE_TOKEN") || strings.Contains(text, root) {
					t.Fatal("private diagnostics entered prompt")
				}
				if mode == "pre-champion" || mode == "champion" {
					if strings.Count(text, `"host_checks":{"scope":"before_and_after_collection","selector_unchanged":true,"ownership_marker_matches":true,"selected_role_active":true,"conflicting_role_inactive":true,"role":"`+mode+`"},"cli_report":{`) != 2 ||
						strings.Count(text, `"role_binding_verified":true,"process_health_verified":false`) != 2 ||
						(diagnostic == "performance" && strings.Count(text, `"performance":{"active_role_verified":false}`) != 2) {
						t.Fatal("wrapper checks missing or original CLI verification flags changed")
					}
					if diagnostic == "performance" && (strings.Count(text, `"realized_micros":-100`) != 2 || !strings.Contains(text, "already include fees")) {
						t.Fatal("verified negative performance was not preserved")
					}
					if diagnostic == "quotes" && (strings.Count(text, `"size_basis":"initial_policy_lot"`) != 2 ||
						!strings.Contains(text, "Zero route loss is not profit") || !strings.Contains(text, "may already be stale")) {
						t.Fatal("quote values or diagnostic limitations missing")
					}
				} else if !strings.Contains(text, "SOL/USDC: unavailable") || !strings.Contains(text, "JUP/USDC: unavailable") {
					t.Fatalf("conflicting evidence leaked as performance: %s", text)
				} else if strings.Contains(text, `"host_checks"`) {
					t.Fatal("failed checks emitted a verified host envelope")
				}
			})
		}
	}
}

func TestResearchQuoteWrapperRefreshesWithoutSharingCredential(t *testing.T) {
	runner := readDocumentation(t, "../../deploy/hermes-research/run-market-scout.sh")
	credentialStart := strings.Index(runner, "unset jupiter_api_key\n")
	credentialEnd := strings.Index(runner, "\n\nallocations=")
	helperStart := strings.Index(runner, "allocation_diagnostic() (")
	helperEnd := strings.Index(runner, "\nreplay_rejection_hint()")
	blockStart := strings.Index(runner, "  sol_quotes=unavailable")
	blockEnd := strings.Index(runner, "  /usr/bin/chmod 0644 \"$research_query\"")
	if credentialStart < 0 || credentialEnd < credentialStart || helperStart < 0 || helperEnd < helperStart || blockStart < 0 || blockEnd < blockStart {
		t.Fatal("quote credential or per-attempt boundaries missing")
	}
	helper := strings.NewReplacer("/usr/bin/readlink", "fake_readlink", "/usr/bin/systemctl", "fake_systemctl", "/usr/sbin/runuser", "fake_runuser").Replace(runner[helperStart:helperEnd])
	for _, test := range []struct {
		name, environment, credential, want string
		createCredential                    bool
	}{
		{name: "environment", environment: "test-quote-credential", want: "test-quote-credential"},
		{name: "missing"},
		{name: "credential-newline", environment: "unused-environment-key", credential: "test-quote-credential\n", want: "test-quote-credential", createCredential: true},
		{name: "credential-no-newline", environment: "unused-environment-key", credential: "test-quote-credential", want: "test-quote-credential", createCredential: true},
		{name: "credential-empty", environment: "unused-environment-key", createCredential: true},
		{name: "credential-empty-line", environment: "unused-environment-key", credential: "\n", createCredential: true},
		{name: "credential-multiline", environment: "unused-environment-key", credential: "test-quote-credential\nsecond-line", createCredential: true},
		{name: "credential-extra-blank-line", environment: "unused-environment-key", credential: "test-quote-credential\n\n", createCredential: true},
		{name: "credential-symlink", environment: "unused-environment-key"},
		{name: "credential-directory", environment: "unused-environment-key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			ratePath := filepath.Join(root, "rate-state")
			if err := os.WriteFile(ratePath, []byte("12345678"), 0600); err != nil {
				t.Fatal(err)
			}
			credentials := filepath.Join(root, "credentials")
			if err := os.Mkdir(credentials, 0700); err != nil {
				t.Fatal(err)
			}
			if test.createCredential {
				if err := os.WriteFile(filepath.Join(credentials, "jupiter-api-key"), []byte(test.credential), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if test.name == "credential-symlink" {
				target := filepath.Join(root, "credential-target")
				if err := os.WriteFile(target, []byte("test-quote-credential"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(credentials, "jupiter-api-key")); err != nil {
					t.Fatal(err)
				}
			}
			if test.name == "credential-directory" {
				if err := os.Mkdir(filepath.Join(credentials, "jupiter-api-key"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			script := `set -eu
generation=$1
selector=fixed-selector
research_query=$generation/prompt
expected_key=$2
export MITHRIL_AGENT_JUPITER_RATE_STATE="$generation/rate-state"
` + runner[credentialStart:credentialEnd] + `
fake_readlink() { printf '%s' "$generation"; }
fake_systemctl() {
  case "$*" in *-pre-champion.service) printf active;; *) printf inactive;; esac
}
fake_runuser() {
  if [ "$1" != -u ] || [ "$2" != mithril-agent-research ] || [ "$3" != -- ] ||
     [ "${MITHRIL_AGENT_JUPITER_RATE_STATE-}" != "$generation/rate-state" ]; then
    printf routing > "$generation/violation"; exit 9
  fi
  if [ "$6" = allocation-performance ]; then
    if [ "$4" != /usr/local/libexec/mithril-agent/mithril-agent ] || [ "${MITHRIL_AGENT_JUPITER_API_KEY+x}" = x ]; then
      printf performance > "$generation/violation"; exit 9
    fi
    printf '{}'; return
  fi
  if [ "$4" != /opt/mithril-hermes-research/mithril-agent-quotes ] || [ "$6" != allocation-quotes ]; then
    printf quotes > "$generation/violation"; exit 9
  fi
  [ "${MITHRIL_AGENT_JUPITER_API_KEY-}" = "$expected_key" ] || exit 8
  [ -n "$expected_key" ] || exit 1
  case "$*" in *"$expected_key"*) exit 9;; esac
  n=0
  [ ! -f "$generation/count" ] || n=$(cat "$generation/count")
  n=$((n + 1))
  printf '%s' "$n" > "$generation/count"
  printf '{"quote_sequence":%s}' "$n"
}
` + helper + `
for attempt in 1 2; do
` + runner[blockStart:blockEnd] + `
  [ "${MITHRIL_AGENT_JUPITER_API_KEY+x}" != x ] || exit 10
  /bin/sh -c 'test "${MITHRIL_AGENT_JUPITER_API_KEY+x}" != x && test "${jupiter_api_key+x}" != x && test "${jupiter_credential_extra+x}" != x' || exit 11
done
allocation_diagnostic sol performance >/dev/null
if allocation_diagnostic sol invalid >/dev/null 2>&1; then exit 12; fi
cat "$research_query"
`
			command := exec.Command("/bin/sh", "-c", script, "test", root, test.want)
			command.Env = append(os.Environ(), "MITHRIL_AGENT_JUPITER_API_KEY="+test.environment, "jupiter_api_key=inherited-placeholder", "CREDENTIALS_DIRECTORY="+credentials)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("quote wrapper: %v: %s", err, output)
			}
			if _, err := os.Stat(filepath.Join(root, "violation")); !os.IsNotExist(err) {
				t.Fatal("credential wrapper selected another binary or changed rate-state forwarding")
			}
			if rate, err := os.ReadFile(ratePath); err != nil || string(rate) != "12345678" {
				t.Fatal("credential wrapper reset shared rate-state bytes")
			}
			text := string(output)
			if strings.Contains(text, "test-quote-credential") || strings.Contains(text, "unused-environment-key") || strings.Contains(text, root) {
				t.Fatal("credential or private path entered quote prompt")
			}
			if test.want == "" {
				if strings.Count(text, "SOL/USDC: unavailable") != 2 || strings.Count(text, "JUP/USDC: unavailable") != 2 {
					t.Fatal("missing credential did not remain unavailable on both attempts")
				}
				return
			}
			for _, sequence := range []string{"1", "2", "3", "4"} {
				if strings.Count(text, `"quote_sequence":`+sequence) != 1 {
					t.Fatal("research attempt reused a previous quote")
				}
			}
		})
	}
}

func TestResearchPrefixPublicationOnlyActiveRoles(t *testing.T) {
	runner := readDocumentation(t, "../../deploy/hermes-research/run-paper-generation.sh")
	if strings.Count(runner, "--publish-research-prefix") != 2 ||
		!strings.Contains(runner, `--alert-status "$status/alerts.json" --publish-research-prefix`) ||
		!strings.Contains(runner, `[ "$role" = champion ] && set -- "$@" --alert-status "$status/alerts.json" --publish-research-prefix`) {
		t.Fatal("research prefix publication must be limited to pre-champion and champion")
	}
}
