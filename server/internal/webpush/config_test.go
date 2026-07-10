package webpush

import (
	"bytes"
	"crypto/elliptic"
	"encoding/base64"
	"strings"
	"testing"
)

func testVAPIDKeys() (string, string) {
	privateBytes := bytes.Repeat([]byte{1}, 32)
	x, y := elliptic.P256().ScalarBaseMult(privateBytes)
	publicBytes := elliptic.Marshal(elliptic.P256(), x, y)
	return base64.RawURLEncoding.EncodeToString(publicBytes),
		base64.RawURLEncoding.EncodeToString(privateBytes)
}

func testSubscription() Subscription {
	publicKey, _ := testVAPIDKeys()
	auth := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 16))
	return Subscription{
		Endpoint: "https://updates.push.services.mozilla.com/wpush/v2/test-endpoint",
		Keys: SubscriptionKeys{
			P256dh: publicKey,
			Auth:   auth,
		},
	}
}

func TestNewConfigDisabledWhenUnset(t *testing.T) {
	cfg, err := NewConfig("", "", "")
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Enabled() {
		t.Fatal("empty VAPID config must be disabled")
	}
	if cfg.PublicKey() != "" {
		t.Fatalf("disabled public key = %q, want empty", cfg.PublicKey())
	}
}

func TestNewConfigValidatesCompleteKeyPairWithoutLeakingPrivateKey(t *testing.T) {
	publicKey, privateKey := testVAPIDKeys()

	cfg, err := NewConfig(publicKey, privateKey, "mailto:ops@example.com")
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if !cfg.Enabled() {
		t.Fatal("complete VAPID config must be enabled")
	}
	if cfg.PublicKey() != publicKey {
		t.Fatalf("public key = %q, want configured key", cfg.PublicKey())
	}

	secret := "private-key-must-not-appear"
	_, err = NewConfig(publicKey, secret, "mailto:ops@example.com")
	if err == nil {
		t.Fatal("invalid private key must fail")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked private key: %v", err)
	}

	if _, err := NewConfig(publicKey, "", "mailto:ops@example.com"); err == nil {
		t.Fatal("partial VAPID config must fail")
	}
}

func TestValidateSubscription(t *testing.T) {
	valid := testSubscription()
	if err := ValidateSubscription(valid); err != nil {
		t.Fatalf("valid subscription rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Subscription)
	}{
		{name: "http endpoint", mutate: func(s *Subscription) { s.Endpoint = "http://push.example.com/sub" }},
		{name: "localhost endpoint", mutate: func(s *Subscription) { s.Endpoint = "https://localhost/sub" }},
		{name: "ip endpoint", mutate: func(s *Subscription) { s.Endpoint = "https://127.0.0.1/sub" }},
		{name: "credentialed endpoint", mutate: func(s *Subscription) { s.Endpoint = "https://user:pass@push.example.com/sub" }},
		{name: "oversized endpoint", mutate: func(s *Subscription) { s.Endpoint = "https://push.example.com/" + strings.Repeat("x", 4096) }},
		{name: "invalid p256dh", mutate: func(s *Subscription) { s.Keys.P256dh = "not-base64" }},
		{name: "invalid auth", mutate: func(s *Subscription) { s.Keys.Auth = "not-base64" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			subscription := valid
			tc.mutate(&subscription)
			if err := ValidateSubscription(subscription); err == nil {
				t.Fatal("invalid subscription accepted")
			}
		})
	}
}
