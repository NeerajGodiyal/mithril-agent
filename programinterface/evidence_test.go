package programinterface

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestProgramEvidencePinAndLoad(t *testing.T) {
	registry := t.TempDir()
	program := "11111111111111111111111111111111"
	data := []byte("reviewed repository analysis at commit abc123")
	path := filepath.Join(t.TempDir(), "review.txt")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := ReadEvidence(path)
	if err != nil {
		t.Fatal(err)
	}
	created, err := PinEvidence(registry, program, "repository", read, testEvidenceReview(t, registry, program, "repository"))
	if err != nil {
		t.Fatal(err)
	}
	if !created.Created || created.Bytes != len(data) || len(created.SHA256) != 64 {
		t.Fatalf("created evidence = %+v", created)
	}
	again, err := PinEvidence(registry, program, "repository", read, testEvidenceReview(t, registry, program, "repository"))
	if err != nil || again.Created {
		t.Fatalf("idempotent pin = %+v, %v", again, err)
	}
	loaded, err := LoadEvidence(registry, program, "repository", created.SHA256)
	if err != nil || loaded.Path != created.Path || loaded.Bytes != len(data) {
		t.Fatalf("loaded evidence = %+v, %v", loaded, err)
	}
	legacy := testEvidenceReview(t, registry, program, "repository")
	legacy.Version, legacy.Summary = 1, ""
	if _, err := PinEvidence(registry, program, "repository", []byte("legacy reviewed evidence"), legacy); err == nil {
		t.Fatal("new version 1 evidence was accepted")
	}
	legacySHA := seedLegacyEvidence(t, registry, program, "repository", []byte("legacy reviewed evidence"))
	if _, err := LoadEvidence(registry, program, "repository", legacySHA); err != nil {
		t.Fatalf("load version 1 evidence: %v", err)
	}
	if listed, err := ListEvidence(registry, program); err != nil || len(listed) != 2 {
		t.Fatalf("list evidence with legacy artifact = %+v, %v", listed, err)
	}
	if _, err := PinEvidence(registry, program, "unknown", data, testEvidenceReview(t, registry, program, "repository")); err == nil {
		t.Fatal("unknown evidence kind was accepted")
	}
	if _, err := LoadEvidence(registry, program, "repository", strings.Repeat("A", 64)); err == nil {
		t.Fatal("non-canonical evidence hash was accepted")
	}
}

func TestPinEvidenceRequiresPinnedInterfaceBeforeWriting(t *testing.T) {
	registry := t.TempDir()
	program := "11111111111111111111111111111111"
	review := testEvidenceReview(t, registry, program, "repository")
	review.InterfaceSHA256 = strings.Repeat("b", 64)
	if _, err := PinEvidence(registry, program, "repository", []byte("reviewed"), review); err == nil {
		t.Fatal("evidence for an unpinned interface was accepted")
	}
	assertNoEvidenceDirectory(t, registry, program)

	review = testEvidenceReview(t, registry, program, "repository")
	review.Version, review.Summary = 1, ""
	if _, err := PinEvidence(registry, program, "repository", []byte("legacy"), review); err == nil {
		t.Fatal("new version 1 evidence was accepted")
	}
	assertNoEvidenceDirectory(t, registry, program)
}

