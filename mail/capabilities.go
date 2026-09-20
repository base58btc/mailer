package mail

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Contract versions describe implemented behavior, not deployment health or
// provider credentials. Checking this endpoint never schedules an email.
func HandleCapabilities(w http.ResponseWriter, r *http.Request, secret string) {
	w.Header().Set("Cache-Control", "no-store")
	if err := checkKey(secret, r); err != nil {
		returnErr(w, errors.New("not authorized"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Success       bool     `json:"success"`
		Code          int      `json:"code"`
		QueueContract int      `json:"queue_contract"`
		Features      []string `json:"features"`
	}{true, http.StatusOK, 1, []string{"idempotent_jobs", "unsubscribe_headers", "durable_suppression", "recipient_status", "job_erasure", "provider_acceptance_status"}})
}
