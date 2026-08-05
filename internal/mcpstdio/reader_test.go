package mcpstdio

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReaderNormalizesValidFrames(t *testing.T) {
	input := "  \n{\"id\":1}\r\n[1,2]"
	reader := NewReader(io.NopCloser(strings.NewReader(input)))
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "{\"id\":1}\n[1,2]\n"; got != want {
		t.Fatalf("frames = %q, want %q", got, want)
	}
}

func TestReaderSupportsSmallDestinationBuffers(t *testing.T) {
	reader := NewReader(io.NopCloser(strings.NewReader("{\"jsonrpc\":\"2.0\"}\n")))
	buffer := make([]byte, 3)
	var output strings.Builder
	for {
		count, err := reader.Read(buffer)
		output.Write(buffer[:count])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, want := output.String(), "{\"jsonrpc\":\"2.0\"}\n"; got != want {
		t.Fatalf("frame = %q, want %q", got, want)
	}
}

func TestReaderRejectsInvalidFrames(t *testing.T) {
	tests := []struct {
		name  string
		input string
		err   error
	}{
		{name: "malformed", input: "{\n", err: errInvalidFrame},
		{name: "multiple values", input: "{} {}\n", err: errInvalidFrame},
		{name: "embedded carriage return", input: "{\"x\":\"a\rb\"}\n", err: errInvalidFrame},
		{name: "invalid utf8", input: string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}', '\n'}), err: errInvalidFrame},
		{name: "oversized", input: strings.Repeat("x", MaxFrameBytes+1) + "\n", err: errFrameTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := NewReader(io.NopCloser(strings.NewReader(test.input)))
			_, err := io.ReadAll(reader)
			if !errors.Is(err, test.err) {
				t.Fatalf("error = %v, want %v", err, test.err)
			}
			if _, nextErr := reader.Read(make([]byte, 1)); !errors.Is(nextErr, test.err) {
				t.Fatalf("second error = %v, want %v", nextErr, test.err)
			}
		})
	}
}

func TestReaderAcceptsMaximumFrame(t *testing.T) {
	payload := `"` + strings.Repeat("x", MaxFrameBytes-2) + `"`
	reader := NewReader(io.NopCloser(strings.NewReader(payload + "\n")))
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != MaxFrameBytes+1 || string(data[:len(data)-1]) != payload {
		t.Fatalf("frame length = %d", len(data))
	}
}

type closeTracker struct{ closed bool }

func (*closeTracker) Read([]byte) (int, error) { return 0, io.EOF }

func (c *closeTracker) Close() error {
	c.closed = true
	return nil
}

func TestReaderClosesSource(t *testing.T) {
	source := &closeTracker{}
	if err := NewReader(source).Close(); err != nil {
		t.Fatal(err)
	}
	if !source.closed {
		t.Fatal("source was not closed")
	}
}

func TestNewReaderSizeBoundsLargerClientFrames(t *testing.T) {
	const limit = 1 << 20
	big := "{\"a\":\"" + strings.Repeat("x", limit/2) + "\"}\n"

	accepted := NewReaderSize(io.NopCloser(strings.NewReader(big)), limit)
	if _, err := io.ReadAll(accepted); err != nil {
		t.Fatalf("frame within the client limit was rejected: %v", err)
	}

	// The same frame must still be refused by the server-side default.
	rejected := NewReader(io.NopCloser(strings.NewReader(big)))
	if _, err := io.ReadAll(rejected); !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("error = %v, want errFrameTooLarge", err)
	}

	over := "{\"a\":\"" + strings.Repeat("x", limit) + "\"}\n"
	if _, err := io.ReadAll(NewReaderSize(io.NopCloser(strings.NewReader(over)), limit)); !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("error = %v, want errFrameTooLarge for an oversized client frame", err)
	}
}
