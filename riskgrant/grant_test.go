package riskgrant

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestVerifyRejectsLifetimeThatOverflowsDuration(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	digest := strings.Repeat("0", 64)
	binding := Binding{
		ActionID:             digest,
		ProfileFingerprint:   digest,
		MessageSHA256:        digest,
		RequestSHA256:        digest,
		FeeLamports:          1,
		LastValidBlockHeight: 1,
	}
	claims := Claims{
		Version:              Version,
		Domain:               Domain,
		KeyID:                "risk-key",
		IssuedAtUnix:         now.Unix(),
		ExpiresAtUnix:        now.Unix() + 9_223_372_037,
		ActionID:             binding.ActionID,
		ProfileFingerprint:   binding.ProfileFingerprint,
		MessageSHA256:        binding.MessageSHA256,
		RequestSHA256:        binding.RequestSHA256,
		FeeLamports:          binding.FeeLamports,
		LastValidBlockHeight: binding.LastValidBlockHeight,
	}
	message, err := signedMessage(claims)
	if err != nil {
		t.Fatal(err)
	}
	grant := Grant{
		Claims:          claims,
		SignatureBase64: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, message)),
	}
	if err := Verify(publicKey, "risk-key", binding, grant, now); err == nil {
		t.Fatal("accepted a signed grant beyond the maximum lifetime")
	}
}

func TestVerifyBindsEveryGrantField(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	binding := testBinding()
	grant, err := Sign(privateKey, "risk-key", binding, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(publicKey, "risk-key", binding, grant, now); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Binding){
		"action":  func(value *Binding) { value.ActionID = strings.Repeat("1", 64) },
		"profile": func(value *Binding) { value.ProfileFingerprint = strings.Repeat("1", 64) },
		"message": func(value *Binding) { value.MessageSHA256 = strings.Repeat("1", 64) },
		"request": func(value *Binding) { value.RequestSHA256 = strings.Repeat("1", 64) },
		"fee":     func(value *Binding) { value.FeeLamports++ },
		"height":  func(value *Binding) { value.LastValidBlockHeight++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := binding
			mutate(&changed)
			if err := Verify(publicKey, "risk-key", changed, grant, now); err == nil {
				t.Fatal("changed binding was accepted")
			}
		})
	}

	wrongVersion := grant
	wrongVersion.Claims.Version = 1
	wrongVersion.Claims.Domain = "mithril-agent/risk-grant-v1"
	message, err := signedMessage(wrongVersion.Claims)
	if err != nil {
		t.Fatal(err)
	}
	wrongVersion.SignatureBase64 = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	if err := Verify(publicKey, "risk-key", binding, wrongVersion, now); err == nil {
		t.Fatal("grant with the wrong version and domain was accepted")
	}
}

func testBinding() Binding {
	return Binding{
		ActionID: strings.Repeat("0", 64), ProfileFingerprint: strings.Repeat("2", 64),
		MessageSHA256: strings.Repeat("3", 64), RequestSHA256: strings.Repeat("4", 64),
		FeeLamports: 5_000, LastValidBlockHeight: 250,
	}
}
