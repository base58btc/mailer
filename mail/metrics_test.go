package mail

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsAuthenticationAndQueueStates(t *testing.T) {
	for _, token := range []string{"", "wrong"} {
		r := httptest.NewRequest("GET", "/metrics", nil)
		r.Header.Set("Authorization", "Bearer wrong")
		w := httptest.NewRecorder()
		configured := "read-only-secret"
		want := 401
		if token == "" {
			configured = ""
			want = 404
		}
		SetupRoutesWithMetrics(nil, "queue-secret", configured).ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	ds := getDatastore(t)
	defer ds.Data.Close()
	states := []string{"unsent", "inprog", "sending", "failed", "failed", "sent", "cancelled"}
	for i, state := range states {
		attempts := 1
		if i == 4 {
			attempts = 20
		}
		_, err := ds.Data.Exec(`INSERT INTO scheduled(idem_key,job_key,to_addr,title,html_body,text_body,send_at,state,try_count) VALUES(?,?,?,?,?,?,unixepoch()-600,?,?)`, fmt.Sprint(i), "private-job", "private@example.test", "private-title", "private-body", "private-body", state, attempts)
		if err != nil {
			t.Fatal(err)
		}
	}
	scrape := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/metrics", nil)
		r.Header.Set("Authorization", "Bearer read-only-secret")
		w := httptest.NewRecorder()
		SetupRoutesWithMetrics(ds, "queue-secret", "read-only-secret").ServeHTTP(w, r)
		return w
	}
	w := scrape()
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, value := range []string{`mailer_jobs{state="pending"} 1`, `mailer_jobs{state="in_flight"} 2`, `mailer_jobs{state="retrying"} 1`, `mailer_jobs{state="exhausted"} 1`, `mailer_jobs{state="provider_accepted"} 1`, `mailer_jobs{state="cancelled"} 1`} {
		if !strings.Contains(w.Body.String(), value) {
			t.Fatal("missing", value, w.Body.String())
		}
	}
	if strings.Contains(w.Body.String(), "private") {
		t.Fatal("private queue data exposed")
	}
	var pending int
	if err := ds.Data.Get(&pending, `SELECT count(*) FROM scheduled WHERE state='unsent'`); err != nil || pending != 1 {
		t.Fatal("scrape mutated queue", pending, err)
	}
	if _, err := ds.Data.Exec(`DELETE FROM scheduled`); err != nil {
		t.Fatal(err)
	}
	if w = scrape(); w.Code != 200 || !strings.Contains(w.Body.String(), "mailer_oldest_attempt_overdue_seconds 0\n") {
		t.Fatal(w.Code, w.Body.String())
	}
	ds.Data.Close()
	if w = scrape(); w.Code != 503 || strings.Contains(w.Body.String(), "sql") {
		t.Fatal(w.Code, w.Body.String())
	}
}
