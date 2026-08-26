package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	"github.com/Overclock-Validator/mithril-agent/signer"
)

// setup ends by telling the operator to run the runner and "leave it going".
// That instruction is the single largest defect in this product, and it is not
// a wording problem:
//
//   - a runner in a terminal dies when the terminal closes, the laptop sleeps,
//     or the SSH session drops;
//   - it dies SILENTLY, while the wallet stays armed, because arming is
//     recorded on disk and survives the process that was meant to use it;
//   - and nothing notices, because the thing that would have noticed was the
//     process that died.
//
// doctor now detects the resulting state. Detecting it is not enough — an
// operator should not have to be told about a failure that should not have been
// possible. This command removes the terminal from the picture by handing the
// runner to the init system, which is the one component on the machine whose
// entire job is keeping a process alive.
//
// It writes a unit derived ENTIRELY from what setup already recorded: the legs
// come from the strategy pointer, the binary from this executable, the writable
// paths from each leg's own state and journal locations. Nothing about any
// particular host is baked in, because a unit that names one deployment's paths
// is a unit that silently supervises the wrong thing on the next one.
const serviceUsage = `Usage: mithril-agent service install [--output PATH]

Writes a systemd unit that keeps the runner alive, so it survives closed
terminals, dropped connections and reboots.

Installing the unit arms nothing. Granting spending authority stays a separate,
bounded, explicit command.

With no --output the unit is printed. Run this command as the same service
identity that owns the strategy; it stages the units there and prints the exact
privileged install commands:

  sudo -u mithril-agent env HOME=/var/lib/mithril-agent \
    mithril-agent service install \
    --output /var/lib/mithril-agent/.mithril-agent/mithril-agent-run.service`

const serviceUnitName = "mithril-agent-run.service"

const supervisedQuoteSocket = "/run/mithril-agent-quote/quote.sock"

// The runner unit alone produces a working agent that TELLS NOBODY. Trades
// execute, the sweep pays out, and the operator watching Telegram sees silence,
// because the path from a leg's status file to the bot is four more units that
// name that leg's paths — and they were hand-written per deployment.
//
// A fresh install therefore ended with alerting that was not broken so much as
// never connected, which is indistinguishable from broken and worse than an
// error: an operator who goes away expecting messages gets none, and reads that
// as "nothing happened".
//
// These are generated from the same recorded legs the runner unit is, so the
// two cannot drift apart.
const (
	statusGroupName       = "mithril-agent-status"
	nodeStateGroupName    = "mithril-node-state"
	signerAccountName     = "mithril-agent-signer"
	riskAccountName       = "mithril-agent-policy"
	submitterAccountName  = "mithril-agent-submitter"
	alertsAccountName     = "mithril-agent-telegram"
	alertsUnitName        = "mithril-agent-alerts.service"
	alertsEnvFile         = "/etc/mithril-agent/telegram-operator.env"
	alertsCursorPath      = "/var/lib/mithril-agent-telegram/update-cursor.json"
	statusCredential      = "operator-status"
	statusUnitPrefix      = "mithril-agent-status-"
	statusSocketPrefix    = "/run/mithril-agent-status-"
	signerSocketPrefix    = "/run/mithril-agent-signer-"
	riskSocketPrefix      = "/run/mithril-agent-policy-"
	submitterSocketPrefix = "/run/mithril-agent-submitter-"
	operatorSocketPrefix  = "/run/mithril-agent-submitter-operator-"
	defaultOperatorSocket = operatorSocketPrefix + "agent.sock"
)

// serviceLeg is one leg's share of the alert wiring: the status file the runner
// rewrites every cycle, and the socket that publishes it read-only.
type serviceLeg struct {
	Name            string
	StatusFile      string
	SignerPolicy    string
	SignerKeypair   string
	SignerStateDir  string
	RiskPolicy      string
	RiskKeypair     string
	SubmitterPolicy string
	SubmitterKey    string
	ControlStateDir string
}

func (l serviceLeg) unit() string   { return statusUnitPrefix + l.Name }
func (l serviceLeg) socket() string { return statusSocketPrefix + l.Name + ".sock" }
func (l serviceLeg) signerUnit() string {
	return "mithril-agent-signer-" + l.Name
}
func (l serviceLeg) signerSocket() string    { return signerSocketPrefix + l.Name + ".sock" }
func (l serviceLeg) riskUnit() string        { return "mithril-agent-policy-" + l.Name }
func (l serviceLeg) riskSocket() string      { return riskSocketPrefix + l.Name + ".sock" }
func (l serviceLeg) submitterUnit() string   { return "mithril-agent-submitter-" + l.Name }
func (l serviceLeg) submitterSocket() string { return submitterSocketPrefix + l.Name + ".sock" }
func (l serviceLeg) recoveryUnit() string    { return "mithril-agent-recovery-" + l.Name }
func (l serviceLeg) operatorUnit() string {
	return "mithril-agent-submitter-operator-" + l.Name
}
func (l serviceLeg) operatorSocket() string { return operatorSocketPrefix + l.Name + ".sock" }

// A strategy runner binds one metrics port per leg, starting at a base. The
// default base is the same port a single-leg `swap run` uses, so on a host that
// already has one the runner binds, collides, and EXITS — discovered only at
// runtime, as a crash loop, on a real host.
//
// Supervised units restart forever by design (StartLimitIntervalSec=0), so a
// port collision becomes a permanent loop rather than a visible failure. The
// generated unit therefore never inherits the colliding default: it names a
// base of its own, clear of the single-leg runner's range.
const defaultMetricsBasePort = 9310

// The runner reads its RPC endpoints and API keys from the environment
// (MITHRIL_AGENT_MITHRIL_RPC_URL and friends), and the documented deployment
// keeps them in these files. A unit that omitted them would produce a runner
// that starts cleanly and can reach nothing — a NEW silent failure, and a worse
// one than the terminal it replaces.
//
// rpc.env is required, so a missing one fails the unit loudly at start rather
// than at the first trade. The rest are optional, marked with systemd's "-".
var defaultEnvironmentFiles = []string{
	"/etc/mithril-agent/rpc.env",
	"-/etc/mithril-agent/quote.env",
	"-/etc/mithril-agent/mcp.env",
	"-/etc/mithril-agent/price.env",
}

