package pricesource

import (
	"errors"
	"strconv"
	"strings"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

func decimalMicros(raw string) (uint64, bool, error) {
	if raw == "" || strings.ContainsAny(raw, "eE+-") {
		return 0, false, errors.New("invalid decimal")
	}
	whole, fraction, found := strings.Cut(raw, ".")
	if !found {
		fraction = ""
	}
	if whole == "" || len(whole) > 7 || len(fraction) > 24 {
		return 0, false, errors.New("invalid decimal")
	}
	for _, part := range []string{whole, fraction} {
		for _, char := range part {
			if char < '0' || char > '9' {
				return 0, false, errors.New("invalid decimal")
			}
		}
	}
	wholeValue, err := strconv.ParseUint(whole, 10, 64)
	if err != nil || wholeValue > pricetrigger.MaxPriceMicros/1_000_000 {
		return 0, false, errors.New("invalid decimal")
	}
	padded := fraction + strings.Repeat("0", 6)
	fractionValue, err := strconv.ParseUint(padded[:6], 10, 64)
	if err != nil {
		return 0, false, errors.New("invalid decimal")
	}
	value := wholeValue*1_000_000 + fractionValue
	rounded := len(fraction) > 6 && strings.Trim(fraction[6:], "0") != ""
	if value == 0 || value > pricetrigger.MaxPriceMicros {
		return 0, false, errors.New("invalid decimal")
	}
	return value, rounded, nil
}
