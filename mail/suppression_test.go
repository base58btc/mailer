package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func suppressionMail(t *testing.T, job, sub string) *Mail {
	t.Helper()
	m, err := ConvertMailRequest(MailRequest{JobKey: job, Subscription: sub, ToAddr: "fixture@example.test", Title: "Update", TextBody: "body", SendAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSuppressionBeforeSubmissionSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.db")
	ds, err := DatastoreNew(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = ds.Suppress(Suppression{"subscription", "old-opt-in"}); err != nil {
		t.Fatal(err)
	}
	if err = ds.Suppress(Suppression{"job", "cancelled-job"}); err != nil {
		t.Fatal(err)
	}
	ds.Data.Close()
	ds, err = DatastoreNew(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Data.Close()
	for _, m := range []*Mail{suppressionMail(t, "late-job", "old-opt-in"), suppressionMail(t, "cancelled-job", ""), suppressionMail(t, "new-job", "new-opt-in"), suppressionMail(t, "receipt", "")} {
		if err = ds.ScheduleMail(m); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := ds.GetToSendBatch(time.Now(), 10)
	if err != nil || len(jobs) != 2 {
		t.Fatal(jobs, err)
	}
	for _, m := range jobs {
		if m.JobKey != "new-job" && m.JobKey != "receipt" {
			t.Fatal("suppressed mail queued", m.JobKey)
		}
	}
}

func TestSuppressionCancelsSelectedAndInFlightRetries(t *testing.T) {
	for _, inFlight := range []bool{false, true} {
		t.Run(strconv.FormatBool(inFlight), func(t *testing.T) {
			ds := getDatastore(t)
			defer ds.Data.Close()
			m := suppressionMail(t, "release", "generation-1")
			if err := ds.ScheduleMail(m); err != nil {
				t.Fatal(err)
			}
			batch, err := ds.GetToSendBatch(time.Now(), 10)
			if err != nil || len(batch) != 1 {
				t.Fatal(batch, err)
			}
			if inFlight {
				if ok, err := ds.BeginDelivery(m.IdemKey()); err != nil || !ok {
					t.Fatal(ok, err)
				}
			}
			if err := ds.Suppress(Suppression{"subscription", "generation-1"}); err != nil {
				t.Fatal(err)
			}
			if ok, err := ds.BeginDelivery(m.IdemKey()); err != nil || ok {
				t.Fatal("cancelled batch sent", ok, err)
			}
			// Late provider completions cannot restore a cancelled job or its retries.
			ds.RescheduleFailed(m.IdemKey(), 1, 1)
			ds.MarkSent(m.IdemKey())
			ds.ResetInProgress()
			if err := ds.ScheduleMail(m); err != nil {
				t.Fatal(err)
			}
			got, err := ds.GetMail(m.IdemKey())
			if err != nil || got.State != "cancelled" {
				t.Fatal(got, err)
			}
			batch, err = ds.GetToSendBatch(time.Now(), 10)
			if err != nil || len(batch) != 0 {
				t.Fatal(batch, err)
			}
		})
	}
}

func TestSuppressionLeavesSentRecordsAndRollsBack(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	m := suppressionMail(t, "sent", "")
	if err := ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.GetToSendBatch(time.Now(), 10); err != nil {
		t.Fatal(err)
	}
	if ok, err := ds.BeginDelivery(m.IdemKey()); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := ds.BeginDelivery(m.IdemKey()); err != nil || ok {
		t.Fatal("duplicate send claim", ok, err)
	}
	ds.MarkSent(m.IdemKey())
	if err := ds.Suppress(Suppression{"job", "sent"}); err != nil {
		t.Fatal(err)
	}
	got, err := ds.GetMail(m.IdemKey())
	if err != nil || got.State != SENT {
		t.Fatal(got, err)
	}
	m = suppressionMail(t, "rollback", "")
	if err := ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.Data.Exec(`CREATE TRIGGER reject_cancel BEFORE UPDATE ON scheduled WHEN NEW.state='cancelled' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := ds.Suppress(Suppression{"job", "rollback"}); err == nil {
		t.Fatal("accepted failed cancellation")
	}
	var n int
	if err := ds.Data.QueryRow("SELECT count(*) FROM mail_suppressions WHERE suppression_key='rollback'").Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	got, err = ds.GetMail(m.IdemKey())
	if err != nil || got.State != UNSENT {
		t.Fatal(got, err)
	}
}

func TestSuppressionHTTPAuthenticationAndValidation(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	for _, tc := range []struct {
		body     string
		auth, ok bool
	}{
		{`{"scope":"job","key":"release"}`, false, false},
		{`{"scope":"job","key":""}`, true, false},
		{`{"scope":"all","key":"release"}`, true, false},
		{`{"scope":"job","key":"release"} {}`, true, false},
		{`{"scope":"job","key":"release"}`, true, true},
		{`{"scope":"job","key":"release"}`, true, true},
	} {
		r := httptest.NewRequest(http.MethodPut, "/suppression", strings.NewReader(tc.body))
		if tc.auth {
			stamp := strconv.FormatInt(time.Now().Unix(), 10)
			sum := sha256.Sum256([]byte("fixture-secret" + stamp + "/suppressionPUT"))
			r.Header.Set("Authorization", hex.EncodeToString(sum[:]))
			r.Header.Set("X-Base58-Timestamp", stamp)
		}
		w := httptest.NewRecorder()
		SetupRoutes(ds, "fixture-secret").ServeHTTP(w, r)
		var result ReturnVal
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Success != tc.ok {
			t.Fatal(result, err)
		}
	}
}