func runService(args []string, output io.Writer) error {
	if len(args) == 0 {
		_, err := fmt.Fprintln(output, serviceUsage)
		return err
	}
	switch args[0] {
	case "install":
		return runServiceInstall(args[1:], output)
	case "-h", "--help", "help":
		_, err := fmt.Fprintln(output, serviceUsage)
		return err
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func runServiceInstall(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("service install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	unitPath := flags.String("output", "", "write the unit here instead of printing it")
	basePort := flags.Int("metrics-base-port", defaultMetricsBasePort,
		"first metrics port for the runner; one port per leg from here")
	var envFiles stringList
	flags.Var(&envFiles, "env-file",
		"environment file for the runner; repeatable, replaces the documented defaults")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, serviceUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("service install takes no positional arguments")
	}
	clean := ""
	updating := false
	if *unitPath != "" {
		clean = filepath.Clean(*unitPath)
		if !filepath.IsAbs(clean) {
			return errors.New("--output must be an absolute path")
		}
		if !systemdAtom(clean) {
			return errors.New("--output contains characters that are unsafe in the printed install command")
		}
		if err := validateSafeParent(filepath.Dir(clean), 0o022); err != nil {
			return errors.New("--output directory must be private and trusted")
		}
		exists, targetErr := generatedUnitTarget(clean)
		if targetErr != nil {
			return fmt.Errorf("--output: %w", targetErr)
		}
		updating = exists
	}

	plan, err := buildServicePlan(*basePort)
	if err != nil {
		return err
	}
	// A runner binds one metrics port per leg. If any of them is already taken,
	// the runner exits at startup — and because the unit restarts forever by
	// design, that becomes a permanent crash loop discovered only in the journal.
	//
	// This is not hypothetical twice over: the default once collided with a
	// single-leg `swap run`, and then the NEW default collided with a runner left
	// behind by an earlier rehearsal, crash-looping seven times before anyone
	// looked. Checking here costs one bind attempt and turns it into a sentence.
	if !updating {
		if err := metricsPortsFree(*basePort, metricsPortSpan(plan.Legs)); err != nil {
			return err
		}
	}
	if len(envFiles) != 0 {
		plan.EnvFiles = envFiles
		plan, err = plan.checked()
		if err != nil {
			return err
		}
	}
	unit := renderServiceUnit(plan)
	if *unitPath == "" {
		_, err := fmt.Fprint(output, unit)
		return err
	}
	name := filepath.Base(clean)
	directory := filepath.Dir(clean)
	staged := []string{name}
	// Socket-activated authorities and read-only status bridges always go beside
	// the runner. The Telegram process joins them only when it is enabled.
	units := supportUnits(plan)
	// Refuse every destination before writing any of them. Checking only the
	// runner unit let a generated status or Telegram unit replace an unrelated
	// file with the same name halfway through an otherwise successful install.
	for _, unitName := range sortedKeys(units) {
		if _, err := generatedUnitTarget(filepath.Join(directory, unitName)); err != nil {
			return fmt.Errorf("%s: %w", unitName, err)
		}
	}
	if err := writeGeneratedUnit(clean, []byte(unit)); err != nil {
		return fmt.Errorf("write the unit: %w", err)
	}
	for _, unitName := range sortedKeys(units) {
		if err := writeGeneratedUnit(
			filepath.Join(directory, unitName), []byte(units[unitName]),
		); err != nil {
			return fmt.Errorf("write %s: %w", unitName, err)
		}
		staged = append(staged, unitName)
	}

	installStep := ""
	if directory != "/etc/systemd/system" {
		// One file gets its destination named in full; several get the directory,
		// which is the only form install(1) accepts for multiple sources.
		destination := "/etc/systemd/system/"
		if len(staged) == 1 {
			destination += staged[0]
		}
		installStep = fmt.Sprintf("  sudo install -o root -g root -m 0644 %s %s\n",
			strings.Join(prefixed(directory, staged), " "), destination)
	}
	installedUnits := strings.Join(prefixed("/etc/systemd/system", staged), " ")
	// An already-active socket keeps its loaded settings across daemon-reload.
	// Stop the runner first, then restart every authority socket before bringing
	// it back, so an update cannot mix new services with stale listeners.
	if _, err := fmt.Fprintf(output,
		"Wrote %d unit(s) into %s:\n  %s\n\nReview them, then install:\n  less %s\n%s"+
			"\nPrepare the isolated signer, risk-authority, and submitter accounts:\n"+
			"  id %s >/dev/null 2>&1 || sudo useradd --system --no-create-home --user-group %s\n%s"+
			"  id %s >/dev/null 2>&1 || sudo useradd --system --no-create-home --user-group %s\n%s"+
			"  id %s >/dev/null 2>&1 || sudo useradd --system --no-create-home --user-group %s\n%s"+
			"\nVerify every installed unit before loading it:\n"+
			"  sudo systemd-analyze verify %s\n"+
			"  sudo systemctl daemon-reload\n"+
			"  sudo systemctl enable %s\n"+
			"  sudo systemctl enable %s\n"+
			"  sudo systemctl enable %s\n"+
			"  sudo systemctl stop %s\n"+
			"  sudo systemctl restart %s\n"+
			"  sudo systemctl restart %s\n"+
			"  sudo systemctl restart %s\n"+
			"  sudo systemctl restart %s\n",
		len(staged), directory, strings.Join(staged, "\n  "),
		strings.Join(prefixed(directory, staged), " "), installStep,
		signerAccountName, signerAccountName, signerPreparationCommands(plan),
		riskAccountName, riskAccountName, riskPreparationCommands(plan),
		submitterAccountName, submitterAccountName, submitterPreparationCommands(plan),
		installedUnits,
		name, strings.Join(operatorSocketUnits(plan), " "), strings.Join(recoveryTimerUnits(plan), " "),
		name, strings.Join(runtimeSocketUnits(plan), " "),
		strings.Join(operatorSocketUnits(plan), " "), strings.Join(recoveryTimerUnits(plan), " "), name); err != nil {
		return err
	}
	statusSockets := statusSocketUnits(plan)
	if len(statusSockets) != 0 {
		if _, err := fmt.Fprintf(output,
			"\nEnable the read-only status sockets used by MCP and optional alerts:\n"+
				"  sudo groupadd -f --system %s\n"+
				"  sudo systemctl enable %s\n"+
				"  sudo systemctl restart %s\n",
			statusGroupName, strings.Join(statusSockets, " "), strings.Join(statusSockets, " ")); err != nil {
			return err
		}
	}
	if plan.Telegram && len(statusSockets) != 0 {
		// The account and group are the boundary that lets the bot be told what
		// happened without being able to touch a key. Creating them is the one
		// step that cannot be derived, so it is spelled out rather than assumed.
		if _, err := fmt.Fprintf(output,
			"\nThen the alerts, so trades do not execute in silence:\n"+
				// --user-group makes the account's own group; naming it with --gid
				// would require a group that does not exist yet on a fresh host.
				"  id %s >/dev/null 2>&1 || sudo useradd --system --no-create-home --user-group -G %s %s\n"+
				"  sudo install -d -o %s -g %s -m 0700 %s\n"+
				"  sudo systemctl enable %s\n"+
				"  sudo systemctl restart %s\n\n"+
				"Check delivery before you rely on it:\n"+
				"  sudo systemd-run --quiet --wait --pipe --collect \\\n"+
				"    --uid=%s --gid=%s \\\n"+
				"    -p 'EnvironmentFile=%s' \\\n"+
				"    %s test\n",
			alertsAccountName, statusGroupName, alertsAccountName,
			alertsAccountName, alertsAccountName, filepath.Dir(alertsCursorPath),
			alertsUnitName, alertsUnitName,
			alertsAccountName, alertsAccountName, alertsEnvFile,
			filepath.Join(filepath.Dir(plan.Binary), "mithril-agent-telegram")); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(output,
		"\nInstalling arms nothing. Granting spending authority stays separate:\n"+
			"  sudo env HOME=%s %s %s\n",
		plan.Home, plan.Binary, plan.ArmCommand)
	return err
}

func generatedUnitTarget(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("could not inspect the staged unit")
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("already exists and is not a regular file")
	}
	if info.Size() > 1<<20 {
		return false, errors.New("already exists and is too large to be a generated unit")
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return false, errors.New("could not read the existing staged unit")
	}
	if !bytes.Contains(existing, []byte("Description=Mithril agent")) {
		return false, errors.New("already exists and was not written by mithril-agent")
	}
	return true, nil
}

// writeGeneratedUnit publishes a complete sibling file with rename(2), so a
// path replaced after generatedUnitTarget checked it is replaced, not opened
// and followed. The caller has already validated the directory and every
// destination before the first write.
func writeGeneratedUnit(path string, data []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o644); err != nil {
		cleanup()
		return err
	}
	if _, err := io.Copy(temporary, bytes.NewReader(data)); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	return directoryFile.Sync()
}

// metricsPortsFree reports whether the runner could bind every port it needs.
//
// A free port now can be taken a moment later, so this is a check, not a
// reservation — but the failure it catches is a crash loop that lasts until
// somebody reads the journal, and the race it cannot catch is a single restart.
// At least one port is always checked, so a plan with no legs recorded yet
// still reports a collision on the base port the unit will use.
func metricsPortsFree(base, span int) error {
	if span < 1 {
		span = 1
	}
	if base < 1024 || base > 65_000 || base+span-1 > 65_535 {
		return errors.New("--metrics-base-port must leave room for every fixed strategy metrics offset")
	}
	for offset := range span {
		port := base + offset
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return fmt.Errorf(
				"%s is already in use, and the runner reserves fixed metrics offsets "+
					"from that base — it would crash-loop forever. Free that port, or "+
					"pass a clear range: mithril-agent service install --metrics-base-port %d",
				address, base+100)
		}
		if err := listener.Close(); err != nil {
			return fmt.Errorf("release the probed metrics port %s: %w", address, err)
		}
	}
	return nil
}

