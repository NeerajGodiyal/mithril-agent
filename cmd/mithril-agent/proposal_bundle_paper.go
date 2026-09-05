package main

import (
	"errors"

	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
)

func validatePaperBundlePaths(policy, journal, bounds string) error {
	if policy == "" && journal == "" && bounds == "" {
		return nil
	}
	if !distinctAbsolutePaths(policy, journal, bounds) {
		return errors.New("paper bundle review requires --paper-policy, --paper-journal, and --paper-bounds together as distinct absolute paths")
	}
	return nil
}

// checkPaperBundle adds evidence binding, never authorization or readiness.
// The existing journal reader checks its hash chain, UTC day and policy header.
func checkPaperBundle(policyPath, journalPath, boundsPath string, route jupiterswap.Policy, candidate proposalcheck.Candidate) (*proposalcheck.PaperIntent, error) {
	if err := validatePaperBundlePaths(policyPath, journalPath, boundsPath); err != nil {
		return nil, err
	}
	if policyPath == "" {
		return nil, nil
	}
	policy, err := loadShadowPolicy(policyPath)
	if err != nil {
		return nil, err
	}
	ticks, err := readShadowTicks(journalPath, policy)
	if err != nil {
		return nil, err
	}
	var bounds proposalcheck.PaperIntentBounds
	if err := readStrictJSON(boundsPath, &bounds); err != nil {
		return nil, errors.New("read private paper bounds: use quoted decimal base-unit amounts")
	}
	intent, err := proposalcheck.CheckPaperIntent(policy, ticks, route, candidate, bounds)
	if err != nil {
		return nil, err
	}
	return &intent, nil
}
