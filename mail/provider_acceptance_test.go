package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProviderAcceptanceAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	ds, err := DatastoreNew(path)
	if err != nil {
		t.Fatal(err)
	}
	m := suppressionMail(t, "insider:campaign:fixture", "")
	if err := ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	batch, err := ds.GetToSendBatch(time.Now(), 10)
	if err != nil || len(batch) != 1 {
		t.Fatal(err)
	}
	if ok, err := ds.BeginDelivery(m.IdemKey()); err != nil || !ok {
		t.Fatal(err)
	}
	// Persistence failure leaves no partial receipt or false 'sent' state.
	if _, err := ds.Data.Exec(`CREATE TRIGGER fail_acceptance BEFORE INSERT ON mail_provider_acceptances BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := ds.RecordProviderAcceptance(m.IdemKey(), "mail.example", "<accepted@mail.example>", now); err == nil {
		t.Fatal("failed receipt accepted")
	}
	if _, err := ds.Data.Exec(`DROP TRIGGER fail_acceptance`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := ds.Data.Get(&n, `SELECT count(*) FROM mail_provider_acceptances`); err != nil || n != 0 {
		t.Fatal("partial acceptance committed", err)
	}
	ds.Data.Close()
	ds, err = DatastoreNew(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Data.Close()
	statuses, err := ds.JobStatuses([]string{m.JobKey})
	if err != nil || statuses[0].QueueState != "delivery_unknown" || statuses[0].AcceptedAt != 0 {
		t.Fatal("ambiguous delivery was not isolated", statuses, err)
	}
	if jobs, err := ds.GetToSendBatch(time.Now(), 10); err != nil || len(jobs) != 0 {
		t.Fatal("uncertain delivery automatically resent", err)
	}
	// Recovery can record verified provider evidence without another send.
	if err := ds.RecordProviderAcceptance(m.IdemKey(), "mail.example", "<accepted@mail.example>", now); err != nil {
		t.Fatal(err)
	}
	if err := ds.RecordProviderAcceptance(m.IdemKey(), "MAIL.EXAMPLE", "<accepted@mail.example>", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := ds.RecordProviderAcceptance(m.IdemKey(), "mail.example", "<different@mail.example>", now); err == nil {
		t.Fatal("provider evidence overwritten")
	}
	statuses, err = ds.JobStatuses([]string{m.JobKey, "missing"})
	if err != nil || statuses[0].QueueState != "sent" || statuses[0].AcceptedAt != now.Unix() || statuses[0].ProviderMessageID != "<accepted@mail.example>" || statuses[1].QueueState != "unknown" {
		t.Fatal(statuses, err)
	}
	if err := ds.EraseJob(m.JobKey); err != nil {
		t.Fatal(err)
	}
	statuses, err = ds.JobStatuses([]string{m.JobKey})
	if err != nil || !statuses[0].Erased || statuses[0].ProviderMessageID == "" {
		t.Fatal("erasure lost provider evidence", statuses, err)
	}
	body, _ := json.Marshal(statuses)
	if strings.Contains(string(body), m.ToAddr) || strings.Contains(string(body), m.TextBody) {
		t.Fatal("status contains mail content")
	}
}

func TestProviderAcceptanceAfterCancellation(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	m := suppressionMail(t, "cancelled-in-flight", "")
	if err := ds.ScheduleMail(m); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.GetToSendBatch(time.Now(), 10); err != nil {
		t.Fatal(err)
	}
	if ok, err := ds.BeginDelivery(m.IdemKey()); err != nil || !ok {
		t.Fatal(err)
	}
	if err := ds.EraseJob(m.JobKey); err != nil {
		t.Fatal(err)
	}
	if err := ds.RecordProviderAcceptance(m.IdemKey(), "mail.example", "<late@mail.example>", time.Now()); err != nil {
		t.Fatal(err)
	}
	status, err := ds.JobStatuses([]string{m.JobKey})
	if err != nil || status[0].QueueState != "cancelled" || !status[0].Erased || status[0].AcceptedAt == 0 {
		t.Fatal(status, err)
	}
	stored, err := ds.GetMail(m.IdemKey())
	if err != nil || stored.ToAddr != "" || stored.HTMLBody != "" {
		t.Fatal("late acceptance restored erased content", err)
	}
}

func TestJobStatusHTTP(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	for _, tc := range []struct {
		body          string
		auth, success bool
	}{{`{"job_keys":["missing"]}`, true, true}, {`{"job_keys":["missing"]}`, false, false}, {`{"job_keys":[]}`, true, false}, {`{"job_keys":["x","x"]}`, true, false}, {`{"job_keys":["x"]} {}`, true, false}} {
		r := httptest.NewRequest("POST", "/job-status", strings.NewReader(tc.body))
		if tc.auth {
			stamp := strconv.FormatInt(time.Now().Unix(), 10)
			sum := sha256.Sum256([]byte("secret" + stamp + "/job-statusPOST"))
			r.Header.Set("X-Base58-Timestamp", stamp)
			r.Header.Set("Authorization", hex.EncodeToString(sum[:]))
		}
		w := httptest.NewRecorder()
		SetupRoutes(ds, "secret").ServeHTTP(w, r)
		var response struct {
			Success bool        `json:"success"`
			Jobs    []JobStatus `json:"jobs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Success != tc.success || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(response, err)
		}
		if !tc.success && len(response.Jobs) != 0 {
			t.Fatal("unauthorized/invalid status leaked")
		}
	}
}
