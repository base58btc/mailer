package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"github.com/gorilla/mux"
	"fmt"
	"strconv"
	"time"
)

func returnErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(&ReturnVal{
		Success: false,
		Code: http.StatusBadRequest,
		Message: err.Error(),
	})
}

func returnSuccess(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(&ReturnVal{
		Success: true,
		Code: http.StatusOK,
		Message: "",
	})
}

func checkKey(secret string, r *http.Request) error {
	/* Expect a header: Authorization: xxx */
	authToken := r.Header.Get("Authorization")
	timestamp := r.Header.Get("X-Base58-Timestamp")

	if timestamp == "" {
		return fmt.Errorf("Missing timestamp")
	}
	val, err := strconv.ParseInt(timestamp, 10, 32)
	if err != nil {
		return err
	}
	madeAt := time.Unix(val, 0)
	now := time.Now()
	windowStart := now.Add(-30 * time.Minute)
	windowEnd := now.Add(30 * time.Minute)
	if madeAt.Before(windowStart) || madeAt.After(windowEnd) {
		return fmt.Errorf("Invalid timestamp")
	}

	h := sha256.New()
	h.Write([]byte(secret))
	h.Write([]byte(timestamp))
	h.Write([]byte(r.URL.Path))
	h.Write([]byte(r.Method))
	expToken := hex.EncodeToString(h.Sum(nil))

	if authToken != expToken {
		fmt.Println("inputs:", timestamp, r.URL.Path, r.Method)
		fmt.Printf("expected %s, got %s", expToken, authToken)
		return fmt.Errorf("Invalid auth token")
	}
	return nil
}

func HandleMailJob(w http.ResponseWriter, r *http.Request, ds *Datastore, secret string) {
	err := checkKey(secret, r)
	if err != nil {
		fmt.Printf("Not auth'd")
		returnErr(w, err)
		return
	}

	/* Pull the data out of the request body */
	var job MailRequest
	decoder := json.NewDecoder(r.Body)
	err = decoder.Decode(&job)

	if err != nil {
		fmt.Printf("Unable to decode request: %s\n", err)
		returnErr(w, err)
		return
	}

	/* Convert to Mail */
	m, err := ConvertMailRequest(job)
	if err != nil {
		fmt.Printf("Unable to convert mail job: %s\n", err)
		returnErr(w, err)
		return
	}

	/* Save Job */
	err = ds.ScheduleMail(m)
	if err != nil {
		fmt.Printf("Unable to schedule mail: %s\n", err)
		returnErr(w, err)
		return
	}

	/* Send a success */
	fmt.Printf("Scheduled new mail item for job %s %s\n", m.JobKey, m.IdemKey())
	returnSuccess(w)
}

func DeleteMailJob(w http.ResponseWriter, r *http.Request, ds *Datastore, secret string) {
	err := checkKey(secret, r)
	if err != nil {
		fmt.Printf("Not auth'd")
		returnErr(w, err)
		return
	}

	var job JobDelete
	decoder := json.NewDecoder(r.Body)
	err = decoder.Decode(&job)

	if err != nil {
		fmt.Printf("Unable to decode request: %s\n", err)
		returnErr(w, err)
		return
	}
	ds.DeleteJob(job.JobKey)
	fmt.Printf("Deleted job %s\n", job.JobKey)
	/* FIXME: notify if none? */
	returnSuccess(w)
}

func DeleteSubJob(w http.ResponseWriter, r *http.Request, ds *Datastore, secret string) {
	err := checkKey(secret, r)
	if err != nil {
		fmt.Printf("Not auth'd")
		returnErr(w, err)
		return
	}

	var sub SubDelete
	decoder := json.NewDecoder(r.Body)
	err = decoder.Decode(&sub)

	if err != nil {
		fmt.Printf("Unable to decode request: %s\n", err)
		returnErr(w, err)
		return
	}
	ds.DeleteSubscription(sub.SubKey)
	fmt.Printf("Deleted subscription %s\n", sub.SubKey)
	/* FIXME: notify if none? */
	returnSuccess(w)
}

func SetupRoutes(ds *Datastore, secret string) http.Handler {
	r := mux.NewRouter()

	r.HandleFunc("/job", func (w http.ResponseWriter, r *http.Request) {
		HandleMailJob(w, r, ds, secret)
	}).Methods("PUT")

	r.HandleFunc("/job", func (w http.ResponseWriter, r *http.Request) {
		DeleteMailJob(w, r, ds, secret)
	}).Methods("DELETE")

	r.HandleFunc("/sub", func (w http.ResponseWriter, r *http.Request) {
		DeleteSubJob(w, r, ds, secret)
	}).Methods("DELETE")

	return r
}
