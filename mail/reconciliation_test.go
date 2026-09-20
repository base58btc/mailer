package mail

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReconciliationPreservesActiveJobsAndAuditsAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue with spaces.db")
	ds, err := DatastoreNew(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Data.Close()
	m := suppressionMail(t, "reconciliation:fixture", "")
	if err = ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	if _, err = ds.GetToSendBatch(time.Now(), 10); err != nil {
		t.Fatal(err)
	}
	if ok, err := ds.BeginDeliveryForDomain(m.IdemKey(), "actual.example"); err != nil || !ok {
		t.Fatal(err)
	}
	operator, err := OpenReconciliationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer operator.Data.Close()
	rows, err := operator.UnknownDeliveries("", 100)
	if err != nil || len(rows) != 0 {
		t.Fatal("opening operator connection reset active send", rows, err)
	}
	input := AcceptanceReconciliation{OperationID: "case-1", QueueID: m.IdemKey(), Actor: "operator", EvidenceRef: "provider-support:ticket-1", Domain: "actual.example", MessageID: "<message@actual.example>"}
	if err = operator.ReconcileProviderAcceptance(input, time.Now()); err == nil {
		t.Fatal("active send reconciled")
	}
	ds.ResetInProgress()
	rows, err = operator.UnknownDeliveries("", 1)
	if err != nil || len(rows) != 1 || rows[0].QueueID != m.IdemKey() {
		t.Fatal(rows, err)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), m.ToAddr) || strings.Contains(string(raw), m.TextBody) {
		t.Fatal("listing leaked content")
	}
	if rest, err := operator.UnknownDeliveries(rows[0].QueueID, 1); err != nil || len(rest) != 0 {
		t.Fatal("invalid pagination", rest, err)
	}
	wrong := input
	wrong.Domain = "other.example"
	if err = operator.ReconcileProviderAcceptance(wrong, time.Now()); err == nil {
		t.Fatal("wrong domain accepted")
	}
	if _, err = ds.Data.Exec(`CREATE TRIGGER fail_operator_audit BEFORE INSERT ON mail_delivery_reconciliations BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err = operator.ReconcileProviderAcceptance(input, time.Now()); err == nil {
		t.Fatal("audit failure accepted")
	}
	status, err := ds.JobStatuses([]string{m.JobKey})
	if err != nil || status[0].AcceptedAt != 0 || status[0].QueueState != "delivery_unknown" {
		t.Fatal("partial reconciliation committed", status, err)
	}
	if _, err = ds.Data.Exec(`DROP TRIGGER fail_operator_audit`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	if err = operator.ReconcileProviderAcceptance(input, now); err != nil {
		t.Fatal(err)
	}
	input.MessageID = "message@actual.example"
	if err = operator.ReconcileProviderAcceptance(input, now.Add(time.Hour)); err != nil {
		t.Fatal("exact replay failed", err)
	}
	wrong = input
	wrong.EvidenceRef = "different"
	if err = operator.ReconcileProviderAcceptance(wrong, now); err == nil {
		t.Fatal("audit conflict accepted")
	}
	status, err = ds.JobStatuses([]string{m.JobKey})
	if err != nil || status[0].AcceptedAt != now.Unix() || status[0].QueueState != "sent" {
		t.Fatal(status, err)
	}
	if jobs, err := ds.GetToSendBatch(time.Now(), 10); err != nil || len(jobs) != 0 {
		t.Fatal("reconciliation caused resend", jobs, err)
	}
	for _, query := range []string{`UPDATE mail_delivery_reconciliations SET actor='other'`, `DELETE FROM mail_delivery_reconciliations`} {
		if _, err = ds.Data.Exec(query); err == nil {
			t.Fatal("audit mutated")
		}
	}
}

func TestReconciliationAfterErasureDoesNotRestoreContent(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	m := suppressionMail(t, "erased:reconciliation", "")
	if err := ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.GetToSendBatch(time.Now(), 10); err != nil {
		t.Fatal(err)
	}
	if ok, err := ds.BeginDeliveryForDomain(m.IdemKey(), "actual.example"); err != nil || !ok {
		t.Fatal(err)
	}
	if err := ds.EraseJob(m.JobKey); err != nil {
		t.Fatal(err)
	}
	input := AcceptanceReconciliation{OperationID: "case-erased", QueueID: m.IdemKey(), Actor: "operator", EvidenceRef: "verified-acceptance", Domain: "actual.example", MessageID: "message@actual.example"}
	if err := ds.ReconcileProviderAcceptance(input, time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := ds.JobStatuses([]string{m.JobKey})
	if err != nil || !rows[0].Erased || rows[0].QueueState != "cancelled" || rows[0].AcceptedAt == 0 {
		t.Fatal(rows, err)
	}
	var content int
	if err = ds.Data.Get(&content, `SELECT count(*) FROM scheduled WHERE to_addr<>'' OR text_body<>'' OR html_body<>''`); err != nil || content != 0 {
		t.Fatal("restored personal data", content, err)
	}
}

func unknownReconciliationFixture(t *testing.T) (*Datastore, *Mail, AcceptanceReconciliation) {
	t.Helper()
	ds := getDatastore(t)
	t.Cleanup(func() { ds.Data.Close() })
	m := suppressionMail(t, "retry:fixture", "subscription:fixture")
	if err := ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.GetToSendBatch(time.Now(), 10); err != nil {
		t.Fatal(err)
	}
	if ok, err := ds.BeginDeliveryForDomain(m.IdemKey(), "actual.example"); err != nil || !ok {
		t.Fatal(err)
	}
	ds.ResetInProgress()
	return ds, m, AcceptanceReconciliation{OperationID: "retry-operation", QueueID: m.IdemKey(), Actor: "operator", EvidenceRef: "provider-support:confirmed-not-accepted", Domain: "actual.example"}
}

func TestNonAcceptanceReconciliationIsAtomicAndCannotReplayRetry(t *testing.T) {
	ds, m, input := unknownReconciliationFixture(t)
	if _, err := ds.Data.Exec(`CREATE TRIGGER fail_retry_audit BEFORE INSERT ON mail_delivery_reconciliations BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := ds.ReconcileProviderNonAcceptance(input, time.Now()); err == nil {
		t.Fatal("audit failure accepted")
	}
	rows, err := ds.JobStatuses([]string{m.JobKey})
	if err != nil || rows[0].QueueState != "delivery_unknown" {
		t.Fatal("retry escaped audit transaction", rows, err)
	}
	if _, err = ds.Data.Exec(`DROP TRIGGER fail_retry_audit`); err != nil {
		t.Fatal(err)
	}
	if err = ds.ReconcileProviderNonAcceptance(input, time.Now()); err != nil {
		t.Fatal(err)
	}
	batch, err := ds.GetToSendBatch(time.Now(), 10)
	if err != nil || len(batch) != 1 {
		t.Fatal("confirmed retry not eligible", batch, err)
	}
	if ok, err := ds.BeginDeliveryForDomain(m.IdemKey(), "actual.example"); err != nil || !ok {
		t.Fatal(err)
	}
	ds.ResetInProgress() // A second interruption needs a fresh investigation.
	if err = ds.ReconcileProviderNonAcceptance(input, time.Now()); err != nil {
		t.Fatal("exact replay failed", err)
	}
	rows, err = ds.JobStatuses([]string{m.JobKey})
	if err != nil || rows[0].QueueState != "delivery_unknown" {
		t.Fatal("old evidence requeued a later interruption", rows, err)
	}
	if batch, err = ds.GetToSendBatch(time.Now(), 10); err != nil || len(batch) != 0 {
		t.Fatal("replayed operation caused delivery", batch, err)
	}
	conflict := input
	conflict.EvidenceRef = "changed"
	if err = ds.ReconcileProviderNonAcceptance(conflict, time.Now()); err == nil {
		t.Fatal("conflicting evidence accepted")
	}
	conflict = input
	conflict.MessageID = "provider-id"
	if err = ds.ReconcileProviderAcceptance(conflict, time.Now()); err == nil {
		t.Fatal("operation changed action")
	}
}

