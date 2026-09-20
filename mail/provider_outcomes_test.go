package mail

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProviderOutcomesAreBoundMonotonicAndPersistBeforeAcceptance(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	m := suppressionMail(t, "insider:outcome:fixture", "")
	m.Domain = "requested.example"
	if err := ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.GetToSendBatch(time.Now(), 10); err != nil {
		t.Fatal(err)
	}
	// Runtime chose a configured fallback domain; callback association uses it.
	if ok, err := ds.BeginDeliveryForDomain(m.IdemKey(), "actual.example"); err != nil || !ok {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	event := MailgunEvent{ID: "delivered", Event: "delivered", Recipient: m.ToAddr}
	event.Domain.Name = "actual.example"
	event.Variables.QueueID = m.IdemKey()
	event.Message.Headers.MessageID = "message@actual.example"
	if err := ds.ApplyMailgunFeedback(event, strings.Repeat("d", 50), now); err != nil {
		t.Fatal("early delivery lost", err)
	}
	status, err := ds.JobStatuses([]string{m.JobKey})
	if err != nil || status[0].DeliveredAt != 0 {
		t.Fatal("unconfirmed provider identity exposed", err)
	}
	var outcomes int
	if err := ds.Data.Get(&outcomes, `SELECT count(*) FROM mail_provider_outcomes`); err != nil || outcomes != 1 {
		t.Fatal("early callback was not durable", err)
	}
	if err := ds.RecordProviderAcceptance(m.IdemKey(), "actual.example", "<message@actual.example>", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	status, err = ds.JobStatuses([]string{m.JobKey})
	if err != nil || status[0].DeliveredAt != now.Unix() {
		t.Fatal("accepted message did not expose durable callback", status, err)
	}
	for i, kind := range []string{"complained", "delivered", "failed", "failed"} {
		event.Event = kind
		event.ID = kind
		event.Severity = "permanent"
		event.Reason = "bounce"
		event.Delivery.BounceType = "hard"
		if i == 3 {
			event.Reason = "policy"
			event.Delivery.BounceType = ""
		}
		if err := ds.ApplyMailgunFeedback(event, strings.Repeat(string(rune('k'+i)), 50), now.Add(time.Duration(i+2)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	status, err = ds.JobStatuses([]string{m.JobKey})
	if err != nil || status[0].DeliveredAt != now.Unix() || status[0].ComplainedAt == 0 || status[0].BouncedAt == 0 || status[0].FailedAt == 0 {
		t.Fatal("out-of-order outcomes erased evidence", status, err)
	}
	event.Event = "delivered"
	for _, id := range []string{"wrong@actual.example", ""} {
		event.Message.Headers.MessageID = id
		if err := ds.ApplyMailgunFeedback(event, strings.Repeat("x", 50), now); !errors.Is(err, ErrMailgunAssociation) {
			t.Fatal("wrong provider message accepted", err)
		}
	}
	event.Message.Headers.MessageID = "message@actual.example"
	event.Domain.Name = "requested.example"
	if err := ds.ApplyMailgunFeedback(event, strings.Repeat("x", 50), now); !errors.Is(err, ErrMailgunAssociation) {
		t.Fatal("wrong sending domain accepted", err)
	}
	// Ignoring feedback for erased content must not reintroduce suppression addresses.
	if err := ds.EraseJob(m.JobKey); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.Data.Exec(`DELETE FROM mail_recipient_suppressions`); err != nil {
		t.Fatal(err)
	}
	event.Event = "complained"
	event.Domain.Name = "actual.example"
	if err := ds.ApplyMailgunFeedback(event, strings.Repeat("z", 50), now); err != nil {
		t.Fatal(err)
	}
	var suppressions int
	if err := ds.Data.Get(&suppressions, `SELECT count(*) FROM mail_recipient_suppressions`); err != nil || suppressions != 0 {
		t.Fatal("late callback restored erased address", err)
	}
}

func TestProviderOutcomeRollbackAndClassification(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	m := suppressionMail(t, "rollback", "")
	m.Domain = "example.test"
	if err := ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	event := MailgunEvent{ID: "event", Event: "complained", Recipient: m.ToAddr}
	event.Domain.Name = m.Domain
	event.Variables.QueueID = m.IdemKey()
	event.Message.Headers.MessageID = "msg@example.test"
	if _, err := ds.Data.Exec(`CREATE TRIGGER fail_outcome BEFORE INSERT ON mail_provider_outcomes BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := ds.ApplyMailgunFeedback(event, strings.Repeat("r", 50), time.Now()); err == nil {
		t.Fatal("failed result acknowledged")
	}
	for _, table := range []string{"mailgun_feedback_tokens", "mail_provider_outcomes", "mail_recipient_suppressions"} {
		var n int
		if err := ds.Data.Get(&n, `SELECT count(*) FROM `+table); err != nil || n != 0 {
			t.Fatal("partial feedback committed", table, err)
		}
	}
	if _, err := ds.Data.Exec(`DROP TRIGGER fail_outcome`); err != nil {
		t.Fatal(err)
	}
	if err := ds.ApplyMailgunFeedback(event, strings.Repeat("r", 50), time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"opened", "clicked", "accepted"} {
		event.Event = kind
		if event.DeliveryOutcome() != "" {
			t.Fatal("unsupported event recorded", kind)
		}
	}
	event.Event = "failed"
	event.Severity = "temporary"
	if event.DeliveryOutcome() != "" {
		t.Fatal("temporary failure recorded as permanent")
	}
	event.Event = "delivered"
	event.Flags.Test = true
	if event.DeliveryOutcome() != "" {
		t.Fatal("test event counted")
	}
}
