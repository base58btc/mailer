package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/base58btc/mailer/mail"
	"github.com/joho/godotenv"
)

type env struct {
	SendGrid string
	SendTimer int
	DbName string
	IsProd bool
	Port string
	MailGunKey string
	MailGunDomain string
	Secret string
}

func setupEnv() (*env, error) {
	var e env

	err := godotenv.Load()
	if err != nil {
		return nil, err
	}

	e.SendGrid = os.Getenv("SENDGRID_KEY")
	val, err := strconv.ParseInt(os.Getenv("MAIL_SEND_TIMER"), 10, 32)
	if err != nil {
		return nil, err
	}
	e.MailGunKey = os.Getenv("MAILGUN_KEY")
	e.MailGunDomain = os.Getenv("MAILGUN_DOMAIN")
	e.SendTimer = int(val)
	e.DbName = os.Getenv("DB_NAME")
	e.IsProd = os.Getenv("PROD") == "1"
	e.Port = os.Getenv("PORT")
	e.Secret = os.Getenv("HMAC_SECRET")
	return &e, nil
}

/* For now, we do it simply with a single worker bot */
func mailWorker(e *env, ds *mail.Datastore, ms *mail.Mailer) {
	for {
		mails, err := ds.GetToSendBatch(time.Now(), 1000)
		if err != nil {
			fmt.Printf("Unable to fetch batch %s", err)
			os.Exit(1)
		}

		fmt.Printf("Processing batch of %d mails\n", len(mails))
		/* Send off mails to be sent! */
		for _, m := range mails {
			id, err := ms.SendMail(m)
			if err != nil {
				fmt.Printf("Mail job %s failed (x%d)! %s\n", m.IdemKey(), m.TryCount + 1, err.Error())
				addlTime := time.Duration(m.TryCount * 100)
				retryAt := time.Now().Add(addlTime * time.Second)
				ds.RescheduleFailed(m.IdemKey(), m.TryCount + 1, retryAt.UTC().Unix())
			} else {
				fmt.Println("sent id:", id)
				ds.MarkSent(m.IdemKey())
			}
		}

		fmt.Printf("Batch of %d sent, sleeping %ds\n", len(mails), e.SendTimer)
		time.Sleep(time.Second * time.Duration(e.SendTimer))
	}
}

func main() {
	env, err := setupEnv()

	if err != nil {
		fmt.Printf("Unable to setup env %s\n", err)
		os.Exit(1)
	}

	ds, err := mail.DatastoreNew(env.DbName)
	if err != nil {
		fmt.Printf("Unable to setup db %s\n", err)
		os.Exit(1)
	}
	ms := mail.NewMailer(
		env.SendGrid,
		!env.IsProd,
		env.MailGunKey,
		env.MailGunDomain,
	)
	fmt.Println("The Mailer Domain is:", ms.MailGunDomain)

	/* Start up the mail worker */
	go mailWorker(env, ds, ms)

	/* Listen for incoming mail requests */
	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%s", "", env.Port),
		Handler: mail.SetupRoutes(ds, env.Secret),
	}

	fmt.Printf("Starting application on port %s\n", env.Port)
	err = srv.ListenAndServe()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
