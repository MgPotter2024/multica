package webpush

import (
	"crypto/elliptic"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strings"
)

const (
	maxEndpointLength = 4096
	maxP256dhLength   = 256
	maxAuthLength     = 128
)

type SubscriptionKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type Subscription struct {
	Endpoint string           `json:"endpoint"`
	Keys     SubscriptionKeys `json:"keys"`
}

type Config struct {
	publicKey  string
	privateKey string
	subject    string
	enabled    bool
}

func ConfigFromEnv() (Config, error) {
	return NewConfig(
		os.Getenv("VAPID_PUBLIC_KEY"),
		os.Getenv("VAPID_PRIVATE_KEY"),
		os.Getenv("VAPID_SUBJECT"),
	)
}

func NewConfig(publicKey, privateKey, subject string) (Config, error) {
	publicKey = strings.TrimSpace(publicKey)
	privateKey = strings.TrimSpace(privateKey)
	subject = strings.TrimSpace(subject)
	if publicKey == "" && privateKey == "" && subject == "" {
		return Config{}, nil
	}
	if publicKey == "" || privateKey == "" || subject == "" {
		return Config{}, errors.New("VAPID_PUBLIC_KEY, VAPID_PRIVATE_KEY, and VAPID_SUBJECT must all be set")
	}

	publicBytes, err := decodeBase64URL(publicKey, "VAPID public key")
	if err != nil || len(publicBytes) != 65 {
		return Config{}, errors.New("VAPID_PUBLIC_KEY must be a valid P-256 public key")
	}
	publicX, publicY := elliptic.Unmarshal(elliptic.P256(), publicBytes)
	if publicX == nil || publicY == nil {
		return Config{}, errors.New("VAPID_PUBLIC_KEY must be a valid P-256 public key")
	}

	privateBytes, err := decodeBase64URL(privateKey, "VAPID private key")
	if err != nil || len(privateBytes) != 32 {
		return Config{}, errors.New("VAPID_PRIVATE_KEY must be a valid P-256 private key")
	}
	privateX, privateY := elliptic.P256().ScalarBaseMult(privateBytes)
	if privateX == nil || privateY == nil || privateX.Sign() == 0 || privateY.Sign() == 0 {
		return Config{}, errors.New("VAPID_PRIVATE_KEY must be a valid P-256 private key")
	}
	if privateX.Cmp(publicX) != 0 || privateY.Cmp(publicY) != 0 {
		return Config{}, errors.New("VAPID public and private keys do not match")
	}

	normalizedSubject, err := normalizeSubject(subject)
	if err != nil {
		return Config{}, err
	}

	return Config{
		publicKey:  publicKey,
		privateKey: privateKey,
		subject:    normalizedSubject,
		enabled:    true,
	}, nil
}

func (c Config) Enabled() bool {
	return c.enabled
}

func (c Config) PublicKey() string {
	if !c.enabled {
		return ""
	}
	return c.publicKey
}

func normalizeSubject(subject string) (string, error) {
	if strings.HasPrefix(subject, "mailto:") {
		subject = strings.TrimPrefix(subject, "mailto:")
	}
	if strings.HasPrefix(subject, "https://") {
		u, err := url.Parse(subject)
		if err != nil || u.Hostname() == "" || u.User != nil {
			return "", errors.New("VAPID_SUBJECT must be a mailto address or HTTPS URL")
		}
		return subject, nil
	}
	address, err := mail.ParseAddress(subject)
	if err != nil || address.Address != subject {
		return "", errors.New("VAPID_SUBJECT must be a mailto address or HTTPS URL")
	}
	return subject, nil
}

func ValidateSubscription(subscription Subscription) error {
	if err := ValidateEndpoint(subscription.Endpoint); err != nil {
		return err
	}
	if subscription.Keys.P256dh == "" || len(subscription.Keys.P256dh) > maxP256dhLength {
		return errors.New("invalid p256dh key")
	}
	publicKey, err := decodeBase64URL(subscription.Keys.P256dh, "p256dh key")
	if err != nil || len(publicKey) != 65 {
		return errors.New("invalid p256dh key")
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), publicKey)
	if x == nil || y == nil {
		return errors.New("invalid p256dh key")
	}

	if subscription.Keys.Auth == "" || len(subscription.Keys.Auth) > maxAuthLength {
		return errors.New("invalid auth key")
	}
	authKey, err := decodeBase64URL(subscription.Keys.Auth, "auth key")
	if err != nil || len(authKey) != 16 {
		return errors.New("invalid auth key")
	}
	return nil
}

func ValidateEndpoint(endpoint string) error {
	if endpoint == "" || len(endpoint) > maxEndpointLength || endpoint != strings.TrimSpace(endpoint) {
		return errors.New("invalid push endpoint")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("invalid push endpoint")
	}
	hostname := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return errors.New("invalid push endpoint")
	}
	if net.ParseIP(hostname) != nil {
		return errors.New("invalid push endpoint")
	}
	return nil
}

func decodeBase64URL(value, field string) ([]byte, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return nil, fmt.Errorf("invalid %s", field)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	if err != nil {
		return nil, fmt.Errorf("invalid %s", field)
	}
	return decoded, nil
}
