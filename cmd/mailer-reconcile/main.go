// mailer-reconcile inspects interrupted sends and records operator-verified
// provider acceptance. It never sends mail or turns an ambiguous job into a retry.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/base58btc/mailer/mail"
)

func run(args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("mailer-reconcile", flag.ContinueOnError)
	flags.SetOutput(errOut)
	database := flags.String("db", "", "existing mailer SQLite database")
	action := flags.String("action", "list", "list or confirm-accepted")
	after := flags.String("after", "", "queue ID cursor for the next list page")
	limit := flags.Int("limit", 100, "list page size (1-100)")
	operation := flags.String("operation-id", "", "unique operation reference; reuse for an identical retry")
	queue := flags.String("queue-id", "", "exact 64-character queue ID, not logical job key")
	domain := flags.String("domain", "", "verified sending domain")
	message := flags.String("message-id", "", "verified provider message ID")
	actor := flags.String("actor", "", "operator identity")
	evidence := flags.String("evidence-ref", "", "reference to verified provider evidence; no message content")
	confirm := flags.Bool("confirm", false, "persist the verified acceptance and immutable audit record")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *database == "" || flags.NArg() != 0 {
		return fmt.Errorf("-db is required and positional arguments are not supported")
	}
	if *action != "list" && *action != "confirm-accepted" {
		return fmt.Errorf("unknown action")
	}
	if *action == "confirm-accepted" && !*confirm {
		return fmt.Errorf("review provider evidence and use -confirm to persist it")
	}
	ds, err := mail.OpenReconciliationStore(*database)
	if err != nil {
		return err
	}
	defer ds.Data.Close()
	if *action == "list" {
		rows, err := ds.UnknownDeliveries(*after, *limit)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(rows)
	}
	input := mail.AcceptanceReconciliation{OperationID: *operation, QueueID: *queue, Actor: *actor, EvidenceRef: *evidence, Domain: *domain, MessageID: *message}
	if err = ds.ReconcileProviderAcceptance(input, time.Now().UTC()); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"recorded": true, "operation_id": *operation, "queue_id": *queue})
}
func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
