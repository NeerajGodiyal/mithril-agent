// Package base58 contains keyless Solana base58 encoding checks. It stays
// below the transaction package so read-only components do not acquire signing
// dependencies merely to validate a public address.
package base58

import (
	"errors"
	"math/big"
	"strings"
)

const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var radix = big.NewInt(58)

func Validate(value string, maxEncoded int) error {
	if value == "" {
		return errors.New("base58 value is empty")
	}
	if maxEncoded <= 0 || len(value) > maxEncoded {
		return errors.New("base58 value is too long")
	}
	n := new(big.Int)
	for _, r := range value {
		digit := strings.IndexRune(alphabet, r)
		if digit < 0 {
			return errors.New("base58 value contains an invalid character")
		}
		n.Mul(n, radix)
		n.Add(n, big.NewInt(int64(digit)))
	}
	leadingZeroes := 0
	for leadingZeroes < len(value) && value[leadingZeroes] == '1' {
		leadingZeroes++
	}
	decoded := make([]byte, leadingZeroes+len(n.Bytes()))
	copy(decoded[leadingZeroes:], n.Bytes())
	if Encode(decoded) != value {
		return errors.New("base58 value is not canonical")
	}
	return nil
}

func Decode32(value string) ([32]byte, error) {
	var out [32]byte
	if value == "" {
		return out, errors.New("base58 value is empty")
	}
	if len(value) > 44 {
		return out, errors.New("base58 value is too long")
	}
	n := new(big.Int)
	for _, r := range value {
		digit := strings.IndexRune(alphabet, r)
		if digit < 0 {
			return out, errors.New("base58 value contains an invalid character")
		}
		n.Mul(n, radix)
		n.Add(n, big.NewInt(int64(digit)))
		if n.BitLen() > 256 {
			return out, errors.New("base58 value exceeds 32 bytes")
		}
	}
	leadingZeroes := 0
	for leadingZeroes < len(value) && value[leadingZeroes] == '1' {
		leadingZeroes++
	}
	decoded := n.Bytes()
	if leadingZeroes+len(decoded) != len(out) {
		return out, errors.New("base58 value must decode to exactly 32 bytes")
	}
	copy(out[leadingZeroes:], decoded)
	if Encode(out[:]) != value {
		return [32]byte{}, errors.New("base58 value is not canonical")
	}
	return out, nil
}

func Decode64(value string) ([64]byte, error) {
	var out [64]byte
	decoded, err := decode(value, len(out), 88)
	if err != nil {
		return out, err
	}
	copy(out[:], decoded)
	return out, nil
}

func decode(value string, size, maxEncoded int) ([]byte, error) {
	if value == "" {
		return nil, errors.New("base58 value is empty")
	}
	if len(value) > maxEncoded {
		return nil, errors.New("base58 value is too long")
	}
	n := new(big.Int)
	for _, r := range value {
		digit := strings.IndexRune(alphabet, r)
		if digit < 0 {
			return nil, errors.New("base58 value contains an invalid character")
		}
		n.Mul(n, radix)
		n.Add(n, big.NewInt(int64(digit)))
		if n.BitLen() > size*8 {
			return nil, errors.New("base58 value exceeds decoded size")
		}
	}
	leadingZeroes := 0
	for leadingZeroes < len(value) && value[leadingZeroes] == '1' {
		leadingZeroes++
	}
	decoded := n.Bytes()
	if leadingZeroes+len(decoded) != size {
		return nil, errors.New("base58 value has the wrong decoded size")
	}
	out := make([]byte, size)
	copy(out[leadingZeroes:], decoded)
	if Encode(out) != value {
		return nil, errors.New("base58 value is not canonical")
	}
	return out, nil
}

func Encode(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	leadingZeroes := 0
	for leadingZeroes < len(data) && data[leadingZeroes] == 0 {
		leadingZeroes++
	}
	n := new(big.Int).SetBytes(data)
	var encoded []byte
	mod := new(big.Int)
	for n.Sign() > 0 {
		n.QuoRem(n, radix, mod)
		encoded = append(encoded, alphabet[mod.Int64()])
	}
	for i := 0; i < leadingZeroes; i++ {
		encoded = append(encoded, '1')
	}
	for left, right := 0, len(encoded)-1; left < right; left, right = left+1, right-1 {
		encoded[left], encoded[right] = encoded[right], encoded[left]
	}
	return string(encoded)
}
