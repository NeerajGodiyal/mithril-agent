package jupiterswap

import (
	"encoding/base64"
	"errors"
	"sort"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

const maxAddressTables = 32

// AddressTableEvidence is the compact canonical form of independently
// verified lookup-table contents bound into a signing request.
type AddressTableEvidence struct {
	Address         string `json:"address"`
	AddressesBase64 string `json:"addresses_base64"`
}

// EncodeAddressTables converts verified table contents into deterministic JSON
// order without expanding each 32-byte address into a base58 string.
func EncodeAddressTables(tables map[[32]byte][][32]byte) ([]AddressTableEvidence, error) {
	if len(tables) > maxAddressTables {
		return nil, errors.New("too many Jupiter address tables")
	}
	encoded := make([]AddressTableEvidence, 0, len(tables))
	for table, addresses := range tables {
		if len(addresses) == 0 || len(addresses) > 256 {
			return nil, errors.New("Jupiter address table size is invalid")
		}
		raw := make([]byte, 0, len(addresses)*32)
		for _, address := range addresses {
			raw = append(raw, address[:]...)
		}
		encoded = append(encoded, AddressTableEvidence{
			Address: solana.Encode(table[:]), AddressesBase64: base64.StdEncoding.EncodeToString(raw),
		})
	}
	sort.Slice(encoded, func(i, j int) bool { return encoded[i].Address < encoded[j].Address })
	return encoded, nil
}

// DecodeAddressTables rejects non-canonical ordering, duplicates, and padded
// or partial entries before returning data suitable for v0 message decoding.
func DecodeAddressTables(evidence []AddressTableEvidence) (map[[32]byte][][32]byte, error) {
	if len(evidence) > maxAddressTables {
		return nil, errors.New("too many Jupiter address tables")
	}
	tables := make(map[[32]byte][][32]byte, len(evidence))
	previous := ""
	for _, item := range evidence {
		if item.Address <= previous {
			return nil, errors.New("Jupiter address tables are not in canonical order")
		}
		table, err := solana.Decode32(item.Address)
		if err != nil {
			return nil, errors.New("Jupiter address table address is invalid")
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(item.AddressesBase64)
		if err != nil || len(raw) == 0 || len(raw)%32 != 0 || len(raw)/32 > 256 ||
			base64.StdEncoding.EncodeToString(raw) != item.AddressesBase64 {
			return nil, errors.New("Jupiter address table contents are invalid")
		}
		addresses := make([][32]byte, len(raw)/32)
		for index := range addresses {
			copy(addresses[index][:], raw[index*32:(index+1)*32])
		}
		tables[table] = addresses
		previous = item.Address
	}
	return tables, nil
}
