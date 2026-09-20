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
