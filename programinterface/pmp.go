package programinterface

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"io"

	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

const (
	// ProgramMetadataProgram is the canonical Solana Program Metadata program.
	ProgramMetadataProgram = "ProgM6JCCvbYkfKqJYHePx4xxSUSqJp7rh8Lyv7nk7S"
	pmpHeaderBytes         = 96
	pmpExternalDataBytes   = 40
)

// PMPReader supplies bounded, context-checked account values.
type PMPReader interface {
	AccountData(context.Context, string, uint64, uint64) (solanarpc.AccountDataSlice, error)
	AccountDataRange(context.Context, string, uint64, uint64, uint64) (solanarpc.AccountDataSlice, error)
}

// PMPResult records the canonical metadata account and immutable local pin.
type PMPResult struct {
	PinResult
	MetadataAccount    string `json:"metadata_account"`
	ContextSlot        uint64 `json:"context_slot"`
	Bankhash           string `json:"bankhash"`
	Source             string `json:"source"`
	ContentAccount     string `json:"content_account,omitempty"`
	ContentContextSlot uint64 `json:"content_context_slot,omitempty"`
}

// FetchAndPinPMP loads a canonical direct or external-account PMP IDL through
// bounded account reads, validates its complete header and content, then stores
// the exact uncompressed JSON in the immutable local registry.
func FetchAndPinPMP(
	ctx context.Context,
	reader PMPReader,
	registry, program string,
	minContextSlot uint64,
) (PMPResult, error) {
	if reader == nil || minContextSlot == 0 {
		return PMPResult{}, errors.New("PMP reader and minimum context slot are required")
	}
	programKey, err := solana.Decode32(program)
	if err != nil {
		return PMPResult{}, errors.New("program is not a canonical Solana address")
	}
	seed := make([]byte, 16)
	copy(seed, "idl")
	metadata, _, err := solana.FindProgramAddress(
		[][]byte{programKey[:], seed}, ProgramMetadataProgram,
	)
	if err != nil {
		return PMPResult{}, errors.New("derive canonical PMP account")
	}
	account, err := reader.AccountData(ctx, metadata, minContextSlot, MaxIDLBytes+pmpHeaderBytes)
	if err != nil {
		return PMPResult{}, errors.New("read canonical PMP account through Mithril")
	}
	if account.ContextSlot < minContextSlot || account.Owner != ProgramMetadataProgram ||
		account.Executable || account.DataLength != uint64(len(account.Data)) {
		return PMPResult{}, errors.New("canonical PMP account owner or context is invalid")
	}
	bankhash, bankhashErr := solana.Decode32(account.Bankhash)
	if bankhashErr != nil || bankhash == ([32]byte{}) {
		return PMPResult{}, errors.New("canonical PMP account bank identity is invalid")
	}
	metadataValue, err := decodePMP(account.Data, programKey)
	if err != nil {
		return PMPResult{}, err
	}
	result := PMPResult{
		MetadataAccount: metadata, ContextSlot: account.ContextSlot, Bankhash: account.Bankhash,
	}
	var content []byte
	switch metadataValue.source {
	case 0:
		content, err = decodePMPContent(
			metadataValue.data, metadataValue.compression,
		)
		result.Source = "canonical_pmp_direct"
	case 1:
		return PMPResult{}, errors.New("canonical PMP URL IDLs are disabled; pin reviewed bytes explicitly")
	case 2:
		if len(metadataValue.data) != pmpExternalDataBytes {
			return PMPResult{}, errors.New("canonical PMP external reference is invalid")
		}
		address := solana.Encode(metadataValue.data[:32])
		offset := uint64(binary.LittleEndian.Uint32(metadataValue.data[32:36]))
		length := uint64(binary.LittleEndian.Uint32(metadataValue.data[36:40]))
		readLength := uint64(MaxIDLBytes)
		if length != 0 {
			if length > MaxIDLBytes {
				return PMPResult{}, errors.New("canonical PMP external IDL exceeds 1 MiB")
			}
			readLength = length
		}
		external, readErr := reader.AccountDataRange(
			ctx, address, account.ContextSlot, offset, readLength,
		)
		if readErr != nil {
			return PMPResult{}, errors.New("read canonical PMP external account through Mithril")
		}
		if external.ContextSlot != account.ContextSlot || external.Bankhash != account.Bankhash || offset > external.DataLength {
			return PMPResult{}, errors.New("canonical PMP external account context or range is invalid")
		}
		available := external.DataLength - offset
		if length == 0 {
			length = available
		}
		if length == 0 || length > available || length > MaxIDLBytes || uint64(len(external.Data)) != length {
			return PMPResult{}, errors.New("canonical PMP external account range is invalid")
		}
		content, err = decodePMPContent(
			external.Data, metadataValue.compression,
		)
		result.Source = "canonical_pmp_external"
		result.ContentAccount = address
		result.ContentContextSlot = external.ContextSlot
	default:
		return PMPResult{}, errors.New("canonical PMP data source is unsupported")
	}
	if err != nil {
		return PMPResult{}, err
	}
	pin, err := Pin(registry, program, content)
	if err != nil {
		return PMPResult{}, err
	}
	result.PinResult = pin
	return result, nil
}

type pmpData struct {
	data        []byte
	compression byte
	source      byte
}

func decodePMP(data []byte, program [32]byte) (pmpData, error) {
	if len(data) < pmpHeaderBytes || data[0] != 2 || !bytes.Equal(data[1:33], program[:]) {
		return pmpData{}, errors.New("canonical PMP metadata header is invalid")
	}
	if data[65] > 1 || data[66] != 1 || !bytes.Equal(data[67:70], []byte("idl")) ||
		!allZero(data[70:83]) || !allZero(data[91:96]) {
		return pmpData{}, errors.New("canonical PMP identity or seed is invalid")
	}
	encoding, compression, format, source := data[83], data[84], data[85], data[86]
	if encoding != 1 || format != 1 {
		return pmpData{}, errors.New("canonical PMP IDL must use UTF-8 JSON")
	}
	length := uint64(binary.LittleEndian.Uint32(data[87:91]))
	if length == 0 || length > uint64(len(data)-pmpHeaderBytes) {
		return pmpData{}, errors.New("canonical PMP data length is invalid")
	}
	return pmpData{
		data:        data[pmpHeaderBytes : pmpHeaderBytes+length],
		compression: compression, source: source,
	}, nil
}

func decodePMPContent(packed []byte, compression byte) ([]byte, error) {
	var (
		reader io.ReadCloser
		err    error
	)
	switch compression {
	case 0:
		return boundedIDL(bytes.NewReader(packed))
	case 1:
		reader, err = gzip.NewReader(bytes.NewReader(packed))
	case 2:
		reader, err = zlib.NewReader(bytes.NewReader(packed))
	default:
		return nil, errors.New("canonical PMP compression is unsupported")
	}
	if err != nil {
		return nil, errors.New("canonical PMP compressed data is invalid")
	}
	defer reader.Close()
	return boundedIDL(reader)
}

func boundedIDL(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxIDLBytes+1))
	if err != nil || len(data) == 0 || len(data) > MaxIDLBytes {
		return nil, errors.New("canonical PMP IDL is empty, invalid, or exceeds 1 MiB")
	}
	return data, nil
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
