package main

import (
	"strings"
	"testing"
)

func TestPerpsPaperSystemdIsBoundedSignerFreeAndPrivate(t *testing.T) {
	collector := readDocumentation(t, "../../deploy/systemd/mithril-agent-perps-paper.service")
	for _, want := range []string{
		"User=mithril-agent-research", "UMask=0077",
		"RuntimeDirectory=mithril-agent-perps-paper", "RuntimeDirectoryMode=0700",
		"RuntimeDirectoryPreserve=no", "--state-dir /run/mithril-agent-perps-paper",
		"--environment mainnet --symbols SOL,BTC,ETH --arm balanced",
		"--paper-usd-per-market 100 --cadence 15s --duration 1h",
		"Restart=no", "RuntimeMaxSec=65min", "ProtectSystem=strict",
		"RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6",
	} {
		if !strings.Contains(collector, want) {
			t.Errorf("perps paper collector is missing %q", want)
		}
	}
	for _, forbidden := range []string{"EnvironmentFile=", "LoadCredential=", "Restart=always", "--once"} {
		if strings.Contains(collector, forbidden) {
			t.Errorf("perps paper collector contains %q", forbidden)
		}
	}

	bridge := readDocumentation(t, "../../deploy/systemd/mithril-agent-perps-paper-status-bridge@.service")
	for _, want := range []string{
		"LoadCredential=paper-status:/run/mithril-agent-perps-paper/%i-paper-status.json",
		"InaccessiblePaths=-/run/mithril-agent-perps-paper",
		"mithril-agent-paper-status-bridge --credential paper-status",
		"PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX", "RuntimeMaxSec=5s",
	} {
		if !strings.Contains(bridge, want) {
			t.Errorf("perps status bridge is missing %q", want)
		}
	}

	socket := readDocumentation(t, "../../deploy/systemd/mithril-agent-perps-paper-status@.socket")
	for _, want := range []string{
		"ListenStream=/run/mithril-agent-perps-%i-paper-status.sock",
		"BindsTo=mithril-agent-perps-paper.service",
		"After=mithril-agent-perps-paper.service",
		"SocketGroup=mithril-agent-status", "SocketMode=0660",
		"Service=mithril-agent-perps-paper-status-bridge@%i.service", "Accept=no",
	} {
		if !strings.Contains(socket, want) {
			t.Errorf("perps status socket is missing %q", want)
		}
	}
}

func TestDashboardAndTelegramConsumeThreeLabeledPerpsSources(t *testing.T) {
	dashboard := readDocumentation(t, "../../deploy/systemd/mithril-agent-paper-dashboard.service")
	telegram := readDocumentation(t, "../../deploy/systemd/mithril-agent-telegram-paper.conf")
	for name, unit := range map[string]string{"dashboard": dashboard, "Telegram": telegram} {
		for _, want := range []string{
			"mithril-agent-perps-paper.service",
			"mithril-agent-perps-paper-status@sol.socket",
			"mithril-agent-perps-paper-status@btc.socket",
			"mithril-agent-perps-paper-status@eth.socket",
			"SOL-PERP=/run/mithril-agent-perps-sol-paper-status.sock",
			"BTC-PERP=/run/mithril-agent-perps-btc-paper-status.sock",
			"ETH-PERP=/run/mithril-agent-perps-eth-paper-status.sock",
		} {
			if !strings.Contains(unit, want) {
				t.Errorf("%s unit is missing %q", name, want)
			}
		}
	}
	if strings.Count(dashboard, "--optional-paper-status-socket") != 3 {
		t.Fatal("dashboard perps sources are not explicitly optional")
	}
	for name, unit := range map[string]string{"dashboard": dashboard, "Telegram": telegram} {
		for _, line := range strings.Split(unit, "\n") {
			if strings.HasPrefix(line, "Wants=") && strings.Contains(line, "perps") {
				t.Errorf("%s restart can relaunch the bounded perps experiment: %q", name, line)
			}
		}
	}
}
