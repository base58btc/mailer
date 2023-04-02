package mail

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"

	"github.com/mailgun/mailgun-go/v4"
)

/* TODO: send via nostr?! */

var defaultFromName = "Base58⛓🔓"
var defaultToName = "🐲"
var defaultFrom = "hello@base58.info"

type Mailer struct {
	SendGridKey string
	UseSandbox bool
	MailGunDomain string
	MailGunKey string
}

func NewMailer(sgKey string, useSandbox bool, mgKey string, domain string) *Mailer {
	return &Mailer{
		SendGridKey: sgKey,
		UseSandbox: useSandbox,
		MailGunKey: mgKey,
		MailGunDomain: domain,
	}
}

func (mr *Mailer) SendMail(m *Mail) (string, error) {
	return useMailGun(mr, m)
}

func useMailGun(mr *Mailer, m *Mail) (string, error) {
	mg := mailgun.NewMailgun(mr.MailGunDomain, mr.MailGunKey)

	fromname := defaultFromName
	if m.FromName.Valid {
		fromname = m.FromName.String
	}

	fromaddr := defaultFrom
	if m.FromAddr.Valid {
		fromaddr = m.FromAddr.String
	}

	msg := mg.NewMessage(
		fmt.Sprintf("%s <%s>", fromname, fromaddr),
		m.Title,
		m.TextBody,
		m.ToAddr,
	)

	for _, a := range m.Attachments {
		msg.AddBufferAttachment(a.Name, a.Content)
	}

	msg.SetTracking(false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second * 30)
	defer cancel()

	_, id, err := mg.Send(ctx, msg)

	return id, err
}

func useSendGrid(mr *Mailer, m *Mail) error {
	fromname := defaultFromName
	if m.FromName.Valid {
		fromname = m.FromName.String
	}

	toname := defaultToName
	if m.ToName.Valid {
		toname = m.ToName.String
	}

	fromaddr := defaultFrom
	if m.FromAddr.Valid {
		fromaddr = m.FromAddr.String
	}

	from := mail.NewEmail(fromname, fromaddr)
	to := mail.NewEmail(toname, m.ToAddr)

	message := mail.NewSingleEmail(from, m.Title, to, m.TextBody, m.HTMLBody)

	if (mr.UseSandbox) {
		ms := mail.NewMailSettings()
		sbMode := mail.NewSetting(true)
		ms.SetSandboxMode(sbMode)
		message.SetMailSettings(ms)
	}

	/* Add attachments */
	for _, a := range m.Attachments {
		attach := mail.NewAttachment()
		content := base64.StdEncoding.EncodeToString(a.Content)
		fmt.Printf("sending attachment %s with content %s\n", a.Name, content)
		attach.SetContent(content)
		attach.SetFilename(a.Name)
		attach.SetType(a.Type)
		attach.SetDisposition("attachment")
		message.AddAttachment(attach)
	}
	client := sendgrid.NewSendClient(mr.SendGridKey)
	response, err := client.Send(message)
	if err == nil {
		fmt.Println("Got status code: ", response.StatusCode)
		/* If not a 200 era code, send a message */
		if response.StatusCode >= http.StatusMultipleChoices {
			return fmt.Errorf("%s", response.Body)
		}
	}

	return err
}
