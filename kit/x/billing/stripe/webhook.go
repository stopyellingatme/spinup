package stripe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultTolerance is the maximum age or future offset of a webhook signature.
const DefaultTolerance = 300 * time.Second

// SignatureHeader signs a payload for tests and the fake.
func SignatureHeader(payload []byte, secret string, t time.Time) string {
	timestamp := strconv.FormatInt(t.Unix(), 10)
	return "t=" + timestamp + ",v1=" + hex.EncodeToString(signature(payload, secret, timestamp))
}

func signature(payload []byte, secret, timestamp string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

// VerifyWebhook verifies a signature with every configured secret and decodes the event.
func VerifyWebhook(payload []byte, header string, secrets []string, now time.Time) (Event, error) {
	reject := func(reason string) (Event, error) {
		return Event{}, fmt.Errorf("verify Stripe webhook: %s\nTry: check the webhook secrets, signature, and clock", reason)
	}
	if strings.TrimSpace(header) == "" {
		return reject("missing signature header")
	}
	if len(secrets) == 0 {
		return reject("no webhook secrets")
	}
	for _, secret := range secrets {
		if strings.TrimSpace(secret) == "" {
			return reject("blank webhook secret")
		}
	}
	var timestamp string
	var signatures [][]byte
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			if timestamp != "" {
				return reject("multiple timestamps")
			}
			timestamp = value
		case "v1":
			if decoded, err := hex.DecodeString(value); err == nil {
				signatures = append(signatures, decoded)
			}
		}
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return reject("missing or invalid timestamp")
	}
	age := now.Sub(time.Unix(seconds, 0))
	if age > DefaultTolerance || age < -DefaultTolerance {
		return reject("timestamp outside tolerance")
	}
	if len(signatures) == 0 {
		return reject("missing v1 signature")
	}
	matched := false
	for _, secret := range secrets {
		expected := signature(payload, secret, timestamp)
		for _, candidate := range signatures {
			if hmac.Equal(expected, candidate) {
				matched = true
			}
		}
	}
	if !matched {
		return reject("signature does not match")
	}
	var event Event
	if json.Unmarshal(payload, &event) != nil || event.ID == "" || event.Type == "" {
		return reject("invalid JSON event")
	}
	return event, nil
}
