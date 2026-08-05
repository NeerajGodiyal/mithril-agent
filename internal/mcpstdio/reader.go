package mcpstdio

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const MaxFrameBytes = 64 << 10

var (
	errFrameTooLarge = errors.New("MCP frame exceeds the configured limit")
	errInvalidFrame  = errors.New("MCP input must contain one newline-delimited JSON value per frame")
)

type Reader struct {
	source       io.ReadCloser
	buffered     *bufio.Reader
	maxFrame     int
	pending      []byte
	terminalRead error
}

func NewReader(source io.ReadCloser) *Reader {
	return NewReaderSize(source, MaxFrameBytes)
}

// NewReaderSize bounds each frame at maxFrame bytes. Clients reading tool
// results need a larger frame than the server accepts as input, and an
// unbounded decoder would let a peer buffer one message until we run out of
// memory.
func NewReaderSize(source io.ReadCloser, maxFrame int) *Reader {
	if maxFrame <= 0 {
		maxFrame = MaxFrameBytes
	}
	return &Reader{
		source:   source,
		buffered: bufio.NewReaderSize(source, maxFrame+2),
		maxFrame: maxFrame,
	}
}

func (r *Reader) frameTooLarge() error {
	return fmt.Errorf("%w of %d bytes", errFrameTooLarge, r.maxFrame)
}

func (r *Reader) Read(output []byte) (int, error) {
	if len(output) == 0 {
		return 0, nil
	}
	if len(r.pending) > 0 {
		written := copy(output, r.pending)
		r.pending = r.pending[written:]
		return written, nil
	}
	if r.terminalRead != nil {
		return 0, r.terminalRead
	}
	for {
		frame, readErr := r.buffered.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			r.terminalRead = r.frameTooLarge()
			return 0, r.terminalRead
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			r.terminalRead = readErr
			return 0, readErr
		}
		if len(frame) == 0 && errors.Is(readErr, io.EOF) {
			r.terminalRead = io.EOF
			return 0, io.EOF
		}
		payload := frame
		if payload[len(payload)-1] == '\n' {
			payload = payload[:len(payload)-1]
			if len(payload) > 0 && payload[len(payload)-1] == '\r' {
				payload = payload[:len(payload)-1]
			}
		}
		if len(payload) > r.maxFrame {
			r.terminalRead = r.frameTooLarge()
			return 0, r.terminalRead
		}
		if bytes.IndexByte(payload, '\r') >= 0 || !utf8.Valid(payload) {
			r.terminalRead = errInvalidFrame
			return 0, r.terminalRead
		}
		if len(bytes.TrimSpace(payload)) == 0 {
			if errors.Is(readErr, io.EOF) {
				r.terminalRead = io.EOF
				return 0, io.EOF
			}
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(payload))
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			r.terminalRead = errInvalidFrame
			return 0, r.terminalRead
		}
		var extra json.RawMessage
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			r.terminalRead = errInvalidFrame
			return 0, r.terminalRead
		}
		r.pending = append(append([]byte(nil), payload...), '\n')
		if errors.Is(readErr, io.EOF) {
			r.terminalRead = io.EOF
		}
		written := copy(output, r.pending)
		r.pending = r.pending[written:]
		return written, nil
	}
}

func (r *Reader) Close() error {
	return r.source.Close()
}

type WriteCloser struct {
	io.Writer
}

func (WriteCloser) Close() error { return nil }
