package mail

import (
	"strings"
	"testing"
	"time"
)

func TestMailgunFeedbackSuppressesPendingFutureAndSelectedJobs(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	now := time.Now()
	makeMail := func(key, domain string) *Mail {
		m, err := ConvertMailRequest(MailRequest{JobKey: key, ToAddr: "fixture@example.test", Title: "Update", TextBody: "Body", SendAt: float64(now.Add(-time.Minute).Unix())})
		if err != nil {
			t.Fatal(err)
		}
		m.Domain = domain
		if err = ds.ScheduleMail(m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	original := makeMail("original", "example.test")
	pending := makeMail("pending", "example.test")
	fallback := makeMail("fallback", "")
	other := makeMail("other-domain", "other.test")
	batch, err := ds.GetToSendBatch(now, 10)
	if err != nil || len(batch) != 4 {
		t.Fatal(len(batch), err)
	}
	if allowed, err := ds.BeginDeliveryForDomain(original.IdemKey(), "example.test"); err != nil || !allowed {
		t.Fatal(allowed, err)
	}
	ds.MarkSent(original.IdemKey())
	e := MailgunEvent{ID: "event", Recipient: original.ToAddr, Event: "failed", Severity: "permanent", Reason: "bounce"}
	e.Domain.Name = "example.test"
	e.Variables.QueueID = original.IdemKey()
	e.Delivery.BounceType = "hard"
	token := strings.Repeat("a", 50)
	if err = ds.ApplyMailgunFeedback(e, token, now); err != nil {
		t.Fatal(err)
	}
	if err = ds.ApplyMailgunFeedback(e, token, now); err != nil {
		t.Fatal("replay", err)
	}
	for _, m := range []*Mail{pending, fallback} {
		if allowed, err := ds.BeginDeliveryForDomain(m.IdemKey(), "example.test"); err != nil || allowed {
			t.Fatal("suppressed recipient sent", allowed, err)
		}
	}
	if allowed, err := ds.BeginDeliveryForDomain(other.IdemKey(), "other.test"); err != nil || !allowed {
		t.Fatal("other domain suppressed", allowed, err)
	}
	future := makeMail("future", "example.test")
	var count int
	if err = ds.Data.Get(&count, `SELECT count(*) FROM scheduled WHERE idem_key=?`, future.IdemKey()); err != nil || count != 0 {
		t.Fatal("future job accepted into delivery queue", count, err)
	}
	if err = ds.Data.Get(&count, `SELECT count(*) FROM mailgun_feedback_tokens`); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	saved, err := ds.GetMail(original.IdemKey())
	if err != nil || saved.State != "sent" {
		t.Fatal(saved, err)
	}
}

func TestMailgunFeedbackAssociationAndRollback(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	now := time.Now()
	m, err := ConvertMailRequest(MailRequest{JobKey: "original", ToAddr: "fixture@example.test", Title: "Update", TextBody: "Body", SendAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	m.Domain = "example.test"
	if err = ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	e := MailgunEvent{ID: "event", Recipient: "wrong@example.test", Event: "complained"}
	e.Domain.Name = m.Domain
	e.Variables.QueueID = m.IdemKey()
	if err = ds.ApplyMailgunFeedback(e, strings.Repeat("b", 50), now); err != ErrMailgunAssociation {
		t.Fatal(err)
	}
	e.Recipient = m.ToAddr
	if _, err = ds.Data.Exec(`CREATE TRIGGER fail_feedback BEFORE INSERT ON mail_recipient_suppressions BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err = ds.ApplyMailgunFeedback(e, strings.Repeat("b", 50), now); err == nil {
		t.Fatal("expected transaction failure")
	}
	var count int
	if err = ds.Data.Get(&count, `SELECT count(*) FROM mailgun_feedback_tokens`); err != nil || count != 0 {
		t.Fatal("failed event consumed replay token", count, err)
	}
	if _, err = ds.Data.Exec(`DROP TRIGGER fail_feedback`); err != nil {
		t.Fatal(err)
	}
	if err = ds.ApplyMailgunFeedback(e, strings.Repeat("b", 50), now); err != nil {
		t.Fatal("retry", err)
	}
}
