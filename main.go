package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
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
	MailDomains string
	Secret string
}

func setupEnv() (*env, error) {
	var e env
	var err error

	if secrets := os.Getenv("SECRETS_FILE"); secrets != "" {
		fmt.Println("using secrets", secrets)
		err = godotenv.Load(secrets)
	} else {
		err = godotenv.Load()
	}
	if err != nil {
		return nil, err
	}

	e.SendGrid = os.Getenv("SENDGRID_KEY")
	val, err := strconv.ParseInt(os.Getenv("MAIL_SEND_TIMER"), 10, 32)
	if err != nil {
		return nil, err
	}
	e.MailGunKey = os.Getenv("MAILGUN_KEY")
	e.MailDomains = os.Getenv("MAIL_DOMAINS")
	e.SendTimer = int(val)
	e.DbName = os.Getenv("DB_NAME")
	e.IsProd = os.Getenv("PROD") == "1"
	e.Port = os.Getenv("PORT")
	e.Secret = os.Getenv("HMAC_SECRET")
	return &e, nil
}

/* For now, we do it simply with a single worker bot */
func mailWorker(e *env, ds *mail.Datastore, mailers map[string]*mail.Mailer) {

	defaultDomain := e.DefaultDomain()
	dd, ok := mailers[defaultDomain]
	if !ok {
		fmt.Printf("Unable to get default domain %s", defaultDomain)
		os.Exit(1)
	}

	for {
		mails, err := ds.GetToSendBatch(time.Now(), 1000)
		if err != nil {
			fmt.Printf("Unable to fetch batch %s", err)
			os.Exit(1)
		}

		fmt.Printf("Processing batch of %d mails\n", len(mails))
		/* Send off mails to be sent! */
		for _, m := range mails {
			ms, ok := mailers[m.Domain]
			if !ok {
				fmt.Printf("unable to find mailer for domain %s, using default %s", m.Domain, defaultDomain)
				ms = dd
			}

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

func trimstrings(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.TrimSpace(s)
	}
	return out
}

func (e *env) DefaultDomain() string {
	domains := trimstrings(strings.Split(e.MailDomains, ","))
	return domains[0]
}

func buildMailers(env *env) map[string]*mail.Mailer {

	domains := trimstrings(strings.Split(env.MailDomains, ","))
	mailers := make(map[string]*mail.Mailer)

	for _, mailDomain := range domains {
		mailers[mailDomain] = mail.NewMailer(
			env.SendGrid,
			!env.IsProd,
			env.MailGunKey,
			mailDomain,
		)
	}

	return mailers
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

	fmt.Println("The Mailer Domain options are:", env.MailDomains)

	/* Start up the mail worker */
	mailers := buildMailers(env)
	go mailWorker(env, ds, mailers)

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
