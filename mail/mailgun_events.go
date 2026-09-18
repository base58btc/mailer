package mail

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

// MailgunEvent contains only fields needed to bind provider feedback to a
// queued message. Never persist or log the complete webhook payload.
type MailgunEvent struct {
	ID        string `json:"id"`
	Event     string `json:"event"`
	Severity  string `json:"severity"`
	Reason    string `json:"reason"`
	Recipient string `json:"recipient"`
	Domain    struct {
		Name string `json:"name"`
	} `json:"domain"`
	Variables struct {
		QueueID string `json:"mailer_queue_id"`
	} `json:"user-variables"`
	Delivery struct {
		BounceType string `json:"bounce-type"`
	} `json:"delivery-status"`
	Flags struct {
		Test       bool `json:"is-test-mode"`
		SystemTest bool `json:"is-system-test"`
	} `json:"flags"`
}

// RecipientSuppressionReason intentionally excludes temporary failures and
// permanent failures caused by exhausted retries or sender policy problems.
func (e MailgunEvent) RecipientSuppressionReason() string {
	if e.Flags.Test || e.Flags.SystemTest {
		return ""
	}
	if e.Event == "complained" {
		return "complaint"
	}
	if e.Event == "failed" && e.Severity == "permanent" && e.Reason == "bounce" && e.Delivery.BounceType == "hard" {
		return "hard_bounce"
	}
	return ""
}

// VerifyMailgunEvent follows Mailgun's webhook signing-key protocol. Its token
// must additionally be deduplicated transactionally when applying an event.
// The generous one-day window accommodates documented provider retry delays.
func VerifyMailgunEvent(r *http.Request, key string, now time.Time) (*MailgunEvent, string, error) {
	invalid := errors.New("invalid Mailgun webhook")
	if key == "" || r.Method != http.MethodPost {
		return nil, "", invalid
	}
	var payload struct {
		Signature struct {
			Timestamp json.Number `json:"timestamp"`
			Token     string      `json:"token"`
			Signature string      `json:"signature"`
		} `json:"signature"`
		Event json.RawMessage `json:"event-data"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 65537))
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &payload) != nil {
		return nil, "", invalid
	}
	stamp, err := strconv.ParseInt(string(payload.Signature.Timestamp), 10, 64)
	if err != nil || stamp < now.Add(-24*time.Hour).Unix() || stamp > now.Add(5*time.Minute).Unix() || len(payload.Signature.Token) != 50 {
		return nil, "", invalid
	}
	signature, err := hex.DecodeString(payload.Signature.Signature)
	if err != nil {
		return nil, "", invalid
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(string(payload.Signature.Timestamp) + payload.Signature.Token))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, "", invalid
	}
	var event MailgunEvent
	if json.Unmarshal(payload.Event, &event) != nil || event.ID == "" || len(event.ID) > 200 {
		return nil, "", invalid
	}
	// Events from unrelated mail without our variable may encode user-variables as
	// an empty array. They are rejected rather than guessing a queue association.
	return &event, payload.Signature.Token, nil
}
