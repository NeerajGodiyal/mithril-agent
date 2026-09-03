package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

type shadowPerpsResearchError struct {
	err error
}

func (err *shadowPerpsResearchError) Error() string { return err.err.Error() }
func (err *shadowPerpsResearchError) Unwrap() error { return err.err }

func sealShadowPerpsTape(stateDir string, tape shadowPerpsTape) (string, error) {
	raw, digest, err := canonicalShadowPerpsTape(tape)
	if err != nil {
		return "", err
	}
	directory := shadowPerpsCorpusDir(stateDir, tape.Config.Symbol)
	if err := ensureShadowPerpsPrivateDirectory(filepath.Dir(directory)); err != nil {
		return "", err
	}
	if err := ensureShadowPerpsPrivateDirectory(directory); err != nil {
		return "", err
	}
	path := filepath.Join(directory, digest+".json")
	staging := filepath.Join(directory, "."+digest+".staging")
	if err := os.Remove(staging); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("remove stale perps paper tape staging file: %w", err)
	}
	if existing, err := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes); err == nil {
		if bytes.Equal(existing, raw) {
			return path, nil
		}
		return "", errors.New("sealed perps paper tape content does not match its digest")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect sealed perps paper tape: %w", err)
	}
	defer os.Remove(staging)
	if err := securefile.CreatePrivate(staging, raw, shadowPerpsMaxFileBytes); err != nil {
		return "", fmt.Errorf("stage perps paper tape: %w", err)
	}
	staged, err := securefile.ReadPrivate(staging, shadowPerpsMaxFileBytes)
	if err != nil || !bytes.Equal(staged, raw) {
		return "", errors.New("staged perps paper tape did not verify")
	}
	if err := securefile.RenameNoReplace(staging, path); err != nil {
		existing, readErr := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes)
		if readErr == nil && bytes.Equal(existing, raw) {
			return path, nil
		}
		return "", fmt.Errorf("publish sealed perps paper tape: %w", err)
	}
	if err := syncShadowPerpsCorpus(directory); err != nil {
		return "", err
	}
	return path, nil
}

func syncShadowPerpsCorpus(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open perps paper tape corpus: %w", err)
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return fmt.Errorf("sync perps paper tape corpus: %w", err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close perps paper tape corpus: %w", err)
	}
	return nil
}

func shadowPerpsCorpusDir(stateDir string, symbol perpspaper.Symbol) string {
	return filepath.Join(filepath.Dir(stateDir), "tapes", strings.ToLower(string(symbol)))
}

func ensureShadowPerpsPrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create perps paper tape corpus: %w", err)
	}
	if err := secureexec.ValidateProtectedDirectory(path); err != nil {
		return errors.New("perps paper tape corpus is not trusted")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("perps paper tape corpus must be private mode 0700")
	}
	return nil
}

func canonicalShadowPerpsTape(tape shadowPerpsTape) ([]byte, string, error) {
	if _, err := perpspaper.QualifyTournament(tape.Config.qualificationConfig(), tape.Frames); err != nil {
		return nil, "", fmt.Errorf("verify perps paper tape: %w", err)
	}
	raw, err := json.Marshal(tape)
	if err != nil {
		return nil, "", fmt.Errorf("encode perps paper tape: %w", err)
	}
	raw = append(raw, '\n')
	digest := sha256.Sum256(raw)
	return raw, hex.EncodeToString(digest[:]), nil
}

func preserveCompletedShadowPerpsTapes(stateDir string) error {
	for _, symbol := range [...]perpspaper.Symbol{perpspaper.SOL, perpspaper.BTC, perpspaper.ETH} {
		path := filepath.Join(stateDir, strings.ToLower(string(symbol))+"-tape.json")
		raw, err := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read previous %s paper tape: %w", symbol, err)
		}
		var header shadowPerpsTape
		if err := strictjson.Decode(raw, &header); err != nil || header.Config.Symbol != symbol {
			return fmt.Errorf("decode previous %s paper tape", symbol)
		}
		tape, _, err := readShadowPerpsTape(path, header.Config)
		if err != nil {
			return err
		}
		if len(tape.Frames) >= perpspaper.QualificationMinimumFrames {
			if _, err := sealShadowPerpsTape(stateDir, tape); err != nil {
				return fmt.Errorf("preserve previous %s paper tape: %w", symbol, err)
			}
		}
	}
	return nil
}

