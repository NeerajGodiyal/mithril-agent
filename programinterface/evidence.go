package programinterface

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	// MaxEvidenceBytes bounds one reviewed program artifact.
	MaxEvidenceBytes = 16 << 20
	// MaxEvidenceReviewBytes bounds one review input and stored attestation.
	MaxEvidenceReviewBytes = 16 << 10
	// MaxEvidenceArtifacts and MaxEvidenceSetBytes bound MCP discovery work.
	MaxEvidenceArtifacts  = 64
	MaxEvidenceSetBytes   = 64 << 20
	evidenceReviewVersion = 3
	maxEvidenceSummary    = 4 << 10
)

var evidenceKinds = [...]string{"repository", "decompiler", "simulation"}

// EvidenceReview records the human and tool context used to approve one
// artifact. Version 3 binds the reviewed deployment and processed bank;
// simulation reviews additionally bind the exact message they evaluated.
type EvidenceReview struct {
	Version          int    `json:"version"`
	Reviewer         string `json:"reviewer"`
	Decision         string `json:"decision"`
	Summary          string `json:"summary,omitempty"`
	SourceRevision   string `json:"source_revision"`
	Tool             string `json:"tool"`
	ToolVersion      string `json:"tool_version"`
	InterfaceSHA256  string `json:"interface_sha256,omitempty"`
	MessageSHA256    string `json:"message_sha256,omitempty"`
	GenesisHash      string `json:"genesis_hash,omitempty"`
	ContextSlot      uint64 `json:"context_slot,omitempty"`
	Bankhash         string `json:"bankhash,omitempty"`
	DeploymentSHA256 string `json:"deployment_sha256,omitempty"`
}

// EvidenceAttestation binds a validated review to exact immutable artifact
// bytes. It is local audit metadata, not a cryptographic reviewer signature.
type EvidenceAttestation struct {
	Version          int    `json:"version"`
	Program          string `json:"program"`
	Kind             string `json:"kind"`
	ArtifactSHA256   string `json:"artifact_sha256"`
	Reviewer         string `json:"reviewer"`
	Decision         string `json:"decision"`
	Summary          string `json:"summary,omitempty"`
	SourceRevision   string `json:"source_revision"`
	Tool             string `json:"tool"`
	ToolVersion      string `json:"tool_version"`
	InterfaceSHA256  string `json:"interface_sha256,omitempty"`
	MessageSHA256    string `json:"message_sha256,omitempty"`
	GenesisHash      string `json:"genesis_hash,omitempty"`
	ContextSlot      uint64 `json:"context_slot,omitempty"`
	Bankhash         string `json:"bankhash,omitempty"`
	DeploymentSHA256 string `json:"deployment_sha256,omitempty"`
	ResultSHA256     string `json:"result_sha256"`
}

// EvidenceResult identifies one immutable, operator-reviewed program artifact.
// Mithril Agent stores but never executes or interprets the artifact.
type EvidenceResult struct {
	Program     string              `json:"program"`
	Kind        string              `json:"kind"`
	SHA256      string              `json:"sha256"`
	Bytes       int                 `json:"bytes"`
	Path        string              `json:"path"`
	Created     bool                `json:"created"`
	Attestation EvidenceAttestation `json:"attestation"`
}

// ReadEvidence reads one stable local analysis artifact.
func ReadEvidence(path string) ([]byte, error) {
	return readStableRegular(path, "program evidence", MaxEvidenceBytes)
}

// ReadEvidenceReview reads and strictly validates one local review file.
func ReadEvidenceReview(path, kind string) (EvidenceReview, error) {
	raw, err := readStableRegular(path, "program evidence review", MaxEvidenceReviewBytes)
	if err != nil {
		return EvidenceReview{}, err
	}
	var review EvidenceReview
	if err := strictjson.Decode(raw, &review); err != nil {
		return EvidenceReview{}, errors.New("program evidence review JSON is invalid")
	}
	if err := validateEvidenceReview(kind, review); err != nil {
		return EvidenceReview{}, err
	}
	return review, nil
}

