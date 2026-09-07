package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperdashboard"
)

type researchAttemptOutput struct{ buffer bytes.Buffer }

func (b *researchAttemptOutput) Len() int       { return b.buffer.Len() }
func (b *researchAttemptOutput) String() string { return b.buffer.String() }

func (b *researchAttemptOutput) Write(p []byte) (int, error) {
	if len(p) > 4096-b.Len() {
		return 0, errors.New("native research status exceeds limit")
	}
	return b.buffer.Write(p)
}

func recordResearchAttempt(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", "mithril-hermes-research.service", "--property=LoadState,ActiveState,SubState,InvocationID,ExecMainPID,ExecMainStartTimestamp,ExecMainExitTimestamp,Result", "--no-pager")
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "TZ=UTC"}
	var output researchAttemptOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return errors.New("could not inspect native research service")
	}
	status, err := parseResearchAttempt(output.String(), time.Now().UTC())
	if err != nil {
		return err
	}
	return paperdashboard.RecordResearchAttempt(path, status)
}

func parseResearchAttempt(raw string, checked time.Time) (paperdashboard.ResearchAttempt, error) {
	status := paperdashboard.ResearchAttempt{Version: 1, CheckedAt: checked.UTC(), State: "unknown"}
	invalid := errors.New("native research status is invalid")
	if checked.IsZero() || len(raw) == 0 || len(raw) > 4096 {
		return status, invalid
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		switch key {
		case "LoadState", "ActiveState", "SubState", "InvocationID", "ExecMainPID", "ExecMainStartTimestamp", "ExecMainExitTimestamp", "Result":
		default:
			return status, invalid
		}
		if !ok {
			return status, invalid
		}
		if _, exists := values[key]; exists {
			return status, invalid
		}
		values[key] = value
	}
	if len(values) != 8 {
		return status, invalid
	}
	id := values["InvocationID"]
	if id != "" {
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != id {
			return status, invalid
		}
	}
	pid, err := strconv.ParseUint(values["ExecMainPID"], 10, 32)
	if err != nil || strconv.FormatUint(pid, 10) != values["ExecMainPID"] {
		return status, invalid
	}
	parseTime := func(value string) (*time.Time, error) {
		if value == "" {
			return nil, nil
		}
		const layout = "Mon 2006-01-02 15:04:05 MST"
		tm, err := time.Parse(layout, value)
		if err != nil || !strings.HasSuffix(value, " UTC") || tm.Format(layout) != value || tm.After(checked.Add(2*time.Second)) {
			return nil, invalid
		}
		u := tm.UTC()
		return &u, nil
	}
	start, err := parseTime(values["ExecMainStartTimestamp"])
	if err != nil {
		return status, err
	}
	end, err := parseTime(values["ExecMainExitTimestamp"])
	if err != nil {
		return status, err
	}
	if values["LoadState"] != "loaded" {
		return status, nil
	}
	if values["ActiveState"] == "inactive" && start != nil && end != nil && end.Before(*start) {
		return status, invalid
	}
	switch values["ActiveState"] {
	case "activating":
		switch values["SubState"] {
		case "start-pre", "condition":
			if id != "" {
				status.State = "preparing"
				status.InvocationID = id
			}
		case "start-post":
			if id != "" {
				status.State = "finishing"
				status.InvocationID = id
			}
		case "start":
			if id != "" && pid > 0 && start != nil {
				status.State = "running"
				status.InvocationID = id
				status.StartedAt = start
			}
		}
	case "active":
		if values["SubState"] == "running" && id != "" && pid > 0 && start != nil {
			status.State = "running"
			status.InvocationID = id
			status.StartedAt = start
		}
	case "deactivating":
		if id != "" {
			status.State = "finishing"
			status.InvocationID = id
			// Stop hooks can follow failed preflight; old main times are ambiguous.
		}
	case "inactive":
		if values["SubState"] == "dead" && values["Result"] == "success" {
			status.State = "idle"
			if id != "" && start != nil && end != nil && !end.Before(*start) {
				status.State = "completed"
				status.InvocationID = id
				status.StartedAt = start
				status.FinishedAt = end
			}
		}
	case "failed":
		if values["SubState"] == "failed" && values["Result"] != "" && values["Result"] != "success" {
			status.State = "failed"
			status.InvocationID = id
			// An old main-process interval cannot be tied to a failed preflight
			// from these properties alone. Do not attach it to this failure.
		}
	}
	return status, nil
}