func TestListEvidenceReturnsVerifiedBoundedMetadata(t *testing.T) {
	registry := t.TempDir()
	program := "11111111111111111111111111111111"
	repository, err := PinEvidence(
		registry, program, "repository", []byte("reviewed repository"), testEvidenceReview(t, registry, program, "repository"),
	)
	if err != nil {
		t.Fatal(err)
	}
	simulation, err := PinEvidence(
		registry, program, "simulation", []byte("reviewed simulation"), testEvidenceReview(t, registry, program, "simulation"),
	)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := ListEvidence(registry, program)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Kind != "repository" || listed[0].SHA256 != repository.SHA256 ||
		listed[1].Kind != "simulation" || listed[1].SHA256 != simulation.SHA256 {
		t.Fatalf("listed evidence = %+v", listed)
	}
	if err := os.WriteFile(repository.Path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListEvidence(registry, program); err == nil {
		t.Fatal("changed evidence was listed as verified")
	}
}

func TestListEvidenceRefusesTooManyArtifacts(t *testing.T) {
	registry := t.TempDir()
	program := "11111111111111111111111111111111"
	for index := 0; index <= MaxEvidenceArtifacts; index++ {
		if _, err := PinEvidence(
			registry, program, "repository", []byte(fmt.Sprintf("reviewed-%d", index)),
			testEvidenceReview(t, registry, program, "repository"),
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ListEvidence(registry, program); err == nil {
		t.Fatal("oversized evidence registry was accepted")
	}
}

func TestProgramEvidenceRequiresAndRevalidatesReviewAttestation(t *testing.T) {
	registry := t.TempDir()
	program := "11111111111111111111111111111111"
	review := testEvidenceReview(t, registry, program, "simulation")
	review.Decision = "rejected"
	if _, err := PinEvidence(registry, program, "simulation", []byte("result"), review); err == nil {
		t.Fatal("rejected review was accepted")
	}
	review = testEvidenceReview(t, registry, program, "simulation")
	review.ContextSlot = 0
	if _, err := PinEvidence(registry, program, "simulation", []byte("result"), review); err == nil {
		t.Fatal("simulation review without context was accepted")
	}
	review = testEvidenceReview(t, registry, program, "repository")
	review.Summary = ""
	if _, err := PinEvidence(registry, program, "repository", []byte("result"), review); err == nil {
		t.Fatal("version 2 review without a summary was accepted")
	}
	review = testEvidenceReview(t, registry, program, "repository")
	review.InterfaceSHA256 = ""
	if _, err := PinEvidence(registry, program, "repository", []byte("result"), review); err == nil {
		t.Fatal("version 2 review without an interface binding was accepted")
	}
	review = testEvidenceReview(t, registry, program, "repository")
	review.GenesisHash = ""
	if _, err := PinEvidence(registry, program, "repository", []byte("result"), review); err == nil {
		t.Fatal("version 3 review without genesis was accepted")
	}
	review = testEvidenceReview(t, registry, program, "repository")
	review.Bankhash = strings.Repeat("1", 32)
	if _, err := PinEvidence(registry, program, "repository", []byte("result"), review); err == nil {
		t.Fatal("version 3 review with an invalid bankhash was accepted")
	}
	review = testEvidenceReview(t, registry, program, "repository")
	review.DeploymentSHA256 = ""
	if _, err := PinEvidence(registry, program, "repository", []byte("result"), review); err == nil {
		t.Fatal("version 3 review without a deployment identity was accepted")
	}
	review = testEvidenceReview(t, registry, program, "repository")
	review.Summary = strings.Repeat("s", maxEvidenceSummary+1)
	if _, err := PinEvidence(registry, program, "repository", []byte("result"), review); err == nil {
		t.Fatal("oversized evidence summary was accepted")
	}
	review.Version = 1
	review.Summary = "legacy review cannot add a summary"
	if _, err := PinEvidence(registry, program, "repository", []byte("result"), review); err == nil {
		t.Fatal("version 1 review with a summary was accepted")
	}
	created, err := PinEvidence(
		registry, program, "repository", []byte("reviewed"), testEvidenceReview(t, registry, program, "repository"),
	)
	if err != nil {
		t.Fatal(err)
	}
	reviewPath := strings.TrimSuffix(created.Path, ".bin") + ".review.json"
	if err := os.WriteFile(reviewPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEvidence(registry, program, "repository", created.SHA256); err == nil {
		t.Fatal("changed review attestation was accepted")
	}
}

func testEvidenceReview(t *testing.T, registry, program, kind string) EvidenceReview {
	t.Helper()
	pin, err := Pin(registry, program, testIDL())
	if err != nil {
		t.Fatal(err)
	}
	review := EvidenceReview{
		Version: 3, Reviewer: "operator", Decision: "approved",
		Summary:        "Reviewed behavior matches the deployed program and no signing authority is introduced.",
		SourceRevision: strings.Repeat("a", 64), Tool: "review-tool", ToolVersion: "1.0.0",
		InterfaceSHA256: pin.SHA256, GenesisHash: solana.DevnetGenesisHash,
		ContextSlot: 42, Bankhash: "SysvarRent111111111111111111111111111111111",
		DeploymentSHA256: strings.Repeat("d", 64),
	}
	if kind == "simulation" {
		review.MessageSHA256 = strings.Repeat("c", 64)
	}
	return review
}

func seedLegacyEvidence(t *testing.T, registry, program, kind string, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	directory := filepath.Join(registry, program, "evidence", kind)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	attestation, err := json.Marshal(EvidenceAttestation{
		Version: 1, Program: program, Kind: kind, ArtifactSHA256: hash,
		Reviewer: "legacy-operator", Decision: "approved", SourceRevision: "legacy-revision",
		Tool: "repository-review", ToolVersion: "0.9.0", ResultSHA256: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, hash+".bin"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, hash+".review.json"), append(attestation, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return hash
}

func assertNoEvidenceDirectory(t *testing.T, registry, program string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(registry, program, "evidence")); !os.IsNotExist(err) {
		t.Fatalf("rejected evidence created storage: %v", err)
	}
}