func readShadowPerpsCorpusTape(path string) (shadowPerpsTape, string, error) {
	raw, err := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes)
	if err != nil {
		return shadowPerpsTape{}, "", fmt.Errorf("read immutable paper tape: %w", err)
	}
	var header shadowPerpsTape
	if err := strictjson.Decode(raw, &header); err != nil {
		return shadowPerpsTape{}, "", errors.New("decode immutable paper tape")
	}
	tape, _, err := readShadowPerpsTape(path, header.Config)
	if err != nil {
		return shadowPerpsTape{}, "", err
	}
	canonical, digest, err := canonicalShadowPerpsTape(tape)
	if err != nil {
		return shadowPerpsTape{}, "", err
	}
	if !bytes.Equal(raw, canonical) || filepath.Base(path) != digest+".json" {
		return shadowPerpsTape{}, "", errors.New("immutable paper tape name or content digest does not match")
	}
	return tape, digest, nil
}

func qualifyShadowPerpsCorpus(stateDir string, config shadowPerpsTapeConfig) (*perpspaper.WalkForwardQualification, error) {
	directory := shadowPerpsCorpusDir(stateDir, config.Symbol)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read perps paper tape corpus: %w", err)
	}
	removedStaging := false
	for _, entry := range entries {
		name := entry.Name()
		if !shadowPerpsStagingName(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !fileowner.Trusted(info) {
			return nil, errors.New("perps paper tape corpus contains an invalid staging entry")
		}
		if err := recoverShadowPerpsStaging(directory, name); err != nil {
			return nil, err
		}
		removedStaging = true
	}
	if removedStaging {
		if err := syncShadowPerpsCorpus(directory); err != nil {
			return nil, err
		}
		entries, err = os.ReadDir(directory)
		if err != nil {
			return nil, fmt.Errorf("reread perps paper tape corpus: %w", err)
		}
	}
	type corpusTape struct {
		tape   shadowPerpsTape
		digest string
	}
	tapes := make([]corpusTape, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) != 69 || !strings.HasSuffix(name, ".json") {
			return nil, errors.New("perps paper tape corpus contains an unexpected entry")
		}
		if _, err := hex.DecodeString(strings.TrimSuffix(name, ".json")); err != nil {
			return nil, errors.New("perps paper tape corpus contains an unexpected entry")
		}
		tape, digest, err := readShadowPerpsCorpusTape(filepath.Join(directory, name))
		if err != nil {
			return nil, err
		}
		if !compatibleShadowPerpsTapes(config, tape.Config) {
			return nil, errors.New("perps paper tape corpus mixes incompatible market configurations")
		}
		tapes = append(tapes, corpusTape{tape: tape, digest: digest})
	}
	if len(tapes) < 2 {
		return nil, nil
	}
	sort.Slice(tapes, func(left, right int) bool {
		return tapes[left].tape.Frames[0].Book.Time < tapes[right].tape.Frames[0].Book.Time
	})
	windows := make([]perpspaper.WalkForwardTape, len(tapes))
	for index, item := range tapes {
		windows[index] = perpspaper.WalkForwardTape{ContentSHA256: item.digest, Frames: item.tape.Frames}
	}
	result, err := perpspaper.QualifyWalkForward(config.qualificationConfig(), windows)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func recoverShadowPerpsStaging(directory, name string) error {
	staging := filepath.Join(directory, name)
	raw, readErr := securefile.ReadPrivate(staging, shadowPerpsMaxFileBytes)
	digest := name[1:65]
	complete := false
	if readErr == nil {
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) == digest {
			var header shadowPerpsTape
			if strictjson.Decode(raw, &header) == nil {
				tape, _, err := readShadowPerpsTape(staging, header.Config)
				if err == nil {
					canonical, canonicalDigest, err := canonicalShadowPerpsTape(tape)
					complete = err == nil && canonicalDigest == digest && bytes.Equal(canonical, raw)
				}
			}
		}
	}
	if !complete {
		if err := os.Remove(staging); err != nil {
			return fmt.Errorf("remove incomplete perps paper tape staging file: %w", err)
		}
		return nil
	}
	path := filepath.Join(directory, digest+".json")
	if err := securefile.RenameNoReplace(staging, path); err != nil {
		existing, readErr := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes)
		if readErr != nil || !bytes.Equal(existing, raw) {
			return fmt.Errorf("recover staged perps paper tape: %w", err)
		}
		if err := os.Remove(staging); err != nil {
			return fmt.Errorf("remove duplicate perps paper tape staging file: %w", err)
		}
	}
	return nil
}

