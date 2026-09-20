package mail

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// RecordProviderAcceptance commits provider evidence and queue completion together.
// Cancellation/erasure may win while a send is in flight; evidence is retained
// without restoring canceled content or making the job eligible for another send.
func (ds *Datastore) RecordProviderAcceptance(key, domain, messageID string, now time.Time) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if key == "" || domain == "" || len(domain) > 253 || normalizeProviderMessageID(messageID) == "" || len(messageID) > 512 || strings.ContainsAny(messageID, "\r\n\x00") || now.IsZero() {
		return errors.New("invalid provider acceptance")
	}
	tx, err := ds.Data.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.Get(&state, `SELECT state FROM scheduled WHERE idem_key=?`, key); err != nil {
		return err
	}
	var existing struct {
		Domain string `db:"provider_domain"`
		ID     string `db:"provider_message_id"`
	}
	err = tx.Get(&existing, `SELECT provider_domain,provider_message_id FROM mail_provider_acceptances WHERE idem_key=?`, key)
	if err == nil {
		if existing.Domain != domain || normalizeProviderMessageID(existing.ID) != normalizeProviderMessageID(messageID) {
			return errors.New("provider acceptance conflicts with recorded evidence")
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if state != "sending" && state != "inprog" && state != "cancelled" && state != "delivery_unknown" {
		return errors.New("mail is not awaiting provider acceptance")
	}
	if _, err := tx.Exec(`INSERT INTO mail_provider_acceptances(idem_key,provider_domain,provider_message_id,accepted_at) VALUES(?,?,?,?)`, key, domain, messageID, now.Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE scheduled SET state='sent' WHERE idem_key=? AND state IN ('sending','inprog','delivery_unknown')`, key); err != nil {
		return err
	}
	return tx.Commit()
}

type JobStatus struct {
	DeliveredAt       int64  `json:"delivered_at,omitempty" db:"delivered_at"`
	FailedAt          int64  `json:"failed_at,omitempty" db:"failed_at"`
	BouncedAt         int64  `json:"bounced_at,omitempty" db:"bounced_at"`
	ComplainedAt      int64  `json:"complained_at,omitempty" db:"complained_at"`
	JobKey            string `json:"job_key" db:"job_key"`
	QueueState        string `json:"queue_state" db:"queue_state"`
	ProviderDomain    string `json:"provider_domain,omitempty" db:"provider_domain"`
	ProviderMessageID string `json:"provider_message_id,omitempty" db:"provider_message_id"`
	AcceptedAt        int64  `json:"accepted_at,omitempty" db:"accepted_at"`
	Erased            bool   `json:"erased" db:"erased"`
}

func (ds *Datastore) JobStatuses(keys []string) ([]JobStatus, error) {
	if len(keys) == 0 || len(keys) > 100 {
		return nil, errors.New("request between 1 and 100 job keys")
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if key == "" || len(key) > 512 || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\r\n\x00") || seen[key] {
			return nil, errors.New("invalid or duplicate job key")
		}
		seen[key] = true
	}
	tx, err := ds.Data.Beginx()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := make([]JobStatus, 0, len(keys))
	for _, key := range keys {
		var rows []JobStatus
		// Legacy callers can reuse a job key with distinct message hashes, so keep
		// each provider acceptance instead of guessing one message per logical key.
		err := tx.Select(&rows, `SELECT coalesce(o.delivered_at,0) AS delivered_at,coalesce(o.failed_at,0) AS failed_at,coalesce(o.bounced_at,0) AS bounced_at,coalesce(o.complained_at,0) AS complained_at,s.job_key,s.state AS queue_state,coalesce(a.provider_domain,'') AS provider_domain,coalesce(a.provider_message_id,'') AS provider_message_id,coalesce(a.accepted_at,0) AS accepted_at,EXISTS(SELECT 1 FROM mail_job_erasures e WHERE e.job_key=s.job_key) AS erased FROM scheduled s LEFT JOIN mail_provider_acceptances a ON a.idem_key=s.idem_key LEFT JOIN mail_provider_outcomes o ON o.idem_key=a.idem_key AND o.provider_domain=a.provider_domain AND o.provider_message_id=trim(trim(a.provider_message_id),'<>') WHERE s.job_key=? ORDER BY s.idem_key LIMIT 101`, key)
		if err != nil {
			return nil, err
		}
		if len(rows) > 100 {
			return nil, errors.New("too many messages for job key")
		}
		if len(rows) == 0 {
			rows = []JobStatus{{JobKey: key, QueueState: "unknown"}}
		}
		result = append(result, rows...)
	}
	return result, tx.Commit()
}
func HandleJobStatus(w http.ResponseWriter, r *http.Request, ds *Datastore, secret string) {
	w.Header().Set("Cache-Control", "no-store")
	if err := checkKey(secret, r); err != nil {
		returnErr(w, errors.New("not authorized"))
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	decoder.DisallowUnknownFields()
	var request struct {
		JobKeys []string `json:"job_keys"`
	}
	if err := decoder.Decode(&request); err != nil {
		returnErr(w, errors.New("invalid job status request"))
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		returnErr(w, errors.New("invalid job status request"))
		return
	}
	rows, err := ds.JobStatuses(request.JobKeys)
	if err != nil {
		returnErr(w, errors.New("job status unavailable"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Success bool        `json:"success"`
		Code    int         `json:"code"`
		Jobs    []JobStatus `json:"jobs"`
	}{true, http.StatusOK, rows})
}

func normalizeProviderMessageID(id string) string {
	id = strings.TrimSpace(id)
	if strings.HasPrefix(id, "<") && strings.HasSuffix(id, ">") {
		id = id[1 : len(id)-1]
	}
	if id == "" || len(id) > 512 || strings.ContainsAny(id, "<> \r\n\x00") {
		return ""
	}
	return id
}
