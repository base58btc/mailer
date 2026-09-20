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

func TestMailgunWebhookCommitsBeforeAcknowledging(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	m, err := ConvertMailRequest(MailRequest{JobKey: "original", ToAddr: "fixture@example.test", Title: "Update", TextBody: "Body", SendAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	m.Domain = "example.test"
	if err = ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	event := MailgunEvent{ID: "http-event", Recipient: m.ToAddr, Event: "complained"}
	event.Domain.Name = m.Domain
	event.Variables.QueueID = m.IdemKey()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	token := strings.Repeat("h", 50)
	mac := hmac.New(sha256.New, []byte("signing"))
	mac.Write([]byte(timestamp + token))
	signature := hex.EncodeToString(mac.Sum(nil))
	request := func(key, domain string) int {
		body, _ := json.Marshal(map[string]any{"signature": map[string]string{"timestamp": timestamp, "token": token, "signature": signature}, "event-data": event})
		r := httptest.NewRequest("POST", "/webhooks/mailgun", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		SetupRoutesWithMailgun(ds, "queue-secret", "", key, []string{domain}).ServeHTTP(w, r)
		return w.Code
	}
	if status := request("", m.Domain); status != 404 {
		t.Fatal(status)
	}
	if status := request("wrong", m.Domain); status != 406 {
		t.Fatal(status)
	}
	if status := request("signing", "other.test"); status != 406 {
		t.Fatal(status)
	}
	if _, err = ds.Data.Exec(`CREATE TRIGGER fail_feedback BEFORE INSERT ON mail_recipient_suppressions BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if status := request("signing", m.Domain); status != 503 {
		t.Fatal(status)
	}
	if _, err = ds.Data.Exec(`DROP TRIGGER fail_feedback`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if status := request("signing", m.Domain); status != 200 {
			t.Fatal(status)
		}
	}
	var n int
	if err = ds.Data.Get(&n, `SELECT count(*) FROM mail_recipient_suppressions`); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	// Delivery-only callbacks must be persisted, not merely acknowledged by the
	// old suppression-only route. They may precede the send response.
	event.ID, event.Event = "delivery-http-event", "delivered"
	event.Message.Headers.MessageID = "delivered@example.test"
	token = strings.Repeat("v", 50)
	mac = hmac.New(sha256.New, []byte("signing"))
	mac.Write([]byte(timestamp + token))
	signature = hex.EncodeToString(mac.Sum(nil))
	if status := request("signing", m.Domain); status != 200 {
		t.Fatal("delivery callback failed", status)
	}
	if err := ds.Data.Get(&n, `SELECT count(*) FROM mail_provider_outcomes WHERE delivered_at>0`); err != nil || n != 1 {
		t.Fatal("delivery callback acknowledged without persistence", n, err)
	}

}
