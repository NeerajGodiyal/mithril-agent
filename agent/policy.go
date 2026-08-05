package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	maxObservationAgeSeconds = 15 * 60
	maxNodeLagSlots          = 1_000
	maxReconciliationSeconds = 60 * 60
	maxFutureClockSkew       = 5 * time.Second
	minScheduleWindowSeconds = 60
	maxScheduleWindowSeconds = 24 * 60 * 60
	minClockUncertainty      = 100 * time.Millisecond
	maxClockUncertainty      = 2 * time.Second
	actionIDDomain           = "mithril-agent/treasury-sweep-v1/action-id"
	profileFingerprintDomain = "mithril-agent/profile-v1"
)

func (p Profile) Validate() error {
	if p.Name != ProfileTreasurySweepV1 {
		return fmt.Errorf("unsupported profile %q", p.Name)
	}
	if p.Version != 1 {
		return errors.New("treasury_sweep_v1 requires profile version 1")
	}
	switch p.Cluster {
	case "devnet", "testnet", "mainnet-beta":
	default:
		return fmt.Errorf("unsupported cluster %q", p.Cluster)
	}
	if _, err := solana.Decode32(p.Source); err != nil {
		return fmt.Errorf("source: %w", err)
	}
	if _, err := solana.Decode32(p.Destination); err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	if p.Source == p.Destination {
		return errors.New("source and destination must differ")
	}
	if p.MinTransferLamports == 0 {
		return errors.New("minimum transfer must be positive")
	}
	if p.MaxTransferLamports < p.MinTransferLamports {
		return errors.New("maximum transfer is below minimum transfer")
	}
	if p.DailyCapLamports < p.MinTransferLamports {
		return errors.New("daily cap is below minimum transfer")
	}
	if p.MaxTransferLamports > p.DailyCapLamports {
		return errors.New("maximum transfer exceeds daily cap")
	}
	if p.MaxFeeLamports == 0 {
		return errors.New("maximum fee must be positive")
	}
	if p.ReserveLamports > ^uint64(0)-p.MaxFeeLamports {
		return errors.New("reserve and maximum fee overflow")
	}
	if p.MaxFeeLamports >= p.DailyCapLamports ||
		p.MaxTransferLamports > p.DailyCapLamports-p.MaxFeeLamports {
		return errors.New("maximum transfer and fee exceed daily cap")
	}
	if p.ScheduleWindowSeconds < minScheduleWindowSeconds ||
		p.ScheduleWindowSeconds > maxScheduleWindowSeconds ||
		maxScheduleWindowSeconds%p.ScheduleWindowSeconds != 0 {
		return errors.New(
			"schedule window must divide one UTC day and be between 60 and 86400 seconds",
		)
	}
	if p.ScheduleAnchorUnix <= 0 ||
		p.ScheduleAnchorUnix%int64(maxScheduleWindowSeconds) != 0 {
		return errors.New("schedule anchor must be a positive UTC midnight Unix timestamp")
	}
	clockUncertainty := p.ClockUncertaintyLimit()
	if clockUncertainty < minClockUncertainty ||
		clockUncertainty > maxClockUncertainty {
		return errors.New("maximum clock uncertainty must be between 100 and 2000 milliseconds")
	}
	if p.MaxObservationAgeSeconds == 0 || p.MaxObservationAgeSeconds > maxObservationAgeSeconds {
		return fmt.Errorf("maximum observation age must be between 1 and %d seconds", maxObservationAgeSeconds)
	}
	if p.MinHealthyObservationSeconds == 0 ||
		p.MinHealthyObservationSeconds > p.MaxObservationAgeSeconds {
		return errors.New("minimum healthy observation interval must fit within the observation age")
	}
	if p.MinHealthySlotAdvance == 0 || p.MinHealthySlotAdvance > maxNodeLagSlots {
		return fmt.Errorf("minimum healthy slot advance must be between 1 and %d slots", maxNodeLagSlots)
	}
	if p.MaxNodeLagSlots == 0 || p.MaxNodeLagSlots > maxNodeLagSlots {
		return fmt.Errorf("maximum node lag must be between 1 and %d slots", maxNodeLagSlots)
	}
	if p.MaxReconciliationSeconds < 30 ||
		p.MaxReconciliationSeconds > maxReconciliationSeconds {
		return fmt.Errorf(
			"maximum reconciliation time must be between 30 and %d seconds",
			maxReconciliationSeconds,
		)
	}
	return nil
}

func (p Profile) ClockUncertaintyLimit() time.Duration {
	return time.Duration(p.MaxClockUncertaintyMillis) * time.Millisecond
}