// metricsPortSpan includes gaps for legs that are not configured yet. A fresh
// strategy has sell and sweep but no buy; sweep still owns base+2, not base+1.
func metricsPortSpan(legs []serviceLeg) int {
	span := 1
	for _, leg := range legs {
		if offset, ok := legMetricsOffset[leg.Name]; ok && offset+1 > span {
			span = offset + 1
		}
	}
	return span
}

// statusSocketUnits names the sockets to enable. Only the sockets are enabled:
// their services are socket-activated, so enabling those too would start a
// bridge with no reader.
func statusSocketUnits(plan servicePlan) []string {
	names := make([]string, 0, len(plan.Legs))
	for _, leg := range plan.Legs {
		if leg.StatusFile != "" {
			names = append(names, leg.unit()+".socket")
		}
	}
	return names
}

func signerSocketUnits(plan servicePlan) []string {
	names := make([]string, 0, len(plan.Legs))
	for _, leg := range plan.Legs {
		names = append(names, leg.signerUnit()+".socket")
	}
	return names
}

func riskSocketUnits(plan servicePlan) []string {
	names := make([]string, 0, len(plan.Legs))
	for _, leg := range plan.Legs {
		names = append(names, leg.riskUnit()+".socket")
	}
	return names
}

func submitterSocketUnits(plan servicePlan) []string {
	names := make([]string, 0, len(plan.Legs))
	for _, leg := range plan.Legs {
		names = append(names, leg.submitterUnit()+".socket")
	}
	return names
}

func runtimeSocketUnits(plan servicePlan) []string {
	names := append(signerSocketUnits(plan), riskSocketUnits(plan)...)
	return append(names, submitterSocketUnits(plan)...)
}

func operatorSocketUnits(plan servicePlan) []string {
	names := make([]string, 0, len(plan.Legs))
	for _, leg := range plan.Legs {
		names = append(names, leg.operatorUnit()+".socket")
	}
	return names
}

func recoveryTimerUnits(plan servicePlan) []string {
	names := make([]string, 0, len(plan.Legs))
	for _, leg := range plan.Legs {
		names = append(names, leg.recoveryUnit()+".timer")
	}
	return names
}

func signerPreparationCommands(plan servicePlan) string {
	var states []string
	for _, leg := range plan.Legs {
		states = append(states, leg.SignerStateDir)
	}
	states = uniqueSorted(states)
	var commands strings.Builder
	if len(plan.SignerTraverse) != 0 {
		fmt.Fprintf(&commands, "  sudo chgrp %s %s\n",
			plan.Group, strings.Join(plan.SignerTraverse, " "))
		fmt.Fprintf(&commands, "  sudo chmod g+x %s\n", strings.Join(plan.SignerTraverse, " "))
	}
	if len(states) != 0 {
		fmt.Fprintf(&commands, "  sudo chown -R %s:%s %s\n",
			signerAccountName, signerAccountName, strings.Join(states, " "))
		fmt.Fprintf(&commands, "  sudo chmod 0700 %s\n", strings.Join(states, " "))
	}
	return commands.String()
}

func riskPreparationCommands(plan servicePlan) string {
	keys := make([]string, 0, len(plan.Legs))
	for _, leg := range plan.Legs {
		keys = append(keys, leg.RiskKeypair)
	}
	keys = uniqueSorted(keys)
	if len(keys) == 0 {
		return ""
	}
	return fmt.Sprintf("  sudo chown %s:%s %s\n  sudo chmod 0600 %s\n",
		riskAccountName, riskAccountName, strings.Join(keys, " "), strings.Join(keys, " "))
}

func submitterPreparationCommands(plan servicePlan) string {
	var controls, credentials []string
	for _, leg := range plan.Legs {
		controls = append(controls, leg.ControlStateDir)
		credentials = append(credentials, leg.SubmitterPolicy, leg.SubmitterKey)
	}
	controls = uniqueSorted(controls)
	credentials = uniqueSorted(credentials)
	if len(controls) == 0 || len(credentials) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"  sudo chown %s:%s %s\n  sudo chmod 0600 %s\n"+
			"  sudo chown -R %s:%s %s\n  sudo chmod 0700 %s\n",
		submitterAccountName, submitterAccountName,
		strings.Join(credentials, " "), strings.Join(credentials, " "),
		submitterAccountName, submitterAccountName,
		strings.Join(controls, " "), strings.Join(controls, " "))
}

func prefixed(directory string, names []string) []string {
	full := make([]string, 0, len(names))
	for _, name := range names {
		full = append(full, filepath.Join(directory, name))
	}
	return full
}

