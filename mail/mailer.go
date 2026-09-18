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
var defaultFrom = "hello@base58.school"

type Mailer struct {
	SendGridKey   string
	UseSandbox    bool
	MailGunDomain string
	MailGunKey    string
}

func NewMailer(sgKey string, useSandbox bool, mgKey string, domain string) *Mailer {
	return &Mailer{
		SendGridKey:   sgKey,
		UseSandbox:    useSandbox,
		MailGunKey:    mgKey,
		MailGunDomain: domain,
	}
}

func (mr *Mailer) SendMail(m *Mail) (string, error) {
	return useMailGun(mr, m)
}

func useMailGun(mr *Mailer, m *Mail) (string, error) {
	mg := mailgun.NewMailgun(mr.MailGunDomain, mr.MailGunKey)
	msg, err := buildMailgunMessage(mg, m)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, id, err := mg.Send(ctx, msg)
	return id, err
}

func buildMailgunMessage(mg *mailgun.MailgunImpl, m *Mail) (*mailgun.Message, error) {
	if err := validateUnsubscribeURL(m.UnsubscribeURL); err != nil {
		return nil, err
	}

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

	msg.SetHtml(m.HTMLBody)
	addUnsubscribeHeaders(m.UnsubscribeURL, msg.AddHeader)

	for _, a := range m.Attachments {
		msg.AddBufferAttachment(a.Name, a.Content)
	}

	msg.SetTracking(false)
	return msg, nil
}

func useSendGrid(mr *Mailer, m *Mail) error {
	message, err := buildSendGridMessage(m, mr.UseSandbox)
	if err != nil {
		return err
	}
	client := sendgrid.NewSendClient(mr.SendGridKey)
	response, err := client.Send(message)
	if err == nil && response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%s", response.Body)
	}
	return err
}

func buildSendGridMessage(m *Mail, useSandbox bool) (*mail.SGMailV3, error) {
	if err := validateUnsubscribeURL(m.UnsubscribeURL); err != nil {
		return nil, err
	}
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
	addUnsubscribeHeaders(m.UnsubscribeURL, func(k, v string) { message.SetHeader(k, v) })

	if useSandbox {
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
	return message, nil
}
