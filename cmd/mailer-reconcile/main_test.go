package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/base58btc/mailer/mail"
)

func TestOperatorCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	ds, err := mail.DatastoreNew(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Data.Close()
	m, err := mail.ConvertMailRequest(mail.MailRequest{JobKey: "cli-fixture", ToAddr: "fixture@example.test", Title: "subject", TextBody: "private body", SendAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	if _, err = ds.GetToSendBatch(time.Now(), 10); err != nil {
		t.Fatal(err)
	}
	if ok, err := ds.BeginDeliveryForDomain(m.IdemKey(), "actual.example"); err != nil || !ok {
		t.Fatal(err)
	}
	ds.ResetInProgress()
	var output, diagnostics bytes.Buffer
	if err = run([]string{"-db", path}, &output, &diagnostics); err != nil || !strings.Contains(output.String(), m.IdemKey()) || strings.Contains(output.String(), m.ToAddr) {
		t.Fatal(output.String(), err)
	}
	args := []string{"-db", path, "-action", "confirm-accepted", "-operation-id", "cli-operation", "-queue-id", m.IdemKey(), "-domain", "actual.example", "-message-id", "message@actual.example", "-actor", "operator", "-evidence-ref", "support:ticket"}
	if err = run(args, &output, &diagnostics); err == nil {
		t.Fatal("mutation without explicit confirmation")
	}
	status, err := ds.JobStatuses([]string{m.JobKey})
	if err != nil || status[0].QueueState != "delivery_unknown" {
		t.Fatal(status, err)
	}
	output.Reset()
	if err = run(append(args, "-confirm"), &output, &diagnostics); err != nil || !strings.Contains(output.String(), `"recorded":true`) {
		t.Fatal(output.String(), err)
	}
	if err = run(append(args, "-confirm"), &output, &diagnostics); err != nil {
		t.Fatal("safe retry failed", err)
	}
}

func TestRetryCommandRequiresConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	ds, err := mail.DatastoreNew(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Data.Close()
	m, err := mail.ConvertMailRequest(mail.MailRequest{JobKey: "cli-retry", ToAddr: "fixture@example.test", Title: "subject", TextBody: "body", SendAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	if _, err = ds.GetToSendBatch(time.Now(), 10); err != nil {
		t.Fatal(err)
	}
	if ok, err := ds.BeginDeliveryForDomain(m.IdemKey(), "actual.example"); err != nil || !ok {
		t.Fatal(err)
	}
	ds.ResetInProgress()
	args := []string{"-db", path, "-action", "confirm-not-accepted", "-operation-id", "cli-retry", "-queue-id", m.IdemKey(), "-domain", "actual.example", "-actor", "operator", "-evidence-ref", "provider:support-confirmation"}
	var out bytes.Buffer
	if err = run(args, &out, &out); err == nil {
		t.Fatal("retry did not require confirmation")
	}
	rows, err := ds.JobStatuses([]string{m.JobKey})
	if err != nil || rows[0].QueueState != "delivery_unknown" {
		t.Fatal(rows, err)
	}
	if err = run(append(args, "-confirm"), &out, &out); err != nil {
		t.Fatal(err)
	}
	rows, err = ds.JobStatuses([]string{m.JobKey})
	if err != nil || rows[0].QueueState != "failed" {
		t.Fatal(rows, err)
	}
}