// PinEvidence stores exact reviewed bytes under the program and artifact kind.
func PinEvidence(
	registry, program, kind string,
	data []byte,
	review EvidenceReview,
) (EvidenceResult, error) {
	if _, err := solana.Decode32(program); err != nil {
		return EvidenceResult{}, errors.New("program is not a canonical Solana address")
	}
	if !validEvidenceKind(kind) {
		return EvidenceResult{}, errors.New("evidence kind must be repository, decompiler, or simulation")
	}
	if err := validateEvidenceReview(kind, review); err != nil {
		return EvidenceResult{}, err
	}
	if review.Version != evidenceReviewVersion {
		return EvidenceResult{}, errors.New("new program evidence requires review version 3")
	}
	if len(data) == 0 || len(data) > MaxEvidenceBytes {
		return EvidenceResult{}, errors.New("program evidence is empty or exceeds 16 MiB")
	}
	if registry == "" {
		return EvidenceResult{}, errors.New("registry path is required")
	}
	if _, _, err := Load(registry, program, review.InterfaceSHA256); err != nil {
		return EvidenceResult{}, errors.New("program evidence requires a matching pinned interface")
	}
	registry, err := filepath.Abs(registry)
	if err != nil {
		return EvidenceResult{}, errors.New("resolve registry path")
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	attestation := EvidenceAttestation{
		Version: review.Version, Program: program, Kind: kind, ArtifactSHA256: hash,
		Reviewer: review.Reviewer, Decision: review.Decision,
		Summary:        review.Summary,
		SourceRevision: review.SourceRevision, Tool: review.Tool, ToolVersion: review.ToolVersion,
		InterfaceSHA256: review.InterfaceSHA256, MessageSHA256: review.MessageSHA256,
		GenesisHash: review.GenesisHash, ContextSlot: review.ContextSlot,
		Bankhash: review.Bankhash, DeploymentSHA256: review.DeploymentSHA256,
		ResultSHA256: hash,
	}
	encodedReview, err := json.MarshalIndent(attestation, "", "  ")
	if err != nil {
		return EvidenceResult{}, errors.New("encode program evidence attestation")
	}
	encodedReview = append(encodedReview, '\n')
	directory := filepath.Join(filepath.Clean(registry), program, "evidence", kind)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return EvidenceResult{}, errors.New("create program evidence registry")
	}
	path := filepath.Join(directory, hash+".bin")
	result := EvidenceResult{
		Program: program, Kind: kind, SHA256: hash, Bytes: len(data), Path: path,
		Created: true, Attestation: attestation,
	}
	artifactCreated := true
	if err := securefile.CreatePrivate(path, data, MaxEvidenceBytes); err != nil {
		existing, readErr := securefile.ReadPrivate(path, MaxEvidenceBytes)
		if readErr != nil || !bytes.Equal(existing, data) {
			return EvidenceResult{}, errors.New("program evidence path already exists with different or unreadable content")
		}
		artifactCreated = false
	}
	reviewPath := filepath.Join(directory, hash+".review.json")
	reviewCreated := true
	if err := securefile.CreatePrivate(reviewPath, encodedReview, MaxEvidenceReviewBytes); err != nil {
		existing, readErr := securefile.ReadPrivate(reviewPath, MaxEvidenceReviewBytes)
		if readErr != nil || !bytes.Equal(existing, encodedReview) {
			return EvidenceResult{}, errors.New("program evidence attestation already exists with different or unreadable content")
		}
		reviewCreated = false
	}
	result.Created = artifactCreated || reviewCreated
	return result, nil
}

