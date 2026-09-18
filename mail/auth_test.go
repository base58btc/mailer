package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestSignedAuthenticationRejectsInvalidRequestsWithoutLoggingTokens(t *testing.T) {
	// Capture stdout because the legacy verifier logged both the supplied and
	// expected bearer credentials on failed authentication.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original; reader.Close(); writer.Close() }()
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	sign := func(secret, timestamp, path, method string) string {
		sum := sha256.Sum256([]byte(secret + timestamp + path + method))
		return hex.EncodeToString(sum[:])
	}
	for _, tc := range []struct {
		name, secret, timestamp, path, method, token string
		valid                                        bool
	}{
		{"valid", "fixture", stamp, "/job", "PUT", sign("fixture", stamp, "/job", "PUT"), true},
		{"wrong token", "fixture", stamp, "/job", "PUT", "private-supplied-token", false},
		{"wrong path", "fixture", stamp, "/suppression", "PUT", sign("fixture", stamp, "/job", "PUT"), false},
		{"wrong method", "fixture", stamp, "/job", "DELETE", sign("fixture", stamp, "/job", "PUT"), false},
		{"missing timestamp", "fixture", "", "/job", "PUT", "anything", false},
		{"expired", "fixture", "1", "/job", "PUT", sign("fixture", "1", "/job", "PUT"), false},
		{"empty secret", "", stamp, "/job", "PUT", sign("", stamp, "/job", "PUT"), false},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", tc.token)
		r.Header.Set("X-Base58-Timestamp", tc.timestamp)
		if err := checkKey(tc.secret, r); (err == nil) != tc.valid {
			t.Errorf("%s: valid=%v, error=%v", tc.name, tc.valid, err)
		}
	}
	writer.Close()
	os.Stdout = original
	output, err := io.ReadAll(reader)
	if err != nil || len(output) != 0 {
		t.Fatalf("authentication leaked log output: bytes=%d, error=%v", len(output), err)
	}
}
