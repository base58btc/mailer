package mail

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// A separate read-only token allows ordinary Prometheus scrapes without granting
// queue mutation credentials. Queue acceptance is not inbox delivery.
func HandleMetrics(w http.ResponseWriter, r *http.Request, ds *Datastore, token string) {
	w.Header().Set("Cache-Control", "no-store")
	if token == "" {
		http.NotFound(w, r)
		return
	}
	got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	want := sha256.Sum256([]byte("Bearer " + token))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var counts struct {
		Pending, InFlight, Retrying, Exhausted, Accepted, Cancelled int64
		Overdue                                                     int64
	}
	err := ds.Data.QueryRowContext(ctx, `SELECT
 coalesce(sum(state='unsent'),0),coalesce(sum(state IN ('inprog','sending')),0),
 coalesce(sum(state='failed' AND try_count<20),0),coalesce(sum(state='failed' AND try_count>=20),0),
 coalesce(sum(state='sent'),0),coalesce(sum(state='cancelled'),0),
 coalesce(max(CASE WHEN state IN ('unsent','inprog','sending') OR (state='failed' AND try_count<20) THEN max(0,?-send_at) ELSE 0 END),0)
 FROM scheduled`, time.Now().Unix()).Scan(&counts.Pending, &counts.InFlight, &counts.Retrying, &counts.Exhausted, &counts.Accepted, &counts.Cancelled, &counts.Overdue)
	if err != nil {
		http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
		return
	}
	var out strings.Builder
	out.WriteString("# HELP mailer_jobs Stored jobs by fixed delivery state; accepted means provider acceptance, not inbox delivery\n# TYPE mailer_jobs gauge\n")
	for _, item := range []struct {
		name string
		n    int64
	}{{"pending", counts.Pending}, {"in_flight", counts.InFlight}, {"retrying", counts.Retrying}, {"exhausted", counts.Exhausted}, {"provider_accepted", counts.Accepted}, {"cancelled", counts.Cancelled}} {
		fmt.Fprintf(&out, "mailer_jobs{state=%q} %d\n", item.name, item.n)
	}
	fmt.Fprintf(&out, "# HELP mailer_oldest_attempt_overdue_seconds Age past the scheduled time of the oldest outstanding delivery attempt; retries reset the scheduled time\n# TYPE mailer_oldest_attempt_overdue_seconds gauge\nmailer_oldest_attempt_overdue_seconds %d\n", counts.Overdue)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprint(w, out.String())
}