// LoadEvidence revalidates an immutable program artifact by content hash.
func LoadEvidence(registry, program, kind, expectedSHA256 string) (EvidenceResult, error) {
	if _, err := solana.Decode32(program); err != nil {
		return EvidenceResult{}, errors.New("program is not a canonical Solana address")
	}
	if !validEvidenceKind(kind) {
		return EvidenceResult{}, errors.New("evidence kind must be repository, decompiler, or simulation")
	}
	if !validEvidenceSHA256(expectedSHA256) {
		return EvidenceResult{}, errors.New("evidence SHA-256 must be 64 lowercase hexadecimal characters")
	}
	if registry == "" {
		return EvidenceResult{}, errors.New("registry path is required")
	}
	registry, err := filepath.Abs(registry)
	if err != nil {
		return EvidenceResult{}, errors.New("resolve registry path")
	}
	path := filepath.Join(filepath.Clean(registry), program, "evidence", kind, expectedSHA256+".bin")
	data, err := securefile.ReadPrivate(path, MaxEvidenceBytes)
	if err != nil {
		return EvidenceResult{}, errors.New("read pinned program evidence")
	}
	if len(data) == 0 {
		return EvidenceResult{}, errors.New("pinned program evidence is empty")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expectedSHA256 {
		return EvidenceResult{}, errors.New("program evidence hash does not match its requested identity")
	}
	reviewPath := filepath.Join(filepath.Dir(path), expectedSHA256+".review.json")
	rawReview, err := securefile.ReadPrivate(reviewPath, MaxEvidenceReviewBytes)
	if err != nil {
		return EvidenceResult{}, errors.New("read pinned program evidence attestation")
	}
	var attestation EvidenceAttestation
	if err := strictjson.Decode(rawReview, &attestation); err != nil ||
		validateEvidenceAttestation(program, kind, expectedSHA256, attestation) != nil {
		return EvidenceResult{}, errors.New("pinned program evidence attestation is invalid")
	}
	return EvidenceResult{
		Program: program, Kind: kind, SHA256: expectedSHA256, Bytes: len(data), Path: path,
		Attestation: attestation,
	}, nil
}

// ListEvidence revalidates bounded pinned artifacts and returns deterministic
// metadata. Artifact contents never leave this package.
func ListEvidence(registry, program string) ([]EvidenceResult, error) {
	if _, err := solana.Decode32(program); err != nil {
		return nil, errors.New("program is not a canonical Solana address")
	}
	if registry == "" {
		return nil, errors.New("registry path is required")
	}
	registry, err := filepath.Abs(registry)
	if err != nil {
		return nil, errors.New("resolve registry path")
	}
	var results []EvidenceResult
	totalBytes := 0
	for _, kind := range evidenceKinds {
		directory := filepath.Join(filepath.Clean(registry), program, "evidence", kind)
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("program evidence registry is not private")
		}
		if err := secureexec.ValidateProtectedDirectory(directory); err != nil {
			return nil, errors.New("program evidence registry is not private")
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, errors.New("read private program evidence registry")
		}
		artifacts := make(map[string]bool)
		attestations := make(map[string]bool)
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil, errors.New("program evidence registry contains an invalid entry")
			}
			hash, artifact := strings.CutSuffix(name, ".bin")
			if artifact && validEvidenceSHA256(hash) {
				artifacts[hash] = true
				continue
			}
			hash, attestation := strings.CutSuffix(name, ".review.json")
			if !attestation || !validEvidenceSHA256(hash) {
				return nil, errors.New("program evidence registry contains an invalid entry")
			}
			attestations[hash] = true
		}
		if len(artifacts) != len(attestations) {
			return nil, errors.New("program evidence registry contains an unattested or missing artifact")
		}
		for _, entry := range entries {
			hash, ok := strings.CutSuffix(entry.Name(), ".bin")
			if !ok {
				continue
			}
			if !attestations[hash] {
				return nil, errors.New("program evidence registry contains an unattested artifact")
			}
			if len(results) == MaxEvidenceArtifacts {
				return nil, errors.New("program evidence registry exceeds 64 artifacts")
			}
			result, err := LoadEvidence(registry, program, kind, hash)
			if err != nil {
				return nil, err
			}
			totalBytes += result.Bytes
			if totalBytes > MaxEvidenceSetBytes {
				return nil, errors.New("program evidence registry exceeds 64 MiB")
			}
			results = append(results, result)
		}
	}
	return results, nil
}

