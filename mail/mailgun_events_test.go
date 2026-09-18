package mail

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestVerifyMailgunEvents(t *testing.T) {
	now := time.Now()
	stamp := strconv.FormatInt(now.Unix(), 10)
	token := strings.Repeat("a", 50)
	mac := hmac.New(sha256.New, []byte("signing-key"))
	mac.Write([]byte(stamp + token))
	signature := hex.EncodeToString(mac.Sum(nil))
	for _, tc := range []struct {
		name, key, stamp, signature string
		valid                       bool
	}{
		{"valid", "signing-key", stamp, signature, true},
		{"wrong key", "wrong", stamp, signature, false},
		{"unset key", "", stamp, signature, false},
		{"wrong signature", "signing-key", stamp, strings.Repeat("0", 64), false},
		{"expired", "signing-key", "1", signature, false},
		{"future", "signing-key", strconv.FormatInt(now.Add(time.Hour).Unix(), 10), signature, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"signature": map[string]string{"timestamp": tc.stamp, "token": token, "signature": tc.signature}, "event-data": map[string]any{"id": "event", "event": "failed", "severity": "permanent", "reason": "bounce", "recipient": "fixture@example.test", "delivery-status": map[string]string{"bounce-type": "hard"}, "domain": map[string]string{"name": "example.test"}, "user-variables": map[string]string{"mailer_queue_id": "queue"}}})
			event, gotToken, err := VerifyMailgunEvent(httptest.NewRequest("POST", "/webhooks/mailgun", strings.NewReader(string(body))), tc.key, now)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
			if tc.valid && (event.RecipientSuppressionReason() != "hard_bounce" || gotToken != token || event.Variables.QueueID != "queue") {
				t.Fatal(event, gotToken)
			}
			if !tc.valid && (event != nil || gotToken != "") {
				t.Fatal("unverified payload returned")
			}
		})
	}
	for _, body := range []string{"{}", "{} {}", strings.Repeat("x", 65537)} {
		if _, _, err := VerifyMailgunEvent(httptest.NewRequest("POST", "/", strings.NewReader(body)), "signing-key", now); err == nil {
			t.Fatal("accepted invalid body")
		}
	}
}

func TestMailgunSuppressionClassification(t *testing.T) {
	e := MailgunEvent{Event: "failed", Severity: "permanent", Reason: "bounce"}
	if e.RecipientSuppressionReason() != "" {
		t.Fatal("unknown bounce classified as hard")
	}
	e.Delivery.BounceType = "hard"
	if e.RecipientSuppressionReason() != "hard_bounce" {
		t.Fatal(e)
	}
	e.Severity = "temporary"
	if e.RecipientSuppressionReason() != "" {
		t.Fatal("temporary failure suppressed")
	}
	e.Event = "complained"
	if e.RecipientSuppressionReason() != "complaint" {
		t.Fatal(e)
	}
	e.Flags.Test = true
	if e.RecipientSuppressionReason() != "" {
		t.Fatal("test event suppressed live recipient")
	}
}
