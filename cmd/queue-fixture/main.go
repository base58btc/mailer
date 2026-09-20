//go:build integration

// queue-fixture serves the real mailer queue API on an ephemeral loopback port.
// It never starts provider delivery and is only for local integration rehearsals.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/base58btc/mailer/mail"
)

func main() {
	db := flag.String("db", "", "disposable SQLite database")
	ready := flag.String("ready-file", "", "file to receive loopback URL")
	inspect := flag.Bool("inspect", false, "print aggregate queue content counts and exit")
	providerJob := flag.String("provider-job", "", "disposable fixture job key for simulated provider evidence")
	providerPhase := flag.String("provider-phase", "", "begin or accept; never contacts a provider")
	providerDomain := flag.String("provider-domain", "", "simulated sending domain")
	providerID := flag.String("provider-message-id", "", "simulated provider acceptance ID")
	flag.Parse()
	if *db == "" {
		panic("-db required")
	}
	ds, err := mail.DatastoreNew(*db)
	if err != nil {
		panic(err)
	}
	defer ds.Data.Close()
	if *providerJob != "" {
		var keys []string
		if err := ds.Data.Select(&keys, `SELECT idem_key FROM scheduled WHERE job_key=?`, *providerJob); err != nil || len(keys) != 1 {
			panic("expected exactly one fixture job")
		}
		switch *providerPhase {
		case "begin":
			if _, err := ds.GetToSendBatch(time.Now(), 100); err != nil {
				panic(err)
			}
			if ok, err := ds.BeginDeliveryForDomain(keys[0], *providerDomain); err != nil || !ok {
				panic("cannot begin fixture delivery")
			}
		case "accept":
			if err := ds.RecordProviderAcceptance(keys[0], *providerDomain, *providerID, time.Now()); err != nil {
				panic(err)
			}
		default:
			panic("provider-phase must be begin or accept")
		}
		json.NewEncoder(os.Stdout).Encode(map[string]string{"queue_id": keys[0]})
		return
	}
	if *inspect {
		var jobs, content, erased int
		if err := ds.Data.QueryRow(`SELECT count(*),coalesce(sum(CASE WHEN to_addr<>'' OR html_body<>'' OR text_body<>'' OR title<>'' OR unsubscribe_url<>'' OR attachments IS NOT NULL THEN 1 ELSE 0 END),0) FROM scheduled`).Scan(&jobs, &content); err != nil {
			panic(err)
		}
		if err := ds.Data.QueryRow(`SELECT count(*) FROM mail_job_erasures`).Scan(&erased); err != nil {
			panic(err)
		}
		json.NewEncoder(os.Stdout).Encode(map[string]int{"jobs": jobs, "jobs_with_content": content, "erased": erased})
		return
	}
	if *ready == "" || os.Getenv("FIXTURE_MAILER_SECRET") == "" {
		panic("ready file and FIXTURE_MAILER_SECRET required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	ds.ResetInProgress()
	server := &http.Server{Handler: mail.SetupRoutesWithMailgun(ds, os.Getenv("FIXTURE_MAILER_SECRET"), "", os.Getenv("FIXTURE_MAILGUN_SIGNING_KEY"), strings.Fields(os.Getenv("FIXTURE_MAILGUN_DOMAINS"))), ReadHeaderTimeout: 5 * time.Second}
	if err := os.WriteFile(*ready, []byte("http://"+listener.Addr().String()), 0600); err != nil {
		panic(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		timeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(timeout)
	}()
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		panic(fmt.Errorf("serve queue fixture: %w", err))
	}
}