func sortedKeys(units map[string]string) []string {
	names := make([]string, 0, len(units))
	for name := range units {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// servicePlan is everything the unit needs, all of it derived from recorded
// state rather than supplied by the operator. Anything an operator has to
// retype is something they can get wrong.
type servicePlan struct {
	Binary         string
	Home           string
	User           string
	Group          string
	RunArgs        string
	StopArgs       string
	ReadOnly       []string
	ReadWrite      []string
	Inaccessible   []string
	SignerTraverse []string
	EnvFiles       []string
	ArmCommand     string
	Legs           []serviceLeg
	Telegram       bool
}

// stringList collects a repeatable flag.
type stringList []string

func (l stringList) String() string { return strings.Join(l, " ") }

func (l *stringList) Set(value string) error {
	if value == "" {
		return errors.New("an environment file path cannot be empty")
	}
	*l = append(*l, value)
	return nil
}

func buildServicePlan(metricsBasePort int) (servicePlan, error) {
	binary, err := os.Executable()
	if err != nil {
		return servicePlan{}, errors.New("cannot determine this executable's path")
	}
	// HOME is not decoration here: the strategy pointer lives under it, so a
	// unit without it starts a runner that finds no legs and does nothing —
	// the same silent-idle failure this command exists to prevent.
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return servicePlan{}, errors.New("cannot determine the home directory the runner should use")
	}

	plan := servicePlan{
		Binary: binary, Home: home, EnvFiles: defaultEnvironmentFiles, Telegram: true,
	}
	paths, unreadable := discoverStrategy()
	if len(unreadable) != 0 {
		return servicePlan{}, errors.New(
			"some recorded strategy legs cannot be read; run mithril-agent strategy show and repair them first")
	}
	if !paths.empty() {
		plan.Telegram = paths.telegram != "disabled"
		plan.RunArgs = fmt.Sprintf(
			"strategy run --interval 10s --metrics-base-port %d --quote-socket %s --signer-socket-prefix %s --risk-socket-prefix %s --submitter-socket-prefix %s",
			metricsBasePort, supervisedQuoteSocket, signerSocketPrefix, riskSocketPrefix, submitterSocketPrefix)
		plan.StopArgs = "strategy stop --submitter-socket-prefix " + submitterSocketPrefix + " --reason"
		plan.ArmCommand, err = suggestedStrategyEnableCommand(paths)
		if err != nil {
			return servicePlan{}, err
		}
		plan.ArmCommand += " --operator-socket-prefix " + operatorSocketPrefix
		for _, leg := range paths.configured() {
			if err := plan.addLeg(leg.leg, leg.path); err != nil {
				return servicePlan{}, err
			}
		}
		// The buy leg may be added after the first sell. Reserve its stable state
		// path now so adding it only requires a service restart, not a new unit.
		if paths.buy == "" && paths.sell != "" {
			strategyRoot := filepath.Dir(filepath.Dir(paths.sell))
			plan.ReadWrite = append(plan.ReadWrite,
				"-"+filepath.Join(strategyRoot, "buy", stableStateDirName))
		}
		return plan.checked()
	}

	single := discoverCurrentConfig()
	if single == "" {
		return servicePlan{}, errors.New(
			"nothing is set up yet; run mithril-agent setup first, then this command")
	}
	plan.RunArgs = fmt.Sprintf(
		"swap run --config %s --interval 10s --metrics-address 127.0.0.1:%d --quote-socket %s --signer-socket %s --risk-socket %s --submitter-socket %s",
		single, metricsBasePort, supervisedQuoteSocket, signerSocketPrefix+"agent.sock",
		riskSocketPrefix+"agent.sock", submitterSocketPrefix+"agent.sock",
	)
	plan.StopArgs = "swap stop --config " + single + " --submitter-socket " +
		submitterSocketPrefix + "agent.sock --reason"
	plan.ArmCommand = "demo --config " + single + " --operator-socket " +
		operatorSocketPrefix + "agent.sock"
	if err := plan.addLeg("agent", single); err != nil {
		return servicePlan{}, err
	}
	return plan.checked()
}

// checked refuses to hand back a plan that would run as root by omission.
// Failing to read an owner is not a reason to pick the most privileged
// identity available; it is a reason to stop and say so.
func (p servicePlan) checked() (servicePlan, error) {
	if p.User == "" {
		return servicePlan{}, errors.New(
			"cannot tell which user owns this deployment, and a unit without a user runs as root; " +
				"check that the configuration files are readable and owned by the account that should run the agent")
	}
	if p.Group == "" {
		return servicePlan{}, errors.New(
			"cannot determine the deployment group required by the isolated signer socket")
	}
	for label, value := range map[string]string{
		"agent executable": p.Binary,
		"home directory":   p.Home,
		"service user":     p.User,
		"service group":    p.Group,
	} {
		if value != "" && !systemdAtom(value) {
			return servicePlan{}, fmt.Errorf("%s contains characters that are unsafe in a systemd unit", label)
		}
	}
	for _, path := range append(append([]string{}, p.ReadOnly...), p.ReadWrite...) {
		if !systemdAtom(path) {
			return servicePlan{}, errors.New("a configured path contains characters that are unsafe in a systemd unit")
		}
	}
	for _, path := range p.Inaccessible {
		if !systemdAtom(path) {
			return servicePlan{}, errors.New("an inaccessible path is unsafe for a systemd unit")
		}
	}
	for _, path := range p.SignerTraverse {
		if !systemdAtom(path) {
			return servicePlan{}, errors.New("a signer traversal path is unsafe for an install command")
		}
	}
	for _, path := range p.EnvFiles {
		path = strings.TrimPrefix(path, "-")
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || !systemdAtom(path) {
			return servicePlan{}, errors.New("an environment file path is unsafe for a systemd unit")
		}
	}
	for _, leg := range p.Legs {
		if leg.StatusFile != "" && !systemdAtom(leg.StatusFile) {
			return servicePlan{}, errors.New("a status path contains characters that are unsafe in a systemd unit")
		}
		for _, path := range []string{
			leg.SignerPolicy, leg.SignerKeypair, leg.SignerStateDir,
			leg.RiskPolicy, leg.RiskKeypair, leg.SubmitterPolicy,
			leg.SubmitterKey, leg.ControlStateDir,
		} {
			if !systemdAtom(path) {
				return servicePlan{}, errors.New("a leg path contains characters that are unsafe in a systemd unit")
			}
		}
	}
	return p.deduplicated(), nil
}

func systemdAtom(value string) bool {
	return value != "" && strings.IndexFunc(value, func(char rune) bool {
		return unicode.IsSpace(char) || unicode.IsControl(char) ||
			strings.ContainsRune("%\\\\\"'$;&|<>`!(){}[]*?~#", char)
	}) == -1
}

// addLeg grants the narrowest write access that lets a leg run: its journal,
// while the submitter service owns control state. The configuration itself stays
// read-only, so a compromised runner cannot rewrite the profile whose caps
// bound it.
func (p *servicePlan) addLeg(name, configPath string) error {
	if !systemdAtom(configPath) {
		return errors.New("the configuration path contains characters that are unsafe in a systemd unit")
	}
	cfg, err := readConfig(configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}
	p.ReadOnly = append(p.ReadOnly, filepath.Dir(configPath))
	// A system unit with no User= runs as ROOT. This deployment's own files say
	// who it is meant to run as, so the identity is read off them rather than
	// defaulting to the most privileged answer available.
	if p.User == "" {
		if owner, group, err := fileOwner(configPath); err == nil {
			p.User, p.Group = owner, group
		}
	}
	statePath, err := stableLegStatePath(configPath, cfg)
	if err != nil {
		return err
	}
	p.ReadWrite = append(p.ReadWrite, statePath)
	controlDir := filepath.Dir(cfg.Control.StatePath)
	resolvedControl, err := filepath.EvalSymlinks(controlDir)
	if err != nil {
		return fmt.Errorf("resolve %s control state: %w", name, err)
	}
	stableControl, err := filepath.EvalSymlinks(filepath.Join(statePath, controlStateDirName))
	if err != nil || filepath.Clean(stableControl) != filepath.Clean(resolvedControl) {
		return fmt.Errorf(
			"%s control state is not isolated; re-run setup for this leg before installing services",
			name,
		)
	}
	controlTraverse, err := signerTraversalPaths(controlDir)
	if err != nil {
		return fmt.Errorf("inspect %s control state parents: %w", name, err)
	}
	var signerPolicy signer.Policy
	if err := readStrictJSON(cfg.Signer.PolicyPath, &signerPolicy); err != nil {
		return fmt.Errorf("read %s signer policy: %w", name, err)
	}
	signerState := filepath.Dir(signerPolicy.AuthorizationLedgerPath)
	resolvedSigner, err := filepath.EvalSymlinks(signerState)
	if err != nil {
		return fmt.Errorf("resolve %s signer state: %w", name, err)
	}
	stableSigner, err := filepath.EvalSymlinks(filepath.Join(statePath, signerStateDirName))
	if err != nil || filepath.Clean(stableSigner) != filepath.Clean(resolvedSigner) {
		return fmt.Errorf(
			"%s signer ledger is not isolated; re-run setup for this leg before installing services",
			name,
		)
	}
	traverse, err := signerTraversalPaths(signerState)
	if err != nil {
		return fmt.Errorf("inspect %s signer state parents: %w", name, err)
	}
	leg := serviceLeg{
		Name: name, SignerPolicy: cfg.Signer.PolicyPath,
		SignerKeypair: cfg.Signer.KeypairPath, SignerStateDir: signerState,
		RiskPolicy: cfg.Policy.PolicyPath, RiskKeypair: cfg.Policy.KeypairPath,
		SubmitterPolicy: cfg.Submitter.PolicyPath,
		SubmitterKey:    cfg.Submitter.PrivateKeyPath,
		ControlStateDir: controlDir,
	}
	// The status file is the runner's heartbeat and the only thing the alert
	// path reads. A leg whose journal is unset publishes nothing, so it gets no
	// socket rather than one that would serve a file that never appears.
	if cfg.Journal.Path != "" {
		leg.StatusFile = cfg.Journal.Path + ".status.json"
	}
	p.Legs = append(p.Legs, leg)
	p.Inaccessible = append(
		p.Inaccessible, cfg.Signer.KeypairPath, cfg.Policy.KeypairPath, signerState,
		cfg.Submitter.PolicyPath, cfg.Submitter.PrivateKeyPath, controlDir,
	)
	p.SignerTraverse = append(p.SignerTraverse, traverse...)
	p.SignerTraverse = append(p.SignerTraverse, controlTraverse...)
	return nil
}

// signerTraversalPaths returns the private ancestors that need group execute
// permission before the isolated signer can reach its own ledger. It stops at
// the first world-traversable directory because every ancestor above it is
// already reachable without widening another private directory.
func signerTraversalPaths(state string) ([]string, error) {
	var paths []string
	for path := filepath.Dir(filepath.Clean(state)); ; path = filepath.Dir(path) {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errors.New("signer state parent is not a directory")
		}
		if info.Mode().Perm()&0o001 != 0 {
			return paths, nil
		}
		paths = append(paths, path)
		if parent := filepath.Dir(path); parent == path {
			return nil, errors.New("signer state has no traversable ancestor")
		}
	}
}

