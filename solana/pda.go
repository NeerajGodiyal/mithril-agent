package solana

import (
	"crypto/sha256"
	"errors"
	"math/big"
)

const programAddressMarker = "ProgramDerivedAddress"

var (
	edwardsPrime = func() *big.Int {
		prime := new(big.Int).Lsh(big.NewInt(1), 255)
		return prime.Sub(prime, big.NewInt(19))
	}()
	edwardsD = func() *big.Int {
		numerator := new(big.Int).Neg(big.NewInt(121665))
		numerator.Mod(numerator, edwardsPrime)
		inverse := new(big.Int).ModInverse(big.NewInt(121666), edwardsPrime)
		return new(big.Int).Mod(new(big.Int).Mul(numerator, inverse), edwardsPrime)
	}()
)

// FindProgramAddress derives the first off-curve Solana program address.
func FindProgramAddress(seeds [][]byte, program string) (string, uint8, error) {
	if len(seeds) >= 16 {
		return "", 0, errors.New("program address has too many seeds")
	}
	programKey, err := Decode32(program)
	if err != nil {
		return "", 0, errors.New("program address program is invalid")
	}
	for bump := 255; bump >= 0; bump-- {
		withBump := make([][]byte, 0, len(seeds)+1)
		withBump = append(withBump, seeds...)
		withBump = append(withBump, []byte{byte(bump)})
		address, onCurve, err := createProgramAddress(withBump, programKey)
		if err != nil {
			return "", 0, err
		}
		if !onCurve {
			return Encode(address[:]), uint8(bump), nil
		}
	}
	return "", 0, errors.New("program address bump is unavailable")
}

func createProgramAddress(seeds [][]byte, program [32]byte) ([32]byte, bool, error) {
	hash := sha256.New()
	for _, seed := range seeds {
		if len(seed) > 32 {
			return [32]byte{}, false, errors.New("program address seed exceeds 32 bytes")
		}
		_, _ = hash.Write(seed)
	}
	_, _ = hash.Write(program[:])
	_, _ = hash.Write([]byte(programAddressMarker))
	var address [32]byte
	copy(address[:], hash.Sum(nil))
	return address, encodedEdwardsPoint(address), nil
}

func encodedEdwardsPoint(encoded [32]byte) bool {
	sign := encoded[31] >> 7
	encoded[31] &= 0x7f
	for left, right := 0, len(encoded)-1; left < right; left, right = left+1, right-1 {
		encoded[left], encoded[right] = encoded[right], encoded[left]
	}
	y := new(big.Int).SetBytes(encoded[:])
	if y.Cmp(edwardsPrime) >= 0 {
		return false
	}
	ySquared := new(big.Int).Mul(y, y)
	ySquared.Mod(ySquared, edwardsPrime)
	numerator := new(big.Int).Sub(ySquared, big.NewInt(1))
	numerator.Mod(numerator, edwardsPrime)
	denominator := new(big.Int).Mul(edwardsD, ySquared)
	denominator.Add(denominator, big.NewInt(1)).Mod(denominator, edwardsPrime)
	inverse := new(big.Int).ModInverse(denominator, edwardsPrime)
	if inverse == nil {
		return false
	}
	xSquared := new(big.Int).Mul(numerator, inverse)
	xSquared.Mod(xSquared, edwardsPrime)
	x := new(big.Int).ModSqrt(xSquared, edwardsPrime)
	return x != nil && (x.Sign() != 0 || sign == 0)
}
