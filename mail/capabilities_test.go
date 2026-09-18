package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestCapabilitiesRequiresAuthenticationAndDoesNotAccessQueue(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodGet, "/capabilities", nil)
		if authorized {
			stamp := strconv.FormatInt(time.Now().Unix(), 10)
			sum := sha256.Sum256([]byte("fixture-secret" + stamp + "/capabilitiesGET"))
			r.Header.Set("Authorization", hex.EncodeToString(sum[:]))
			r.Header.Set("X-Base58-Timestamp", stamp)
		}
		w := httptest.NewRecorder()
		// No datastore or delivery provider is needed for this read-only endpoint.
		SetupRoutes(nil, "fixture-secret").ServeHTTP(w, r)
		var result struct {
			Success  bool     `json:"success"`
			Version  int      `json:"queue_contract"`
			Features []string `json:"features"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Success != authorized || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(result, w.Header())
		}
		if !authorized {
			if result.Version != 0 || len(result.Features) != 0 {
				t.Fatal("capabilities leaked")
			}
			continue
		}
		if result.Version != 1 {
			t.Fatal(result)
		}
		found := map[string]bool{}
		for _, f := range result.Features {
			found[f] = true
		}
		for _, f := range []string{"idempotent_jobs", "unsubscribe_headers", "durable_suppression"} {
			if !found[f] {
				t.Fatal("missing advertised behavior", f)
			}
		}
	}
}