// stableLegStatePath verifies that the leg's stable state path resolves to the
// directory containing runner-owned mutable files. The unit can then survive a
// safe
// setup replacement without granting write access to the profile directory.
func stableLegStatePath(configPath string, cfg config) (string, error) {
	stable := filepath.Join(filepath.Dir(configPath), stableStateDirName)
	resolvedStable, err := filepath.EvalSymlinks(stable)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("resolve the stable state path for %s: %w", configPath, err)
		}
		// Older setups stored mutable files beside config.json. Keep them
		// runnable; the next setup moves them under the stable state path.
		var legacy string
		for _, writable := range []string{cfg.Journal.Path} {
			if writable == "" {
				continue
			}
			if !filepath.IsAbs(writable) {
				return "", fmt.Errorf("%s records a relative path; re-run setup for this leg", configPath)
			}
			directory := filepath.Dir(writable)
			if legacy == "" {
				legacy = directory
			} else if directory != legacy {
				return "", fmt.Errorf("%s records mutable files in different directories", configPath)
			}
		}
		if legacy == "" {
			return "", fmt.Errorf("%s records no mutable state", configPath)
		}
		return legacy, nil
	}
	resolvedStable = filepath.Clean(resolvedStable)
	for _, writable := range []string{cfg.Journal.Path} {
		if writable == "" {
			continue
		}
		if !filepath.IsAbs(writable) {
			return "", fmt.Errorf("%s records a relative path; re-run setup for this leg", configPath)
		}
		resolvedWritable, err := filepath.EvalSymlinks(filepath.Dir(writable))
		if err != nil {
			return "", fmt.Errorf("resolve state recorded by %s: %w", configPath, err)
		}
		if filepath.Clean(resolvedWritable) != resolvedStable {
			return "", fmt.Errorf(
				"%s records mutable state outside its stable state directory; re-run setup for this leg",
				configPath)
		}
	}
	return stable, nil
}

// deduplicated collapses the paths and orders them, so the same deployment
// always renders the same unit. A unit whose text shuffles between runs is one
// nobody can diff, and a diff is how you check what changed before restarting
// something that spends money.
func (p servicePlan) deduplicated() servicePlan {
	p.ReadOnly = uniqueSorted(p.ReadOnly)
	p.ReadWrite = uniqueSorted(p.ReadWrite)
	p.Inaccessible = uniqueSorted(p.Inaccessible)
	p.SignerTraverse = uniqueSorted(p.SignerTraverse)
	// A path that must be writable cannot also be listed read-only: systemd
	// applies ReadOnlyPaths and the runner would fail to record what it did.
	writable := make(map[string]bool, len(p.ReadWrite))
	for _, path := range p.ReadWrite {
		writable[path] = true
	}
	kept := p.ReadOnly[:0]
	for _, path := range p.ReadOnly {
		if !writable[path] {
			kept = append(kept, path)
		}
	}
	p.ReadOnly = kept
	return p
}

// fileOwner names the user and group that own a path. The runner must run as
// whoever owns the state it writes: any other identity either cannot write it
// or is more privileged than the deployment intended.
func fileOwner(path string) (string, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", "", errors.New("this platform does not report file ownership")
	}
	owner, err := user.LookupId(fmt.Sprint(stat.Uid))
	if err != nil {
		return "", "", err
	}
	group, err := user.LookupGroupId(fmt.Sprint(stat.Gid))
	if err != nil {
		return owner.Username, "", nil
	}
	return owner.Username, group.Name, nil
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]bool, len(values))
	var unique []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}

// renderServiceUnit keeps the confinement of the hand-written units that
// preceded it rather than inventing a weaker one: no capabilities, a read-only
// system, and write access only to the state each leg records into.
func renderServiceUnit(plan servicePlan) string {
	var unit strings.Builder
	write := func(format string, args ...any) {
		fmt.Fprintf(&unit, format, args...)
	}
	write("[Unit]\nDescription=Mithril agent runner\n")
	write("After=network-online.target\nWants=network-online.target\n")
	sockets := runtimeSocketUnits(plan)
	if len(sockets) != 0 {
		write("After=%s\nRequires=%s\n", strings.Join(sockets, " "), strings.Join(sockets, " "))
	}
	// systemd gives up permanently after 5 starts in 10s by default. For a
	// runner that holds spending authority, "gave up" is the worst outcome
	// available: the grant stays on disk, nothing executes against it, and the
	// supervisor that was supposed to prevent exactly that has stopped trying.
	//
	// This key belongs in [Unit]. Putting it in [Service] is silently ignored —
	// systemd-analyze reports "Unknown key name" and the unit still loads — so
	// a unit that looks like it survives restarts does not. That is not
	// hypothetical: a hand-written unit on a real host had it in the wrong
	// section, with a comment claiming the property it did not have.
	write("StartLimitIntervalSec=0\n\n")
	write("[Service]\nType=simple\nUMask=0077\nKillMode=control-group\n")
	if plan.User != "" {
		write("User=%s\n", plan.User)
	}
	if plan.Group != "" {
		write("Group=%s\n", plan.Group)
	}
	if plan.User == "mithril-agent" {
		write("SupplementaryGroups=%s\n", nodeStateGroupName)
	}
	write("Environment=HOME=%s\n", plan.Home)
	for _, file := range plan.EnvFiles {
		write("EnvironmentFile=%s\n", file)
	}
	write("ExecStartPre=%s %s service_start\n", plan.Binary, plan.StopArgs)
	write("ExecStart=%s %s\n\n", plan.Binary, plan.RunArgs)
	write("ExecStopPost=%s %s service_stop\n", plan.Binary, plan.StopArgs)
	write("Restart=on-failure\nRestartSec=10s\n")
	// Long enough to finish reconciling a trade that is already in flight.
	// Killing the runner mid-submission is how a trade becomes unaccounted for.
	write("TimeoutStopSec=17min\n\n")
	write("NoNewPrivileges=yes\nCapabilityBoundingSet=\nAmbientCapabilities=\n")
	write("PrivateDevices=yes\nPrivateTmp=yes\n")
	write("ProtectControlGroups=yes\nProtectHome=read-only\nProtectHostname=yes\n")
	write("ProtectKernelLogs=yes\nProtectKernelModules=yes\nProtectKernelTunables=yes\n")
	// The clock gate reads /proc/sys/kernel/random/boot_id. ProcSubset=pid
	// removes /proc/sys entirely and makes every supervised preflight fail even
	// on a synchronized host. ProtectProc still hides other users' processes.
	write("ProtectProc=invisible\nProtectSystem=strict\n")
	if len(plan.ReadOnly) != 0 {
		write("ReadOnlyPaths=%s\n", strings.Join(plan.ReadOnly, " "))
	}
	if len(plan.ReadWrite) != 0 {
		write("ReadWritePaths=%s\n", strings.Join(plan.ReadWrite, " "))
	}
	if len(plan.Inaccessible) != 0 {
		write("InaccessiblePaths=%s\n", strings.Join(plan.Inaccessible, " "))
	}
	write("RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6\n")
	write("RemoveIPC=yes\nRestrictNamespaces=yes\nRestrictRealtime=yes\nRestrictSUIDSGID=yes\n")
	write("LockPersonality=yes\nSystemCallArchitectures=native\n")
	write("MemoryDenyWriteExecute=yes\n\n")
	write("[Install]\nWantedBy=multi-user.target\n")
	return unit.String()
}

func renderSignerSocket(plan servicePlan, leg serviceLeg) string {
	var unit strings.Builder
	fmt.Fprintf(&unit, "[Unit]\nDescription=Mithril agent %s isolated signer socket\n\n", leg.Name)
	fmt.Fprintf(&unit, "[Socket]\nListenStream=%s\n", leg.signerSocket())
	fmt.Fprintf(&unit, "SocketUser=%s\nSocketGroup=%s\nSocketMode=0660\n", plan.User, plan.Group)
	// With Accept=yes, systemd requires the matching <socket-name>@.service
	// template and rejects Service= entirely.
	unit.WriteString("Accept=yes\nBacklog=8\nMaxConnections=8\nMaxConnectionsPerSource=2\n\n")
	unit.WriteString("[Install]\nWantedBy=sockets.target\n")
	return unit.String()
}

