package main

import "testing"

func TestSwapRunnerStartsOnlyWithMCPStatePending(t *testing.T) {
	ready := preflightChecks{
		Config: preflightOK, Profile: preflightOK,
		PolicyBinding: preflightOK, KeypairBinding: preflightOK,
		RiskPolicy: preflightOK, RiskKeypair: preflightOK,
		SubmitterPolicy: preflightOK, SubmitterKey: preflightOK,
		Commands: preflightOK, ControlPath: preflightOK,
		JournalPath: preflightOK, PathSeparation: preflightOK,
		Providers: preflightOK, MCPInputs: preflightFailed,
		PriceSource: preflightOK, Clock: preflightOK,
	}
	if !swapPreflightAllowsStartup(preflightSummary{
		Status: preflightFailed, Checks: ready,
	}) {
		t.Fatal("a bootstrapping MCP state prevented the runner from publishing status")
	}

	ready.Clock = preflightFailed
	if swapPreflightAllowsStartup(preflightSummary{Status: preflightFailed, Checks: ready}) {
		t.Fatal("a non-MCP preflight failure was allowed to start")
	}
}
