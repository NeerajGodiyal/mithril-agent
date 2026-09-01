package paperdashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
)

const (
	instructionVersion  = 1
	maxInstructionBytes = int64(1024)
)

type Instruction struct {
	Version    uint64    `json:"version"`
	UpdatedAt  time.Time `json:"updated_at"`
	Market     string    `json:"market"`
	Preference string    `json:"preference"`
}

type instructionRequest struct {
	Market     string `json:"market"`
	Preference string `json:"preference"`
}

func (s *Server) serveInstruction(writer http.ResponseWriter, request *http.Request) {
	if s.instructionPath == "" {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.Header.Get("X-Mithril-Paper-Request") != "1" ||
		(request.Header.Get("Sec-Fetch-Site") != "" &&
			request.Header.Get("Sec-Fetch-Site") != "same-origin") {
		http.Error(writer, "request not allowed", http.StatusForbidden)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(writer, "application/json required", http.StatusUnsupportedMediaType)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxInstructionBytes)
	data, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, "instruction is too large", http.StatusRequestEntityTooLarge)
		return
	}
	var wanted instructionRequest
	if err := strictjson.Decode(data, &wanted); err != nil || !s.validInstructionRequest(wanted) {
		http.Error(writer, "instruction is invalid", http.StatusBadRequest)
		return
	}
	instruction := Instruction{
		Version: instructionVersion, UpdatedAt: s.now().UTC(),
		Market: wanted.Market, Preference: wanted.Preference,
	}
	s.mu.Lock()
	err = writeInstruction(s.instructionPath, instruction)
	if err == nil {
		s.readAt = time.Time{}
	}
	s.mu.Unlock()
	if err != nil {
		http.Error(writer, "instruction could not be saved", http.StatusServiceUnavailable)
		return
	}
	encoded, _ := json.Marshal(instruction)
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusCreated)
	_, _ = writer.Write(append(encoded, '\n'))
}

func (s *Server) validInstructionRequest(request instructionRequest) bool {
	instruction := Instruction{
		Version: instructionVersion, UpdatedAt: time.Unix(1, 0).UTC(),
		Market: request.Market, Preference: request.Preference,
	}
	if !validInstruction(instruction) {
		return false
	}
	if request.Market == "all" {
		return true
	}
	for _, source := range s.sources {
		if source.SourceLabel() == request.Market {
			return true
		}
	}
	for _, market := range marketadmission.Markets() {
		if market == request.Market {
			return true
		}
	}
	return false
}

func (s *Server) EnableInstructions(path string) error {
	if !cleanAbsolutePath(path) {
		return errors.New("paper instruction path must be a clean absolute path")
	}
	s.instructionPath = path
	return nil
}

func readInstruction(path string) (*Instruction, error) {
	if !cleanAbsolutePath(path) {
		return nil, errors.New("paper instruction path must be a clean absolute path")
	}
	data, err := securefile.ReadPrivate(path, maxInstructionBytes)
	if err != nil {
		return nil, err
	}
	var instruction Instruction
	if err := strictjson.Decode(data, &instruction); err != nil || !validInstruction(instruction) {
		return nil, errors.New("paper instruction is invalid")
	}
	return &instruction, nil
}

func writeInstruction(path string, instruction Instruction) error {
	if !cleanAbsolutePath(path) || !validInstruction(instruction) {
		return errors.New("paper instruction is invalid")
	}
	encoded, err := json.Marshal(instruction)
	if err != nil {
		return errors.New("encode paper instruction")
	}
	if err := securefile.ReplacePrivate(path, append(encoded, '\n'), maxInstructionBytes); err != nil {
		return errors.New("write paper instruction")
	}
	return nil
}

func validInstruction(instruction Instruction) bool {
	return instruction.Version == instructionVersion &&
		!instruction.UpdatedAt.IsZero() && instruction.UpdatedAt.Equal(instruction.UpdatedAt.UTC()) &&
		(instruction.Market == "all" || validLabel(instruction.Market)) &&
		(instruction.Preference == "more-opportunities" ||
			instruction.Preference == "balanced" ||
			instruction.Preference == "more-selective")
}

func cleanAbsolutePath(path string) bool {
	return path != "" && path != string(filepath.Separator) && filepath.IsAbs(path) &&
		filepath.Clean(path) == path
}

// RenderInstruction returns fixed research guidance rather than copying
// operator-controlled bytes into an LLM prompt.
func RenderInstruction(path string) (string, error) {
	instruction, err := readInstruction(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	market := "all configured paper markets"
	if instruction.Market != "all" {
		market = instruction.Market
	}
	goal := map[string]string{
		"more-opportunities": "look for more independently testable opportunities without lowering any guardrail",
		"balanced":           "balance opportunity frequency with the current conservative evidence requirements",
		"more-selective":     "prefer fewer, higher-confidence opportunities and tighter evidence",
	}[instruction.Preference]
	return fmt.Sprintf("\n\nOperator research preference (not trade authority): for %s, %s. Treat this as a research priority only. It cannot change the live paper policy, budget, safety limits, selection gate, or execution permissions.\n", market, goal), nil
}