func (p Profile) Fingerprint() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return "", errors.New("encode profile fingerprint")
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(profileFingerprintDomain))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(encoded)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (p Profile) Propose(obs Observation, now time.Time, reservedToday uint64) (Proposal, string, error) {
	now = now.UTC()
	observedAt, err := p.validateObservation(obs, now)
	if err != nil {
		return Proposal{}, "", err
	}
	obs.ObservedAt = observedAt
	windowStart, windowEnd, err := p.scheduleWindow(now)
	if err != nil {
		return Proposal{}, "", err
	}
	profileFingerprint, err := p.Fingerprint()
	if err != nil {
		return Proposal{}, "", err
	}
	actionID, err := ComputeActionID(profileFingerprint, windowStart)
	if err != nil {
		return Proposal{}, "", err
	}
	if reservedToday > p.DailyCapLamports {
		return Proposal{}, "", errors.New("journal reservations exceed the daily cap")
	}
	protectedBalance := p.ReserveLamports + p.MaxFeeLamports
	if obs.BalanceLamports <= protectedBalance {
		return Proposal{}, "balance_at_or_below_reserve", nil
	}

	amount := obs.BalanceLamports - protectedBalance
	if amount > p.MaxTransferLamports {
		amount = p.MaxTransferLamports
	}
	remaining := p.DailyCapLamports - reservedToday
	if remaining <= p.MaxFeeLamports {
		return Proposal{}, "below_minimum_or_daily_cap_exhausted", nil
	}
	remaining -= p.MaxFeeLamports
	if amount > remaining {
		amount = remaining
	}
	if amount < p.MinTransferLamports {
		return Proposal{}, "below_minimum_or_daily_cap_exhausted", nil
	}

	reserved := amount + p.MaxFeeLamports
	return Proposal{
		ActionID:                 actionID,
		Profile:                  p.Name,
		ProfileVersion:           p.Version,
		Cluster:                  p.Cluster,
		Source:                   p.Source,
		Destination:              p.Destination,
		AmountLamports:           amount,
		FeeBudgetLamports:        p.MaxFeeLamports,
		ReservedLamports:         reserved,
		ReserveLamports:          p.ReserveLamports,
		ObservedBalanceLamports:  obs.BalanceLamports,
		ObservationSlot:          obs.Slot,
		ObservationUnix:          observedAt.Unix(),
		ReservationDayUTC:        now.Format(time.DateOnly),
		ProfileFingerprint:       profileFingerprint,
		ScheduleWindowStartUnix:  windowStart,
		ScheduleWindowEndUnix:    windowEnd,
		MaxObservationAgeSeconds: p.MaxObservationAgeSeconds,
		MaxNodeLagSlots:          p.MaxNodeLagSlots,
		MaxReconciliationSeconds: p.MaxReconciliationSeconds,
	}, "", nil
}

func (p Profile) validateObservation(obs Observation, now time.Time) (time.Time, error) {
	if err := p.Validate(); err != nil {
		return time.Time{}, err
	}
	if obs.Cluster != p.Cluster {
		return time.Time{}, errors.New("observation cluster does not match profile")
	}
	if obs.Source != p.Source {
		return time.Time{}, errors.New("observation source does not match profile")
	}
	if obs.Slot == 0 {
		return time.Time{}, errors.New("observation slot must be positive")
	}
	if obs.ObservedAt.IsZero() {
		return time.Time{}, errors.New("observation time is missing")
	}
	now = now.UTC()
	observedAt := obs.ObservedAt.UTC()
	if observedAt.After(now.Add(maxFutureClockSkew)) {
		return time.Time{}, errors.New("observation time is in the future")
	}
	if now.Sub(observedAt) > time.Duration(p.MaxObservationAgeSeconds)*time.Second {
		return time.Time{}, errors.New("observation is stale")
	}
	return observedAt, nil
}

func (p Profile) scheduleWindow(now time.Time) (int64, int64, error) {
	if err := p.Validate(); err != nil {
		return 0, 0, err
	}
	nowUnix := now.UTC().Unix()
	if nowUnix < p.ScheduleAnchorUnix {
		return 0, 0, errors.New("schedule anchor is in the future")
	}
	windowSeconds := int64(p.ScheduleWindowSeconds)
	elapsed := nowUnix - p.ScheduleAnchorUnix
	start := nowUnix - elapsed%windowSeconds
	if start > int64(^uint64(0)>>1)-windowSeconds {
		return 0, 0, errors.New("schedule window overflows")
	}
	return start, start + windowSeconds, nil
}

func ComputeActionID(profileFingerprint string, scheduleWindowStartUnix int64) (string, error) {
	decoded, err := hex.DecodeString(profileFingerprint)
	if err != nil || len(decoded) != sha256.Size ||
		hex.EncodeToString(decoded) != profileFingerprint {
		return "", errors.New("profile fingerprint is invalid")
	}
	if scheduleWindowStartUnix <= 0 {
		return "", errors.New("schedule window start is invalid")
	}
	idInput := struct {
		Domain                  string `json:"domain"`
		ProfileFingerprint      string `json:"profile_sha256"`
		ScheduleWindowStartUnix int64  `json:"schedule_window_start_unix"`
	}{
		Domain:                  actionIDDomain,
		ProfileFingerprint:      profileFingerprint,
		ScheduleWindowStartUnix: scheduleWindowStartUnix,
	}
	encoded, err := json.Marshal(idInput)
	if err != nil {
		return "", fmt.Errorf("encode action identity: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
