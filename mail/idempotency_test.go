package mail

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// Exercise the authenticated HTTP boundary as well as the unique database key:
// callers cannot distinguish a lost success response from an unaccepted request.
func TestJobResubmissionAcknowledgesWithoutReset(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	job := MailRequest{JobKey: "alerts:release:123", ToAddr: "fixture@example.test", Title: "New release", TextBody: "original", SendAt: float64(time.Now().Unix())}
	submit := func(job MailRequest) ReturnVal {
		t.Helper()
		body, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPut, "/job", bytes.NewReader(body))
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		sum := sha256.Sum256([]byte("fixture-secret" + stamp + "/job" + http.MethodPut))
		r.Header.Set("Authorization", hex.EncodeToString(sum[:]))
		r.Header.Set("X-Base58-Timestamp", stamp)
		w := httptest.NewRecorder()
		HandleMailJob(w, r, ds, "fixture-secret")
		var result ReturnVal
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || !result.Success || result.Code != http.StatusOK {
			t.Fatalf("not accepted: status=%d result=%+v", w.Code, result)
		}
		return result
	}
	submit(job)
	m, err := ConvertMailRequest(job)
	if err != nil {
		t.Fatal(err)
	}
	key := m.IdemKey()
	job.TextBody = "changed retry body"
	job.SendAt += 3600
	for _, state := range []ScheduleState{UNSENT, INPROG, FAILED, SENT} {
		if _, err := ds.Data.Exec("UPDATE scheduled SET state=?,try_count=7 WHERE idem_key=?", state, key); err != nil {
			t.Fatal(err)
		}
		submit(job)
		var count, attempts int
		var gotState, body string
		var sendAt int64
		if err := ds.Data.QueryRow("SELECT count(*) FROM scheduled").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if err := ds.Data.QueryRow("SELECT state,try_count,text_body,send_at FROM scheduled WHERE idem_key=?", key).Scan(&gotState, &attempts, &body, &sendAt); err != nil {
			t.Fatal(err)
		}
		if count != 1 || gotState != string(state) || attempts != 7 || body != "original" || sendAt != time.Time(m.SendAt).Unix() {
			t.Fatalf("repeat changed queue: count=%d state=%s attempts=%d body=%s sendAt=%d", count, gotState, attempts, body, sendAt)
		}
	}
	// An independent recipient is an independent job, even with the same key.
	job.ToAddr = "second@example.test"
	submit(job)
	var count int
	if err := ds.Data.QueryRow("SELECT count(*) FROM scheduled").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}
