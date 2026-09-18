package mail

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type Suppression struct {
	Scope string `json:"scope"`
	Key   string `json:"key"`
}

// Suppress permanently retires a job or subscription generation. New opt-ins
// must use a new subscription key; delayed requests cannot restore the old one.
func (ds *Datastore) Suppress(s Suppression) error {
	if (s.Scope != "job" && s.Scope != "subscription") || strings.TrimSpace(s.Key) == "" || len(s.Key) > 512 || strings.ContainsAny(s.Key, "\r\n\x00") {
		return errors.New("invalid suppression")
	}
	tx, err := ds.Data.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO mail_suppressions(scope,suppression_key,created_at) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.Scope, s.Key, time.Now().Unix()); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE scheduled SET state='cancelled' WHERE state<>'sent' AND ((?='job' AND job_key=?) OR (?='subscription' AND sub=?))`, s.Scope, s.Key, s.Scope, s.Key); err != nil {
		return err
	}
	return tx.Commit()
}

// BeginDelivery rechecks cancellation after batch selection and establishes the
// send boundary. A provider call already in flight cannot be recalled.
func (ds *Datastore) BeginDelivery(idemKey string) (bool, error) {
	return ds.BeginDeliveryForDomain(idemKey, "")
}

// Runtime passes the actual provider domain, including any configured fallback.
func (ds *Datastore) BeginDeliveryForDomain(idemKey, domain string) (bool, error) {
	tx, err := ds.Data.Beginx()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE scheduled SET state='cancelled' WHERE idem_key=? AND state='inprog'
 AND EXISTS(SELECT 1 FROM mail_recipient_suppressions s WHERE s.recipient=lower(scheduled.to_addr)
 AND s.domain=lower(CASE WHEN ?='' THEN coalesce(scheduled.mail_domain,'') ELSE ? END))`, idemKey, domain, domain); err != nil {
		return false, err
	}
	result, err := tx.Exec(`UPDATE scheduled SET state='sending' WHERE idem_key=? AND state='inprog'
 AND NOT EXISTS(SELECT 1 FROM mail_suppressions WHERE (scope='job' AND suppression_key=scheduled.job_key) OR (scope='subscription' AND suppression_key=scheduled.sub))`, idemKey)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, tx.Commit()
}

func HandleSuppression(w http.ResponseWriter, r *http.Request, ds *Datastore, secret string) {
	if err := checkKey(secret, r); err != nil {
		returnErr(w, errors.New("not authorized"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var s Suppression
	if err := d.Decode(&s); err != nil {
		returnErr(w, errors.New("invalid suppression request"))
		return
	}
	if err := d.Decode(new(any)); err != io.EOF {
		returnErr(w, errors.New("invalid suppression request"))
		return
	}
	if err := ds.Suppress(s); err != nil {
		returnErr(w, errors.New("suppression not confirmed"))
		return
	}
	returnSuccess(w)
}