func validEvidenceKind(kind string) bool {
	return kind == "repository" || kind == "decompiler" || kind == "simulation"
}

func validEvidenceSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateEvidenceReview(kind string, review EvidenceReview) error {
	if !validEvidenceKind(kind) {
		return errors.New("evidence kind must be repository, decompiler, or simulation")
	}
	switch review.Version {
	case 1:
		if review.Summary != "" {
			return errors.New("program evidence review version 1 cannot set summary")
		}
	case 2, evidenceReviewVersion:
		if !validEvidenceSummary(review.Summary) {
			return errors.New("program evidence review summary is invalid")
		}
		if !validEvidenceSHA256(review.InterfaceSHA256) {
			return errors.New("program evidence review requires a valid interface SHA-256")
		}
	default:
		return errors.New("program evidence review version must be 1, 2, or 3")
	}
	for name, value := range map[string]string{
		"reviewer": review.Reviewer, "source revision": review.SourceRevision,
		"tool": review.Tool, "tool version": review.ToolVersion,
	} {
		if !validEvidenceReviewText(value) {
			return errors.New("program evidence review " + name + " is invalid")
		}
	}
	if review.Decision != "approved" {
		return errors.New("program evidence review decision must be approved")
	}
	if review.Version == 1 && review.InterfaceSHA256 != "" && !validEvidenceSHA256(review.InterfaceSHA256) {
		return errors.New("program evidence review interface SHA-256 is invalid")
	}
	if review.MessageSHA256 != "" && !validEvidenceSHA256(review.MessageSHA256) {
		return errors.New("program evidence review message SHA-256 is invalid")
	}
	if review.Version == evidenceReviewVersion {
		if !validEvidenceAddress(review.GenesisHash) || review.ContextSlot == 0 ||
			!validEvidenceAddress(review.Bankhash) || !validEvidenceSHA256(review.DeploymentSHA256) {
			return errors.New("program evidence review version 3 requires genesis_hash, context_slot, bankhash, and deployment_sha256")
		}
	} else if review.GenesisHash != "" || review.Bankhash != "" || review.DeploymentSHA256 != "" {
		return errors.New("only program evidence review version 3 may bind runtime applicability")
	}
	if kind == "simulation" {
		if review.InterfaceSHA256 == "" || review.MessageSHA256 == "" || review.ContextSlot == 0 {
			return errors.New("simulation evidence review requires interface_sha256, message_sha256, and context_slot")
		}
	} else if review.MessageSHA256 != "" || review.Version < evidenceReviewVersion && review.ContextSlot != 0 {
		return errors.New("only simulation or version 3 evidence review may set message_sha256 or context_slot")
	}
	return nil
}

func validateEvidenceAttestation(program, kind, hash string, attestation EvidenceAttestation) error {
	if attestation.Program != program || attestation.Kind != kind ||
		attestation.ArtifactSHA256 != hash || attestation.ResultSHA256 != hash {
		return errors.New("program evidence attestation binding does not match the artifact")
	}
	return validateEvidenceReview(kind, EvidenceReview{
		Version: attestation.Version, Reviewer: attestation.Reviewer,
		Decision: attestation.Decision, Summary: attestation.Summary,
		SourceRevision: attestation.SourceRevision,
		Tool:           attestation.Tool, ToolVersion: attestation.ToolVersion,
		InterfaceSHA256: attestation.InterfaceSHA256,
		MessageSHA256:   attestation.MessageSHA256, GenesisHash: attestation.GenesisHash,
		ContextSlot: attestation.ContextSlot, Bankhash: attestation.Bankhash,
		DeploymentSHA256: attestation.DeploymentSHA256,
	})
}

func validEvidenceAddress(value string) bool {
	decoded, err := solana.Decode32(value)
	return err == nil && decoded != ([32]byte{})
}

func validEvidenceReviewText(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func validEvidenceSummary(value string) bool {
	if value == "" || len(value) > maxEvidenceSummary || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}