func renderSignerService(plan servicePlan, leg serviceLeg) string {
	var unit strings.Builder
	fmt.Fprintf(&unit, "[Unit]\nDescription=Mithril agent %s isolated signer request\n", leg.Name)
	fmt.Fprintf(&unit, "Requires=%s.socket\nCollectMode=inactive-or-failed\n\n", leg.signerUnit())
	unit.WriteString("[Service]\nType=exec\nUMask=0077\n")
	fmt.Fprintf(&unit, "User=%s\nGroup=%s\nSupplementaryGroups=%s\n",
		signerAccountName, signerAccountName, plan.Group)
	fmt.Fprintf(&unit, "LoadCredential=signer-policy:%s\n", leg.SignerPolicy)
	fmt.Fprintf(&unit, "LoadCredential=signer-key:%s\n", leg.SignerKeypair)
	fmt.Fprintf(&unit, "ExecStart=%s --policy %%d/signer-policy --keypair %%d/signer-key --socket\n",
		filepath.Join(filepath.Dir(plan.Binary), "mithril-agent-signer"))
	unit.WriteString("StandardInput=socket\nStandardOutput=socket\nStandardError=journal\n")
	unit.WriteString("RuntimeMaxSec=30s\nTimeoutStopSec=5s\n")
	unit.WriteString("NoNewPrivileges=yes\nCapabilityBoundingSet=\nAmbientCapabilities=\n")
	unit.WriteString("PrivateDevices=yes\nPrivateMounts=yes\nPrivateNetwork=yes\nPrivateTmp=yes\n")
	unit.WriteString("ProtectControlGroups=yes\nProtectHome=read-only\nProtectHostname=yes\n")
	unit.WriteString("ProtectKernelLogs=yes\nProtectKernelModules=yes\nProtectKernelTunables=yes\n")
	unit.WriteString("ProtectProc=invisible\nProtectSystem=strict\n")
	fmt.Fprintf(&unit, "ReadWritePaths=%s\n", leg.SignerStateDir)
	unit.WriteString("RestrictAddressFamilies=AF_UNIX\nRemoveIPC=yes\nRestrictNamespaces=yes\n")
	unit.WriteString("RestrictRealtime=yes\nRestrictSUIDSGID=yes\nLockPersonality=yes\n")
	unit.WriteString("SystemCallArchitectures=native\nMemoryDenyWriteExecute=yes\n")
	unit.WriteString("MemoryMax=128M\nMemorySwapMax=0\nTasksMax=8\nLimitNOFILE=32\n")
	return unit.String()
}

func renderRiskSocket(plan servicePlan, leg serviceLeg) string {
	var unit strings.Builder
	fmt.Fprintf(&unit, "[Unit]\nDescription=Mithril agent %s isolated risk authority socket\n\n", leg.Name)
	fmt.Fprintf(&unit, "[Socket]\nListenStream=%s\n", leg.riskSocket())
	fmt.Fprintf(&unit, "SocketUser=%s\nSocketGroup=%s\nSocketMode=0660\n", plan.User, plan.Group)
	unit.WriteString("Accept=yes\nBacklog=8\nMaxConnections=8\nMaxConnectionsPerSource=2\n\n")
	unit.WriteString("[Install]\nWantedBy=sockets.target\n")
	return unit.String()
}

func renderRiskService(plan servicePlan, leg serviceLeg) string {
	var unit strings.Builder
	fmt.Fprintf(&unit, "[Unit]\nDescription=Mithril agent %s isolated risk authority request\n", leg.Name)
	fmt.Fprintf(&unit, "Requires=%s.socket\nCollectMode=inactive-or-failed\n\n", leg.riskUnit())
	unit.WriteString("[Service]\nType=exec\nUMask=0077\n")
	fmt.Fprintf(&unit, "User=%s\nGroup=%s\n", riskAccountName, riskAccountName)
	fmt.Fprintf(&unit, "LoadCredential=risk-policy:%s\n", leg.RiskPolicy)
	fmt.Fprintf(&unit, "LoadCredential=risk-key:%s\n", leg.RiskKeypair)
	fmt.Fprintf(&unit, "ExecStart=%s --policy %%d/risk-policy --keypair %%d/risk-key --socket\n",
		filepath.Join(filepath.Dir(plan.Binary), "mithril-agent-policy"))
	unit.WriteString("StandardInput=socket\nStandardOutput=socket\nStandardError=journal\n")
	unit.WriteString("RuntimeMaxSec=30s\nTimeoutStopSec=5s\n")
	unit.WriteString("NoNewPrivileges=yes\nCapabilityBoundingSet=\nAmbientCapabilities=\n")
	unit.WriteString("PrivateDevices=yes\nPrivateMounts=yes\nPrivateNetwork=yes\nPrivateTmp=yes\n")
	unit.WriteString("ProtectControlGroups=yes\nProtectHome=yes\nProtectHostname=yes\n")
	unit.WriteString("ProtectKernelLogs=yes\nProtectKernelModules=yes\nProtectKernelTunables=yes\n")
	unit.WriteString("ProtectProc=invisible\nProtectSystem=strict\n")
	unit.WriteString("RestrictAddressFamilies=AF_UNIX\nRemoveIPC=yes\nRestrictNamespaces=yes\n")
	unit.WriteString("RestrictRealtime=yes\nRestrictSUIDSGID=yes\nLockPersonality=yes\n")
	unit.WriteString("SystemCallArchitectures=native\nMemoryDenyWriteExecute=yes\n")
	unit.WriteString("MemoryMax=64M\nMemorySwapMax=0\nTasksMax=8\nLimitNOFILE=32\n")
	return unit.String()
}

func renderSubmitterSocket(plan servicePlan, leg serviceLeg) string {
	var unit strings.Builder
	fmt.Fprintf(&unit, "[Unit]\nDescription=Mithril agent %s isolated submitter socket\n\n", leg.Name)
	fmt.Fprintf(&unit, "[Socket]\nListenStream=%s\n", leg.submitterSocket())
	fmt.Fprintf(&unit, "SocketUser=%s\nSocketGroup=%s\nSocketMode=0660\n", plan.User, plan.Group)
	unit.WriteString("Accept=yes\nBacklog=8\nMaxConnections=8\nMaxConnectionsPerSource=2\n\n")
	unit.WriteString("[Install]\nWantedBy=sockets.target\n")
	return unit.String()
}

func renderSubmitterService(plan servicePlan, leg serviceLeg) string {
	var unit strings.Builder
	fmt.Fprintf(&unit, "[Unit]\nDescription=Mithril agent %s isolated submitter request\n", leg.Name)
	fmt.Fprintf(&unit, "Requires=%s.socket\nCollectMode=inactive-or-failed\n\n", leg.submitterUnit())
	unit.WriteString("[Service]\nType=exec\nUMask=0077\n")
	fmt.Fprintf(&unit, "User=%s\nGroup=%s\nSupplementaryGroups=%s\n",
		submitterAccountName, submitterAccountName, plan.Group)
	fmt.Fprintf(&unit, "LoadCredential=submitter-policy:%s\n", leg.SubmitterPolicy)
	fmt.Fprintf(&unit, "LoadCredential=submitter-key:%s\n", leg.SubmitterKey)
	unit.WriteString("EnvironmentFile=/etc/mithril-agent/rpc.env\n")
	fmt.Fprintf(&unit, "ExecStart=%s --policy %%d/submitter-policy --key %%d/submitter-key --socket\n",
		filepath.Join(filepath.Dir(plan.Binary), "mithril-agent-submitter"))
	unit.WriteString("StandardInput=socket\nStandardOutput=socket\nStandardError=journal\n")
	unit.WriteString("RuntimeMaxSec=15min\nTimeoutStopSec=5s\n")
	unit.WriteString("NoNewPrivileges=yes\nCapabilityBoundingSet=\nAmbientCapabilities=\n")
	unit.WriteString("PrivateDevices=yes\nPrivateMounts=yes\nPrivateTmp=yes\n")
	unit.WriteString("ProtectControlGroups=yes\nProtectHome=yes\nProtectHostname=yes\n")
	unit.WriteString("ProtectKernelLogs=yes\nProtectKernelModules=yes\nProtectKernelTunables=yes\n")
	unit.WriteString("ProtectProc=invisible\nProtectSystem=strict\n")
	fmt.Fprintf(&unit, "ReadWritePaths=%s\nInaccessiblePaths=/proc %s\n",
		leg.ControlStateDir, leg.SubmitterKey)
	unit.WriteString("IPAddressDeny=any\nIPAddressAllow=127.0.0.0/8\nIPAddressAllow=::1/128\n")
	unit.WriteString("RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6\nRemoveIPC=yes\nRestrictNamespaces=yes\n")
	unit.WriteString("RestrictRealtime=yes\nRestrictSUIDSGID=yes\nLockPersonality=yes\n")
	unit.WriteString("SystemCallArchitectures=native\nMemoryDenyWriteExecute=yes\n")
	unit.WriteString("MemoryMax=128M\nMemorySwapMax=0\nTasksMax=16\nLimitNOFILE=64\n")
	return unit.String()
}

