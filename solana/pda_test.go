package solana

import "testing"

func TestFindProgramAddressMatchesCanonicalAssociatedTokenAccounts(t *testing.T) {
	owner, err := Decode32("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	if err != nil {
		t.Fatal(err)
	}
	tokenProgram, err := Decode32("TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		mint string
		want string
	}{
		{"So11111111111111111111111111111111111111112", "6ypgTYMnFZxR4iDLZfVQdeWWNjtXM67qGRbbMATRdv3w"},
		{"BRjpCHtyQLNCo8gqRUr8jtdAj5AjPYQaoqbvcZiHok1k", "AxPxVBmYMB44y2RdzLtGQfJgTbUdQ4DeEzy8cZQUmyQv"},
	} {
		mint, err := Decode32(test.mint)
		if err != nil {
			t.Fatal(err)
		}
		got, bump, err := FindProgramAddress([][]byte{
			owner[:], tokenProgram[:], mint[:],
		}, "ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL")
		if err != nil {
			t.Fatal(err)
		}
		if got != test.want || bump != 255 {
			t.Fatalf("address = %s bump = %d, want %s bump 255", got, bump, test.want)
		}
	}
}

func TestFindProgramAddressRejectsInvalidInputs(t *testing.T) {
	if _, _, err := FindProgramAddress(
		[][]byte{make([]byte, 33)}, "11111111111111111111111111111111",
	); err == nil {
		t.Fatal("oversized seed was accepted")
	}
	if _, _, err := FindProgramAddress(nil, "invalid"); err == nil {
		t.Fatal("invalid program was accepted")
	}
	if _, _, err := FindProgramAddress(
		make([][]byte, 16), "11111111111111111111111111111111",
	); err == nil {
		t.Fatal("too many seeds were accepted")
	}
}
