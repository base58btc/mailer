package mail

import (
	"fmt"
	"net/url"
	"strings"
)

// Optional for transactional mail. Callers supplying a URL must implement the
// one-click POST at that HTTPS endpoint; the mailer does not fetch the URL.
func validateUnsubscribeURL(value string) error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || len(value) > 4096 || strings.ContainsAny(value, "\r\n\t <>") || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid unsubscribe URL")
	}
	return nil
}

func addUnsubscribeHeaders(value string, add func(string, string)) {
	if value == "" {
		return
	}
	add("List-Unsubscribe", "<"+value+">")
	add("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
}
