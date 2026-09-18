package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRecipientStatusAuthenticationPrivacyAndIsolation(t *testing.T) {
	ds := getDatastore(t)
	defer ds.Data.Close()
	if _, err := ds.Data.Exec(`INSERT INTO mail_recipient_suppressions VALUES('example.test','fixture@example.test','hard_bounce',1)`); err != nil {
		t.Fatal(err)
	}
	request := func(body string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/recipient-status", strings.NewReader(body))
		if auth {
			stamp := strconv.FormatInt(time.Now().Unix(), 10)
			sum := sha256.Sum256([]byte("fixture" + stamp + "/recipient-statusPOST"))
			r.Header.Set("Authorization", hex.EncodeToString(sum[:]))
			r.Header.Set("X-Base58-Timestamp", stamp)
		}
		w := httptest.NewRecorder()
		SetupRoutes(ds, "fixture").ServeHTTP(w, r)
		return w
	}
	body := `{"domain":"example.test","recipient":"fixture@example.test"}`
	if w := request(body, false); w.Code != 401 || strings.Contains(w.Body.String(), "hard_bounce") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(body, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"blocked":true`) || strings.Contains(w.Body.String(), "fixture@example.test") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(strings.ReplaceAll(body, "example.test\",\"recipient", "other.test\",\"recipient"), true); w.Code != 200 || !strings.Contains(w.Body.String(), `"blocked":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, bad := range []string{body + " {}", `{"domain":"example.test","recipient":"invalid"}`, `{"domain":"example.test","recipient":"fixture@example.test","extra":true}`} {
		if w := request(bad, true); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	ds.Data.Close()
	if w := request(body, true); w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
}
