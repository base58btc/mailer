package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
)

var ErrMailgunAssociation = errors.New("Mailgun feedback does not match a queued message")

// ApplyMailgunFeedback must receive an authenticated event from an allowed
// provider domain. Replay identity and recipient suppression commit together.
func (ds *Datastore) ApplyMailgunFeedback(e MailgunEvent, token string, now time.Time) error {
	if e.ID == "" || e.Domain.Name == "" || e.Recipient == "" || len(token) != 50 || len(e.Variables.QueueID) != 64 {
		return ErrMailgunAssociation
	}
	if _, err := hex.DecodeString(e.Variables.QueueID); err != nil {
		return ErrMailgunAssociation
	}
	domain := strings.ToLower(e.Domain.Name)
	recipient := strings.ToLower(e.Recipient)
	digest := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(digest[:])
	tx, err := ds.Data.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var replay int
	if err = tx.Get(&replay, `SELECT count(*) FROM mailgun_feedback_tokens WHERE token_hash=?`, hash); err != nil {
		return err
	}
	if replay > 0 {
		return tx.Commit()
	}
	var matched int
	if err = tx.Get(&matched, `SELECT count(*) FROM scheduled WHERE idem_key=? AND lower(to_addr)=? AND (mail_domain IS NULL OR mail_domain='' OR lower(mail_domain)=?)`, e.Variables.QueueID, recipient, domain); err != nil {
		return err
	}
	if matched != 1 {
		return ErrMailgunAssociation
	}
	if _, err = tx.Exec(`INSERT INTO mailgun_feedback_tokens(token_hash,event_id,domain,created_at) VALUES(?,?,?,?)`, hash, e.ID, domain, now.Unix()); err != nil {
		return err
	}
	if reason := e.RecipientSuppressionReason(); reason != "" {
		if _, err = tx.Exec(`INSERT INTO mail_recipient_suppressions(domain,recipient,reason,created_at) VALUES(?,?,?,?) ON CONFLICT(domain,recipient) DO NOTHING`, domain, recipient, reason, now.Unix()); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE scheduled SET state='cancelled' WHERE state<>'sent' AND lower(to_addr)=? AND lower(mail_domain)=?`, recipient, domain); err != nil {
			return err
		}
	}
	// Signature timestamps expire after one day. Keep replay tokens for two days,
	// pruning a bounded batch without retaining complete provider payloads.
	if _, err = tx.Exec(`DELETE FROM mailgun_feedback_tokens WHERE token_hash IN (SELECT token_hash FROM mailgun_feedback_tokens WHERE created_at<? ORDER BY created_at LIMIT 500)`, now.Add(-48*time.Hour).Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// Configure only on HTTPS. Mailgun signs timestamp/token rather than the complete
// body; domain and queue association are checked before applying feedback.
func HandleMailgunFeedback(w http.ResponseWriter, r *http.Request, ds *Datastore, key string, domains []string) {
	w.Header().Set("Cache-Control", "no-store")
	if key == "" {
		http.NotFound(w, r)
		return
	}
	now := time.Now()
	event, token, err := VerifyMailgunEvent(r, key, now)
	if err != nil {
		http.Error(w, "invalid webhook", http.StatusNotAcceptable)
		return
	}
	allowed := false
	for _, domain := range domains {
		if domain != "" && strings.EqualFold(domain, event.Domain.Name) {
			allowed = true
			break
		}
	}
	if !allowed {
		http.Error(w, "unknown sending domain", http.StatusNotAcceptable)
		return
	}
	// Non-suppression feedback does not require persistence or replay state.
	if event.RecipientSuppressionReason() == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err = ds.ApplyMailgunFeedback(*event, token, now); err != nil {
		if errors.Is(err, ErrMailgunAssociation) {
			http.Error(w, "unknown queued message", http.StatusNotAcceptable)
			return
		}
		http.Error(w, "feedback not committed", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}
