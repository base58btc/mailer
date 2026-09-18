package mail

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

// POST keeps the recipient out of URL/access logs. This lookup does not clear
// suppression, submit messages, or change any application's consent records.
func HandleRecipientStatus(w http.ResponseWriter, r *http.Request, ds *Datastore, secret string) {
	w.Header().Set("Cache-Control", "no-store")
	if err := checkKey(secret, r); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var query struct {
		Domain    string `json:"domain"`
		Recipient string `json:"recipient"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&query); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "invalid request", 400)
		return
	}
	address, err := mail.ParseAddress(query.Recipient)
	if err != nil || address.Address != query.Recipient || query.Domain == "" || len(query.Domain) > 253 || strings.ContainsAny(query.Domain, " \t\r\n/@") {
		http.Error(w, "invalid request", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var reason string
	err = ds.Data.QueryRowContext(ctx, `SELECT reason FROM mail_recipient_suppressions WHERE domain=? AND recipient=?`, strings.ToLower(query.Domain), strings.ToLower(query.Recipient)).Scan(&reason)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "status unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Success bool   `json:"success"`
		Code    int    `json:"code"`
		Blocked bool   `json:"blocked"`
		Reason  string `json:"reason"`
	}{true, 200, reason != "", reason})
}