func renderOperatorSocket(leg serviceLeg) string {
	var unit strings.Builder
	fmt.Fprintf(&unit, "[Unit]\nDescription=Mithril agent %s root operator socket\n\n", leg.Name)
	fmt.Fprintf(&unit, "[Socket]\nListenStream=%s\n", leg.operatorSocket())
	unit.WriteString("SocketUser=root\nSocketGroup=root\nSocketMode=0600\n")
	unit.WriteString("Accept=yes\nBacklog=4\nMaxConnections=4\nMaxConnectionsPerSource=1\n\n")
	unit.WriteString("[Install]\nWantedBy=sockets.target\n")
	return unit.String()
}

func renderOperatorService(plan servicePlan, leg serviceLeg) string {
	var unit strings.Builder
	fmt.Fprintf(&unit, "[Unit]\nDescription=Mithril agent %s root operator request\n", leg.Name)
	fmt.Fprintf(&unit, "Requires=%s.socket\nCollectMode=inactive-or-failed\n\n", leg.operatorUnit())
	unit.WriteString("[Service]\nType=exec\nUMask=0077\n")
	fmt.Fprintf(&unit, "User=%s\nGroup=%s\n", submitterAccountName, submitterAccountName)
	fmt.Fprintf(&unit, "LoadCredential=submitter-policy:%s\n", leg.SubmitterPolicy)
	fmt.Fprintf(&unit, "ExecStart=%s --policy %%d/submitter-policy --operator-socket\n",
		filepath.Join(filepath.Dir(plan.Binary), "mithril-agent-submitter"))
	unit.WriteString("StandardInput=socket\nStandardOutput=socket\nStandardError=journal\n")
	unit.WriteString("RuntimeMaxSec=30s\nTimeoutStopSec=5s\n")
	unit.WriteString("NoNewPrivileges=yes\nCapabilityBoundingSet=\nAmbientCapabilities=\n")
	unit.WriteString("PrivateDevices=yes\nPrivateMounts=yes\nPrivateNetwork=yes\nPrivateTmp=yes\n")
	unit.WriteString("ProtectControlGroups=yes\nProtectHome=yes\nProtectHostname=yes\n")
	unit.WriteString("ProtectKernelLogs=yes\nProtectKernelModules=yes\nProtectKernelTunables=yes\n")
	unit.WriteString("ProtectProc=invisible\nProtectSystem=strict\n")
	// This keyless service shares the submitter's UID, so hiding only the source
	// key path would still leave /proc/<submitter>/root as an alias to that key.
	fmt.Fprintf(&unit, "ReadWritePaths=%s\nInaccessiblePaths=/proc %s\n",
		leg.ControlStateDir, leg.SubmitterKey)
	unit.WriteString("RestrictAddressFamilies=AF_UNIX\nRemoveIPC=yes\nRestrictNamespaces=yes\n")
	unit.WriteString("RestrictRealtime=yes\nRestrictSUIDSGID=yes\nLockPersonality=yes\n")
	unit.WriteString("SystemCallArchitectures=native\nMemoryDenyWriteExecute=yes\n")
	unit.WriteString("MemoryMax=64M\nMemorySwapMax=0\nTasksMax=8\nLimitNOFILE=32\n")
	return unit.String()
}

func renderRecoveryService(plan servicePlan, leg serviceLeg) string {
	var unit strings.Builder
	fmt.Fprintf(&unit, "[Unit]\nDescription=Mithril agent %s independent recovery check\n", leg.Name)
	unit.WriteString("After=network-online.target\nWants=network-online.target\n\n")
	unit.WriteString("[Service]\nType=oneshot\nUMask=0077\n")
	fmt.Fprintf(&unit, "User=%s\nGroup=%s\n", submitterAccountName, submitterAccountName)
	fmt.Fprintf(&unit, "LoadCredential=submitter-policy:%s\n", leg.SubmitterPolicy)
	unit.WriteString("EnvironmentFile=/etc/mithril-agent/rpc.env\n")
	fmt.Fprintf(&unit, "ExecStart=%s --policy %%d/submitter-policy --recover\n",
		filepath.Join(filepath.Dir(plan.Binary), "mithril-agent-submitter"))
	unit.WriteString("TimeoutStartSec=15min\nNoNewPrivileges=yes\nCapabilityBoundingSet=\nAmbientCapabilities=\n")
	unit.WriteString("PrivateDevices=yes\nPrivateMounts=yes\nPrivateTmp=yes\nProtectControlGroups=yes\nProtectHome=yes\n")
	unit.WriteString("ProtectClock=yes\nProtectHostname=yes\nProtectKernelLogs=yes\nProtectKernelModules=yes\nProtectKernelTunables=yes\n")
	unit.WriteString("ProtectProc=invisible\nProtectSystem=strict\n")
	// This process intentionally shares the submitter's UID so it can atomically
	// clear durable recovery state. Hiding only the key path is insufficient:
	// the same UID can otherwise reopen it through /proc/<submitter>/root while
	// a socket-activated submitter is alive. Recovery needs no process metadata,
	// so remove /proc from its mount namespace as well as the direct key path.
	fmt.Fprintf(&unit, "ReadWritePaths=%s\nInaccessiblePaths=/proc %s\n",
		leg.ControlStateDir, leg.SubmitterKey)
	unit.WriteString("IPAddressDeny=127.0.0.0/8\nIPAddressDeny=::1/128\n")
	unit.WriteString("IPAddressDeny=10.0.0.0/8\nIPAddressDeny=172.16.0.0/12\n")
	unit.WriteString("IPAddressDeny=192.168.0.0/16\nIPAddressDeny=169.254.0.0/16\n")
	unit.WriteString("IPAddressDeny=100.64.0.0/10\nIPAddressDeny=198.18.0.0/15\n")
	unit.WriteString("IPAddressDeny=224.0.0.0/4\nIPAddressDeny=240.0.0.0/4\n")
	unit.WriteString("IPAddressDeny=fc00::/7\nIPAddressDeny=fe80::/10\n")
	unit.WriteString("IPAddressDeny=ff00::/8\nIPAddressDeny=fec0::/10\n")
	unit.WriteString("IPAddressAllow=127.0.0.53/32\nIPAddressAllow=127.0.0.54/32\n")
	unit.WriteString("RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6\nRemoveIPC=yes\nRestrictNamespaces=yes\n")
	unit.WriteString("RestrictRealtime=yes\nRestrictSUIDSGID=yes\nLockPersonality=yes\n")
	unit.WriteString("SystemCallArchitectures=native\nMemoryDenyWriteExecute=yes\n")
	unit.WriteString("MemoryMax=128M\nMemorySwapMax=0\nTasksMax=16\nLimitNOFILE=64\n")
	return unit.String()
}

func renderRecoveryTimer(leg serviceLeg) string {
	var unit strings.Builder
	fmt.Fprintf(&unit, "[Unit]\nDescription=Mithril agent %s independent recovery schedule\n\n", leg.Name)
	unit.WriteString("[Timer]\nOnBootSec=10s\nOnUnitInactiveSec=10s\nAccuracySec=1s\n")
	fmt.Fprintf(&unit, "Unit=%s.service\n\n", leg.recoveryUnit())
	unit.WriteString("[Install]\nWantedBy=timers.target\n")
	return unit.String()
}

