package mail

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// OpenReconciliationStore never migrates or resets jobs. It may run beside the
// worker; DatastoreNew is deliberately unsuitable for operator access.
func OpenReconciliationStore(path string) (*Datastore, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("existing queue database required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=rw&_busy_timeout=5000"}
	connection, err := sqlx.Open("sqlite3", uri.String())
	if err != nil {
		return nil, err
	}
	connection.SetMaxOpenConns(1)
	var count int
	if err = connection.Get(&count, `SELECT count(*) FROM mail_delivery_reconciliations`); err != nil {
		connection.Close()
		return nil, errors.New("queue schema must be upgraded by the mailer before reconciliation")
	}
	return &Datastore{Data: connection}, nil
}

type UnknownDelivery struct {
	QueueID string `db:"idem_key" json:"queue_id"`
	JobKey  string `db:"job_key" json:"job_key"`
	Domain  string `db:"actual_provider_domain" json:"provider_domain"`
	SendAt  int64  `db:"send_at" json:"send_at"`
}

func (ds *Datastore) UnknownDeliveries(after string, limit int) ([]UnknownDelivery, error) {
	if limit < 1 || limit > 100 || (after != "" && !validQueueID(after)) {
		return nil, errors.New("invalid reconciliation page")
	}
	rows := []UnknownDelivery{}
	err := ds.Data.Select(&rows, `SELECT idem_key,job_key,actual_provider_domain,send_at FROM scheduled WHERE state='delivery_unknown' AND idem_key>? ORDER BY idem_key LIMIT ?`, after, limit)
	return rows, err
}
func validQueueID(id string) bool {
	raw, err := hex.DecodeString(id)
	return err == nil && len(raw) == 32 && id == strings.ToLower(id)
}

type AcceptanceReconciliation struct {
	OperationID string `db:"operation_id"`
	QueueID     string `db:"idem_key"`
	Actor       string `db:"actor"`
	EvidenceRef string `db:"evidence_ref"`
	Domain      string `db:"provider_domain"`
	MessageID   string `db:"provider_message_id"`
}

func (ds *Datastore) ReconcileProviderAcceptance(input AcceptanceReconciliation, now time.Time) error {
	input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))
	input.MessageID = normalizeProviderMessageID(input.MessageID)
	for _, value := range []string{input.OperationID, input.Actor, input.EvidenceRef, input.Domain, input.MessageID} {
		if value == "" || len(value) > 512 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("bounded operator identity and provider evidence required")
		}
	}
	if !validQueueID(input.QueueID) || len(input.Domain) > 253 || strings.ContainsAny(input.Domain, " \t") || now.IsZero() {
		return errors.New("invalid reconciliation identity")
	}
	tx, err := ds.Data.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous AcceptanceReconciliation
	err = tx.Get(&previous, `SELECT operation_id,idem_key,actor,evidence_ref,provider_domain,provider_message_id FROM mail_delivery_reconciliations WHERE operation_id=?`, input.OperationID)
	if err == nil {
		if previous != input {
			return errors.New("reconciliation operation conflicts with recorded evidence")
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var job struct {
		State  string `db:"state"`
		Domain string `db:"domain"`
	}
	if err = tx.Get(&job, `SELECT state,coalesce(nullif(actual_provider_domain,''),mail_domain,'') AS domain FROM scheduled WHERE idem_key=?`, input.QueueID); err != nil {
		return err
	}
	// Cancellation/erasure can win while evidence is being collected. Preserve
	// that terminal state while attaching verified historical acceptance.
	if job.State != "delivery_unknown" && job.State != "cancelled" {
		return errors.New("job is not awaiting operator reconciliation")
	}
	if job.Domain != input.Domain {
		return errors.New("provider domain does not match recorded sending domain")
	}
	if err = recordProviderAcceptance(tx, input.QueueID, input.Domain, input.MessageID, now); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO mail_delivery_reconciliations(operation_id,idem_key,actor,evidence_ref,provider_domain,provider_message_id,recorded_at) VALUES(?,?,?,?,?,?,?)`, input.OperationID, input.QueueID, input.Actor, input.EvidenceRef, input.Domain, input.MessageID, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
