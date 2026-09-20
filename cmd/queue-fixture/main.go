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
	"time"

	"github.com/base58btc/mailer/mail"
)

func main() {
	db := flag.String("db", "", "disposable SQLite database")
	ready := flag.String("ready-file", "", "file to receive loopback URL")
	inspect := flag.Bool("inspect", false, "print aggregate queue content counts and exit")
	flag.Parse()
	if *db == "" {
		panic("-db required")
	}
	ds, err := mail.DatastoreNew(*db)
	if err != nil {
		panic(err)
	}
	defer ds.Data.Close()
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
	server := &http.Server{Handler: mail.SetupRoutes(ds, os.Getenv("FIXTURE_MAILER_SECRET")), ReadHeaderTimeout: 5 * time.Second}
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