func shadowPerpsStagingName(name string) bool {
	if len(name) != 73 || name[0] != '.' || !strings.HasSuffix(name, ".staging") {
		return false
	}
	digest := name[1:65]
	_, err := hex.DecodeString(digest)
	return err == nil && digest == strings.ToLower(digest)
}

func compatibleShadowPerpsTapes(left, right shadowPerpsTapeConfig) bool {
	return left.Environment == right.Environment && left.Symbol == right.Symbol &&
		left.StartingCollateralMicros == right.StartingCollateralMicros &&
		left.VenueMaxLeverage == right.VenueMaxLeverage && left.VenueSzDecimals == right.VenueSzDecimals
}

func applyShadowPerpsWalkForward(summary *paperstatus.CurrentSummary, result perpspaper.WalkForwardQualification) {
	summary.QualificationTracked = true
	summary.QualificationOutcome = result.Outcome
	summary.QualificationSHA256 = result.InputSHA256
	summary.QualificationTapes = uint64(len(result.Tapes))
	summary.QualificationFrames = 0
	summary.QualificationMinimumFrames = 0
	summary.QualificationTrainingFrames = 0
	summary.QualificationHoldoutFrames = 0
	summary.QualificationStrategy = ""
	summary.QualificationRiskProfile = ""
	summary.QualificationHoldoutEvaluated = false
	summary.QualificationStressEvaluated = false
	summary.QualificationHoldoutScored = false
	summary.QualificationStressScored = false
	summary.QualificationHoldoutMicros = 0
	summary.QualificationStressMicros = 0
	summary.QualificationAttempts = nil
	for index, tape := range result.Tapes {
		summary.QualificationFrames += tape.Frames
		if index < len(result.Tapes)-1 {
			summary.QualificationTrainingFrames += tape.Frames
		} else {
			summary.QualificationHoldoutFrames = tape.Frames
		}
	}
	summary.QualificationMinimumFrames = uint64(len(result.Tapes)) * perpspaper.QualificationMinimumFrames
	for _, attempt := range perpspaper.BestCompletedTrainingAttempts(result.Training) {
		score := attempt.Score
		summary.QualificationAttempts = append(summary.QualificationAttempts, paperstatus.QualificationAttempt{
			RiskProfile: string(attempt.RiskArm), Strategy: string(attempt.Strategy),
			NetPnLMicros: score.NetPnLMicros, FeesMicros: score.FeesPaidMicros,
			FundingMicros: score.FundingPnLMicros, MaxDrawdownMicros: score.MaxDrawdownMicros,
			Liquidations: score.Liquidations, FilledOrders: score.FilledOrders,
			ClosedPositions: score.ClosedPositions,
		})
	}
	if result.TrainingLeader != nil {
		summary.QualificationStrategy = string(result.TrainingLeader.Strategy)
		summary.QualificationRiskProfile = string(result.TrainingLeader.RiskArm)
	}
	if result.Forward != nil {
		summary.QualificationHoldoutEvaluated = true
		if result.Forward.Score != nil {
			summary.QualificationHoldoutScored = true
			summary.QualificationHoldoutMicros = result.Forward.Score.NetPnLMicros
		}
	}
	if result.Stress != nil {
		summary.QualificationStressEvaluated = true
		if result.Stress.Score != nil {
			summary.QualificationStressScored = true
			summary.QualificationStressMicros = result.Stress.Score.NetPnLMicros
		}
	}
}

func shadowPerpsWalkForwardLabel(result perpspaper.WalkForwardQualification) string {
	switch result.Outcome {
	case "insufficient_evidence":
		return "more complete market recordings are needed"
	case "no_training_candidate":
		return "no paper plan passed every training gate across the earlier recordings"
	case "candidate_rejected":
		return "the selected paper plan did not pass the final untouched recording"
	default:
		return "one paper plan passed and can be checked again"
	}
}

func shadowPerpsWalkForwardMessage(result perpspaper.WalkForwardQualification) string {
	message := fmt.Sprintf("PAPER · 🧪 STRATEGY CHECK\nRecordings checked: %d separate\nResult: %s", len(result.Tapes), shadowPerpsWalkForwardLabel(result))
	if result.Forward != nil {
		if result.Forward.Score != nil {
			message += "\nFinal untouched recording: " + formatPerpsResult(result.Forward.Score.NetPnLMicros)
		} else {
			message += "\nFinal untouched recording: no complete result"
		}
	} else if result.Outcome == "no_training_candidate" {
		message += "\nFinal untouched recording: kept closed"
	}
	return message + "\nNo real order was sent."
}
