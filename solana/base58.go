package solana

import "github.com/Overclock-Validator/mithril-agent/internal/base58"

const (
	// DevnetGenesisHash is Solana Devnet's immutable genesis identifier.
	DevnetGenesisHash = "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG"
	// TestnetGenesisHash is Solana Testnet's immutable genesis identifier.
	TestnetGenesisHash = "4uhcVJyU9pJkvQyS88uRDiswHXSCkY3zQawwpjk2NsNY"
	// MainnetBetaGenesisHash is Solana Mainnet Beta's immutable genesis identifier.
	MainnetBetaGenesisHash = "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d"
)

func ValidateBase58(value string, maxEncoded int) error {
	return base58.Validate(value, maxEncoded)
}

func Decode32(value string) ([32]byte, error) { return base58.Decode32(value) }

func Decode64(value string) ([64]byte, error) { return base58.Decode64(value) }

func Encode(data []byte) string { return base58.Encode(data) }