func TestNonAcceptanceRejectsContradictoryOrIneligibleJobs(t *testing.T) {
	for _, scenario := range []string{"accepted", "early-outcome", "job-suppression", "subscription-suppression", "recipient-suppression", "erased", "exhausted", "active", "wrong-domain", "message-id"} {
		t.Run(scenario, func(t *testing.T) {
			ds, m, input := unknownReconciliationFixture(t)
			var err error
			switch scenario {
			case "accepted":
				err = ds.RecordProviderAcceptance(m.IdemKey(), "actual.example", "message@actual.example", time.Now())
			case "early-outcome":
				event := MailgunEvent{ID: "early", Event: "delivered", Recipient: m.ToAddr}
				event.Domain.Name = "actual.example"
				event.Variables.QueueID = m.IdemKey()
				event.Message.Headers.MessageID = "early@actual.example"
				err = ds.ApplyMailgunFeedback(event, strings.Repeat("r", 50), time.Now())
			case "job-suppression":
				err = ds.Suppress(Suppression{Scope: "job", Key: m.JobKey})
			case "subscription-suppression":
				err = ds.Suppress(Suppression{Scope: "subscription", Key: "subscription:fixture"})
			case "recipient-suppression":
				_, err = ds.Data.Exec(`INSERT INTO mail_recipient_suppressions(domain,recipient,reason,created_at) VALUES(?,?,'complaint',?)`, "actual.example", strings.ToLower(m.ToAddr), time.Now().Unix())
			case "erased":
				err = ds.EraseJob(m.JobKey)
			case "exhausted":
				_, err = ds.Data.Exec(`UPDATE scheduled SET try_count=20`)
			case "active":
				_, err = ds.Data.Exec(`UPDATE scheduled SET state='sending'`)
			case "wrong-domain":
				input.Domain = "other.example"
			case "message-id":
				input.MessageID = "accepted@actual.example"
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = ds.ReconcileProviderNonAcceptance(input, time.Now()); err == nil {
				t.Fatal("unsafe retry accepted")
			}
			var n int
			if err = ds.Data.Get(&n, `SELECT count(*) FROM mail_delivery_reconciliations`); err != nil || n != 0 {
				t.Fatal("rejected retry recorded", n, err)
			}
		})
	}
}

func TestSuppressionAfterConfirmedRetryStillBlocksSending(t *testing.T) {
	ds, m, input := unknownReconciliationFixture(t)
	if err := ds.ReconcileProviderNonAcceptance(input, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.GetToSendBatch(time.Now(), 10); err != nil {
		t.Fatal(err)
	}
	if err := ds.Suppress(Suppression{Scope: "job", Key: m.JobKey}); err != nil {
		t.Fatal(err)
	}
	if ok, err := ds.BeginDeliveryForDomain(m.IdemKey(), "actual.example"); err != nil || ok {
		t.Fatal("late suppression bypassed", ok, err)
	}
}
