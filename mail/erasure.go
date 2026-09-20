package mail

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// EraseJob suppresses future submissions and removes stored message content in
// one transaction. Delivery evidence remains; provider copies are outside this API.
func (ds *Datastore) EraseJob(key string) error {
	if key == "" || strings.TrimSpace(key) != key || len(key) > 512 || strings.ContainsAny(key, "\r\n\x00") {
		return errors.New("invalid erasure key")
	}
	tx, err := ds.Data.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err = tx.Exec(`INSERT INTO mail_suppressions(scope,suppression_key,created_at) VALUES('job',?,?) ON CONFLICT DO NOTHING`, key, now); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE scheduled SET to_addr='',to_name='',from_addr='',from_name='',reply_to='',title='',html_body='',text_body='',attachments=NULL,mail_domain='',sub=NULL,missive=NULL,unsubscribe_url='',state=CASE WHEN state='sent' THEN 'sent' ELSE 'cancelled' END WHERE job_key=?`, key); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO mail_job_erasures(job_key,erased_at) VALUES(?,?) ON CONFLICT DO NOTHING`, key, now); err != nil {
		return err
	}
	return tx.Commit()
}
func HandleErasure(w http.ResponseWriter, r *http.Request, ds *Datastore, secret string) {
	w.Header().Set("Cache-Control", "no-store")
	if err := checkKey(secret, r); err != nil {
		returnErr(w, errors.New("not authorized"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request Suppression
	if err := decoder.Decode(&request); err != nil {
		returnErr(w, errors.New("invalid erasure request"))
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF || request.Scope != "job" {
		returnErr(w, errors.New("invalid erasure request"))
		return
	}
	if err := ds.EraseJob(request.Key); err != nil {
		returnErr(w, errors.New("erasure not confirmed"))
		return
	}
	returnSuccess(w)
}
