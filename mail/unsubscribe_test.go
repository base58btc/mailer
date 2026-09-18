package mail

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/mailgun/mailgun-go/v4"
)

func TestUnsubscribeSurvivesQueueAndProviderRendering(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	value := "https://alerts.btcpp.dev/unsubscribe?id=123&token=fixture"
	m, err := ConvertMailRequest(MailRequest{JobKey: "release-1", ToAddr: "fixture@example.test", Title: "Update", TextBody: "Release link and footer", UnsubscribeURL: value, SendAt: float64(time.Now().Add(-time.Minute).Unix())})
	if err != nil {
		t.Fatal(err)
	}
	if err = ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	// Retry cannot replace the original unsubscribe target.
	m.UnsubscribeURL = "https://other.example.test/unsubscribe"
	if err = ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	queued, err := ds.GetToSendBatch(time.Now(), 10)
	if err != nil || len(queued) != 1 {
		t.Fatal(queued, err)
	}
	m = queued[0]
	if m.UnsubscribeURL != value {
		t.Fatal(m.UnsubscribeURL)
	}
	mg, err := buildMailgunMessage(mailgun.NewMailgun("example.test", "fixture"), m)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := buildSendGridMessage(m, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, headers := range []map[string]string{mg.GetHeaders(), sg.Headers} {
		if headers["List-Unsubscribe"] != "<"+value+">" || headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
			t.Fatal(headers)
		}
	}
	// Transactional receipts without an opt-out URL stay free of these headers.
	m.UnsubscribeURL = ""
	mg, err = buildMailgunMessage(mailgun.NewMailgun("example.test", "fixture"), m)
	if err != nil {
		t.Fatal(err)
	}
	sg, err = buildSendGridMessage(m, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(mg.GetHeaders()) != 0 || len(sg.Headers) != 0 {
		t.Fatal("transactional mail gained unsubscribe headers")
	}
}

func TestInvalidUnsubscribeURLsRejectedBeforeQueueAndDelivery(t *testing.T) {
	for _, value := range []string{"http://example.test/unsubscribe", "https://example.test/u\r\nBcc: victim@example.test", "https://user:secret@example.test/u", "https:///missing-host", "https://example.test/u#fragment", "https://example.test/u> <https://other.test"} {
		if _, err := ConvertMailRequest(MailRequest{TextBody: "body", UnsubscribeURL: value}); err == nil {
			t.Errorf("accepted %q", value)
		}
		m := &Mail{UnsubscribeURL: value}
		if _, err := buildMailgunMessage(mailgun.NewMailgun("example.test", "fixture"), m); err == nil {
			t.Errorf("Mailgun accepted %q", value)
		}
		if _, err := buildSendGridMessage(m, false); err == nil {
			t.Errorf("SendGrid accepted %q", value)
		}
	}
}

func TestUnsubscribeMigrationPreservesExistingReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range append(append([]string{}, db_setup_exec...), db_migration_exec[:4]...) {
		if _, err = old.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = old.Exec(`UPDATE db_metadata SET value='3' WHERE key='migrations'; INSERT INTO scheduled(idem_key,job_key,to_addr,title,html_body,text_body,send_at,state,try_count,mail_domain) VALUES('old','receipt','fixture@example.test','Receipt','','Paid',1,'sent',2,'')`); err != nil {
		t.Fatal(err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	ds, err := DatastoreNew(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Data.Close()
	jobs, err := ds.GetJob("receipt")
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	if jobs[0].UnsubscribeURL != "" || jobs[0].State != SENT || jobs[0].TryCount != 2 {
		t.Fatal(jobs[0])
	}
}
