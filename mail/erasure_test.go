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

func TestEraseJobRetainsTombstoneAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.db")
	ds, err := DatastoreNew(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"unsent", "sending", "sent"} {
		m := suppressionMail(t, state, "")
		if err := ds.ScheduleMail(m); err != nil {
			t.Fatal(err)
		}
		if _, err := ds.Data.Exec(`UPDATE scheduled SET state=?,to_name='private name',from_name='private sender',reply_to='private@example.test',html_body='private html',attachments=x'010203',mail_domain='private.test',missive='private missive',unsubscribe_url='https://example.test/private-token' WHERE job_key=?`, state, state); err != nil {
			t.Fatal(err)
		}
		if err := ds.EraseJob(state); err != nil {
			t.Fatal(err)
		}
		got, err := ds.GetMail(m.IdemKey())
		if err != nil {
			t.Fatal(err)
		}
		if got.ToAddr != "" || got.Title != "" || got.TextBody != "" || got.HTMLBody != "" {
			t.Fatal("content remains")
		}
		want := "cancelled"
		if state == "sent" {
			want = "sent"
		}
		if string(got.State) != want {
			t.Fatal(got.State)
		}
		ds.MarkSent(m.IdemKey())
		ds.RescheduleFailed(m.IdemKey(), 1, 1)
	}
	if err := ds.EraseJob("late"); err != nil {
		t.Fatal(err)
	}
	ds.Data.Close()
	ds, err = DatastoreNew(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Data.Close()
	for _, key := range []string{"unsent", "sending", "sent", "late"} {
		if err := ds.ScheduleMail(suppressionMail(t, key, "")); err != nil {
			t.Fatal(err)
		}
		if err := ds.EraseJob(key); err != nil {
			t.Fatal(err)
		}
	}
	var content, count int
	if err := ds.Data.QueryRow(`SELECT count(*) FROM scheduled WHERE to_addr<>'' OR to_name<>'' OR from_addr<>'' OR from_name<>'' OR reply_to<>'' OR title<>'' OR text_body<>'' OR html_body<>'' OR attachments IS NOT NULL OR mail_domain<>'' OR sub IS NOT NULL OR missive IS NOT NULL OR unsubscribe_url<>''`).Scan(&content); err != nil || content != 0 {
		t.Fatal(content, err)
	}
	if err := ds.Data.QueryRow(`SELECT count(*) FROM mail_job_erasures`).Scan(&count); err != nil || count != 4 {
		t.Fatal(count, err)
	}
}
func TestEraseJobRollsBack(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	m := suppressionMail(t, "rollback", "")
	if err := ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.Data.Exec(`CREATE TRIGGER fail_erasure BEFORE INSERT ON mail_job_erasures BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := ds.EraseJob("rollback"); err == nil {
		t.Fatal("erasure unexpectedly succeeded")
	}
	got, err := ds.GetMail(m.IdemKey())
	if err != nil || got.ToAddr != m.ToAddr {
		t.Fatal("partial erasure", got, err)
	}
	var count int
	if err := ds.Data.QueryRow(`SELECT count(*) FROM mail_suppressions WHERE suppression_key='rollback'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial suppression", count, err)
	}
}
func TestErasureAuthorizationAndScope(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	for _, tc := range []struct {
		body       string
		auth, want bool
	}{{`{"scope":"job","key":"allowed"}`, true, true}, {`{"scope":"job","key":"denied"}`, false, false}, {`{"scope":"subscription","key":"denied"}`, true, false}, {`{"scope":"job","key":"x"} {}`, true, false}, {`{"scope":"job","key":"x","extra":true}`, true, false}} {
		r := httptest.NewRequest(http.MethodPut, "/erasure", strings.NewReader(tc.body))
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		r.Header.Set("X-Base58-Timestamp", stamp)
		if tc.auth {
			sum := sha256.Sum256([]byte("fixture-secret" + stamp + "/erasurePUT"))
			r.Header.Set("Authorization", hex.EncodeToString(sum[:]))
		}
		w := httptest.NewRecorder()
		HandleErasure(w, r, ds, "fixture-secret")
		var result struct{ Success bool }
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Success != tc.want {
			t.Fatal(w.Body.String())
		}
	}
}
