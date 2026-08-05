package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Prompting is deliberately tiny: a question, a default the operator can accept
// with Enter, and a bounded answer. There is no dependency and no framework,
// because the guided setup only ever needs to read a line.
//
// The important property is that a non-interactive run must never block. When
// stdin is not a terminal — a pipe, CI, a test — every question takes its
// default and the transcript still shows what was chosen, so an automated run
// and a human run produce the same configuration.
const maxAnswerBytes = 512

type prompter struct {
	in          *bufio.Reader
	out         io.Writer
	interactive bool
	// answers records every question and the value used, so setup can print a
	// summary the operator confirms once rather than trusting them to recall
	// eight separate answers.
	answers []promptAnswer
}

type promptAnswer struct {
	Question string
	Value    string
	Default  bool
}

func newPrompter(in io.Reader, out io.Writer, interactive bool) *prompter {
	return &prompter{in: bufio.NewReader(in), out: out, interactive: interactive}
}

// stdinIsTerminal reports whether we can actually ask a human. A character
// device is a terminal; a pipe or file is not.
func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// ask presents a question with a default. Enter accepts the default; anything
// else replaces it.
func (p *prompter) ask(question, fallback string) (string, error) {
	if _, err := fmt.Fprintf(p.out, "\n%s\n  [%s] › ", question, fallback); err != nil {
		return "", err
	}
	if !p.interactive {
		if _, err := fmt.Fprintf(p.out, "%s (default)\n", fallback); err != nil {
			return "", err
		}
		p.answers = append(p.answers, promptAnswer{Question: question, Value: fallback, Default: true})
		return fallback, nil
	}
	line, err := p.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("could not read your answer")
	}
	if len(line) > maxAnswerBytes {
		return "", errors.New("that answer is too long")
	}
	value := strings.TrimSpace(line)
	usedDefault := value == ""
	if usedDefault {
		value = fallback
	}
	p.answers = append(p.answers, promptAnswer{Question: question, Value: value, Default: usedDefault})
	return value, nil
}

// confirm asks a yes/no question. It defaults to NO for anything consequential,
// so an operator who holds Enter through the wizard never authorises something
// by momentum.
func (p *prompter) confirm(question string, defaultYes bool) (bool, error) {
	hint := "y/N"
	if defaultYes {
		hint = "Y/n"
	}
	if _, err := fmt.Fprintf(p.out, "\n%s [%s] › ", question, hint); err != nil {
		return false, err
	}
	if !p.interactive {
		if _, err := fmt.Fprintf(p.out, "%v (default)\n", defaultYes); err != nil {
			return false, err
		}
		return defaultYes, nil
	}
	line, err := p.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, errors.New("could not read your answer")
	}
	if len(line) > maxAnswerBytes {
		// Bounded like ask(). Nothing this long is a yes, but a consent
		// prompt should never buffer an unbounded line either.
		return false, errors.New("that answer is too long")
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return defaultYes, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		// An unrecognised answer is never taken as consent.
		return false, nil
	}
}

func (p *prompter) sayf(format string, args ...any) {
	fmt.Fprintf(p.out, format+"\n", args...)
}