// renderStatusSocket publishes one leg's status on a loopback UNIX socket. The
// socket is owned by the account that runs the agent and readable by the alert
// group, so the bot can be told what happened without being able to touch a
// key, a profile, or a journal.
func renderStatusSocket(plan servicePlan, leg serviceLeg) string {
	var unit strings.Builder
	fmt.Fprintf(&unit, "[Unit]\nDescription=Mithril agent %s status socket\n\n", leg.Name)
	fmt.Fprintf(&unit, "[Socket]\nListenStream=%s\n", leg.socket())
	fmt.Fprintf(&unit, "SocketUser=%s\nSocketGroup=%s\nSocketMode=0660\n", plan.User, statusGroupName)
	fmt.Fprintf(&unit, "FileDescriptorName=%s\nService=%s.service\n", statusCredential, leg.unit())
	// Accept=no hands the listening socket to one short-lived reader. FlushPending
	// keeps a stale queued connection from being answered with an old status.
	unit.WriteString("Accept=no\nBacklog=8\nFlushPending=no\n\n")
	unit.WriteString("[Install]\nWantedBy=sockets.target\n")
	return unit.String()
}

// renderStatusService serves exactly one status file, passed in by systemd as a
// credential. The bridge never opens a path of its own, so a compromised bridge
// cannot read a second leg — or anything else.
func renderStatusService(plan servicePlan, leg serviceLeg) string {
	var unit strings.Builder
	fmt.Fprintf(&unit, "[Unit]\nDescription=Mithril agent %s status bridge\n", leg.Name)
	fmt.Fprintf(&unit, "Requires=%s.socket\n", leg.unit())
	// The alert bot can connect before the runner has written its first status
	// snapshot. Loading that missing file as a credential fails before the
	// bridge starts; retry until the runner creates it instead of leaving the
	// socket permanently failed on a fresh install.
	unit.WriteString("StartLimitIntervalSec=0\n\n")
	unit.WriteString("[Service]\nType=simple\nDynamicUser=yes\nUMask=0077\n")
	fmt.Fprintf(&unit, "LoadCredential=%s:%s\n", statusCredential, leg.StatusFile)
	fmt.Fprintf(&unit, "ExecStart=%s --credential %s\n",
		filepath.Join(filepath.Dir(plan.Binary), "mithril-agent-status-bridge"), statusCredential)
	fmt.Fprintf(&unit, "InaccessiblePaths=%s\n", filepath.Dir(leg.StatusFile))
	// One read, then gone. A bridge that lingers is a second long-lived process
	// holding the agent's status open for no reason.
	unit.WriteString("RuntimeMaxSec=5s\n")
	unit.WriteString("Restart=on-failure\nRestartSec=1s\n")
	unit.WriteString("NoNewPrivileges=yes\nCapabilityBoundingSet=\nAmbientCapabilities=\n")
	unit.WriteString("PrivateDevices=yes\nPrivateMounts=yes\nPrivateNetwork=yes\nPrivateTmp=yes\n")
	unit.WriteString("ProtectHome=yes\nProtectSystem=strict\n")
	unit.WriteString("ProtectKernelLogs=yes\nProtectKernelModules=yes\nProtectKernelTunables=yes\n")
	unit.WriteString("MemoryDenyWriteExecute=yes\nRestrictAddressFamilies=AF_UNIX\n")
	unit.WriteString("RestrictNamespaces=yes\nRestrictSUIDSGID=yes\n")
	return unit.String()
}

// renderAlertsUnit is the bot itself. It runs as its own account so the bot
// token is not readable by the agent, and it reaches every leg through the
// read-only sockets above — it is given no config, no key, and no journal.
func renderAlertsUnit(plan servicePlan) string {
	var unit strings.Builder
	unit.WriteString("[Unit]\nDescription=Mithril agent Telegram alerts\n")
	unit.WriteString("After=network-online.target\nWants=network-online.target\n")
	unit.WriteString("StartLimitIntervalSec=0\n\n")
	unit.WriteString("[Service]\nType=simple\n")
	fmt.Fprintf(&unit, "User=%s\nGroup=%s\n", alertsAccountName, alertsAccountName)
	fmt.Fprintf(&unit, "SupplementaryGroups=%s\n", statusGroupName)
	fmt.Fprintf(&unit, "EnvironmentFile=%s\n", alertsEnvFile)
	fmt.Fprintf(&unit, "ExecStart=%s", filepath.Join(filepath.Dir(plan.Binary), "mithril-agent-telegram"))
	for _, leg := range plan.Legs {
		if leg.StatusFile != "" {
			fmt.Fprintf(&unit, " \\\n  --status-socket %s", leg.socket())
		}
	}
	fmt.Fprintf(&unit, " \\\n  --cursor %s\n", alertsCursorPath)
	unit.WriteString("Restart=on-failure\nRestartSec=10s\n")
	unit.WriteString("NoNewPrivileges=yes\nCapabilityBoundingSet=\nAmbientCapabilities=\n")
	unit.WriteString("PrivateDevices=yes\nPrivateTmp=yes\nProtectHome=yes\nProtectSystem=strict\n")
	unit.WriteString("ProtectProc=invisible\n")
	unit.WriteString("InaccessiblePaths=-/var/lib/mithril-agent -/etc/mithril-agent\n")
	fmt.Fprintf(&unit, "ReadWritePaths=%s\n", filepath.Dir(alertsCursorPath))
	unit.WriteString("ProtectKernelLogs=yes\nProtectKernelModules=yes\nProtectKernelTunables=yes\n")
	unit.WriteString("IPAddressDeny=127.0.0.0/8\nIPAddressDeny=::1/128\n")
	unit.WriteString("IPAddressDeny=10.0.0.0/8\nIPAddressDeny=172.16.0.0/12\n")
	unit.WriteString("IPAddressDeny=192.168.0.0/16\nIPAddressDeny=169.254.0.0/16\n")
	unit.WriteString("IPAddressDeny=fc00::/7\nIPAddressDeny=fe80::/10\n")
	unit.WriteString("IPAddressAllow=127.0.0.2/32\nIPAddressAllow=127.0.0.53/32\n")
	unit.WriteString("IPAddressAllow=127.0.0.54/32\n")
	unit.WriteString("RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6\n")
	unit.WriteString("RestrictNamespaces=yes\nRestrictRealtime=yes\nRestrictSUIDSGID=yes\n")
	unit.WriteString("LockPersonality=yes\nMemoryDenyWriteExecute=yes\nMemoryMax=128M\nMemorySwapMax=0\n")
	unit.WriteString("SystemCallArchitectures=native\nTasksMax=32\nLimitNOFILE=64\n\n")
	unit.WriteString("[Install]\nWantedBy=multi-user.target\n")
	return unit.String()
}

// supportUnits names every generated authority and status unit, plus the
// optional Telegram process.
func supportUnits(plan servicePlan) map[string]string {
	units := make(map[string]string, 10*len(plan.Legs)+1)
	for _, leg := range plan.Legs {
		units[leg.signerUnit()+".socket"] = renderSignerSocket(plan, leg)
		units[leg.signerUnit()+"@.service"] = renderSignerService(plan, leg)
		units[leg.riskUnit()+".socket"] = renderRiskSocket(plan, leg)
		units[leg.riskUnit()+"@.service"] = renderRiskService(plan, leg)
		units[leg.submitterUnit()+".socket"] = renderSubmitterSocket(plan, leg)
		units[leg.submitterUnit()+"@.service"] = renderSubmitterService(plan, leg)
		units[leg.operatorUnit()+".socket"] = renderOperatorSocket(leg)
		units[leg.operatorUnit()+"@.service"] = renderOperatorService(plan, leg)
		units[leg.recoveryUnit()+".service"] = renderRecoveryService(plan, leg)
		units[leg.recoveryUnit()+".timer"] = renderRecoveryTimer(leg)
		if leg.StatusFile != "" {
			units[leg.unit()+".socket"] = renderStatusSocket(plan, leg)
			units[leg.unit()+".service"] = renderStatusService(plan, leg)
		}
	}
	if plan.Telegram && len(statusSocketUnits(plan)) != 0 {
		units[alertsUnitName] = renderAlertsUnit(plan)
	}
	return units
}
