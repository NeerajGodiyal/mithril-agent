package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// setup used to demand four absolute host paths — the wallet keypair, the
// Mithril executable, Node.js, and the Orca quote adapter. Every one of them is
// already knowable, and requiring them made the first run a guessing game with
// cryptic failures for each wrong guess:
//
//	agent account: lstat /var/lib/mithril-agent/agent-account.json: no such file
//
// That is four chances to get it wrong before anything happens, and it is the
// single largest barrier between a stranger and a working agent.
//
// Two sources answer all four, in this order:
//
//  1. A configuration this host already has. If a leg exists, it RECORDS the
//     exact commands and keypair that worked. Nothing beats a value that has
//     already run successfully on this machine.
//  2. Files installed beside this executable. A packaged deployment puts the
//     agent, the node runtime and the quote adapter in one directory, so a
//     sibling lookup finds them on a fresh host with no configuration at all.
//
// A path is only ever adopted if it EXISTS. Guessing a path that is not there
// converts a clear "tell me where X is" into a confusing failure deeper in, and
// the whole point of this file is to remove confusing failures.
type hostPaths struct {
	walletKeypair  string
	mithrilCommand string
	nodeCommand    string
	quoteScript    string
}

// conventional names, as installed beside the agent binary.
const (
	siblingMithrilCommand = "mithril"
	siblingNodeCommand    = "node"
	siblingQuoteScript    = "quote.mjs"
)

// resolveHostPaths fills whatever the operator did not type. Values they DID
// type always win: someone naming a path explicitly is answering a question,
// and silently overriding that answer would be worse than asking.
func resolveHostPaths(given hostPaths) hostPaths {
	resolved := given
	fromConfig := hostPathsFromExistingConfig()
	beside := hostPathsBesideExecutable()

	for _, field := range []struct {
		target             *string
		recorded, neighbor string
	}{
		{&resolved.walletKeypair, fromConfig.walletKeypair, ""},
		{&resolved.mithrilCommand, fromConfig.mithrilCommand, beside.mithrilCommand},
		{&resolved.nodeCommand, fromConfig.nodeCommand, beside.nodeCommand},
		{&resolved.quoteScript, fromConfig.quoteScript, beside.quoteScript},
	} {
		if *field.target != "" {
			continue
		}
		if usableFile(field.recorded) {
			*field.target = field.recorded
			continue
		}
		if usableFile(field.neighbor) {
			*field.target = field.neighbor
		}
	}
	return resolved
}

// describeResolvedHostPaths lists what was found rather than typed, so the
// operator can see what setup decided before it acts on their money. A tool
// that fills things in silently is a tool nobody can check.
func describeResolvedHostPaths(given, resolved hostPaths) []string {
	var lines []string
	for _, field := range []struct {
		label, was, now string
	}{
		{"wallet keypair", given.walletKeypair, resolved.walletKeypair},
		{"mithril command", given.mithrilCommand, resolved.mithrilCommand},
		{"node command", given.nodeCommand, resolved.nodeCommand},
		{"quote adapter", given.quoteScript, resolved.quoteScript},
	} {
		if field.was == "" && field.now != "" {
			lines = append(lines, fmt.Sprintf("  %-16s %s", field.label, field.now))
		}
	}
	return lines
}

// missingHostPaths names what is still unknown, with the flag that supplies it.
// An error that says only "not found" leaves the operator to work out which of
// four things it meant.
func missingHostPaths(resolved hostPaths) error {
	var missing []string
	for _, field := range []struct{ flag, value string }{
		{"--wallet-keypair", resolved.walletKeypair},
		{"--mithril-command", resolved.mithrilCommand},
		{"--node-command", resolved.nodeCommand},
		{"--quote-script", resolved.quoteScript},
	} {
		if field.value == "" {
			missing = append(missing, field.flag+" PATH")
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"could not find %d host path(s); nothing on this machine records them and "+
			"nothing is installed beside this executable, so pass them: %v",
		len(missing), missing)
}

// hostPathsFromExistingConfig reads the commands a working leg already uses.
// It prefers a strategy leg over the single-config pointer because a strategy
// is the richer setup, but either answers the same question.
func hostPathsFromExistingConfig() hostPaths {
	candidate := ""
	if paths, _ := discoverStrategy(); !paths.empty() {
		for _, leg := range paths.configured() {
			if leg.leg != "sweep" {
				candidate = leg.path
				break
			}
		}
	}
	if candidate == "" {
		candidate = discoverCurrentConfig()
	}
	if candidate == "" {
		return hostPaths{}
	}
	cfg, err := readConfig(candidate)
	if err != nil {
		return hostPaths{}
	}
	return hostPaths{
		walletKeypair:  cfg.Signer.KeypairPath,
		mithrilCommand: cfg.MCP.Command,
		nodeCommand:    cfg.Quote.Command,
		quoteScript:    cfg.Quote.ScriptPath,
	}
}

// hostPathsBesideExecutable finds the runtime a packaged install ships next to
// the agent. It never returns the wallet keypair: a private key is not
// something to discover by convention, and adopting one found lying beside a
// binary is exactly the wrong instinct for a program that signs transactions.
func hostPathsBesideExecutable() hostPaths {
	binary, err := os.Executable()
	if err != nil {
		return hostPaths{}
	}
	dir := filepath.Dir(binary)
	return hostPaths{
		mithrilCommand: filepath.Join(dir, siblingMithrilCommand),
		nodeCommand:    filepath.Join(dir, siblingNodeCommand),
		quoteScript:    filepath.Join(dir, siblingQuoteScript),
	}
}

// usableFile reports whether a path names a regular file that exists. A
// directory or a dangling symlink is not an answer.
func usableFile(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}
