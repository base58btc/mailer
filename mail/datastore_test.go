package mail

import (
	"database/sql"
	"strconv"
	tt "testing"
	"time"
	"github.com/google/go-cmp/cmp"
	"encoding/json"
)


func getDatastore(t *tt.T) *Datastore {
	ds, err := DatastoreNew(":memory:")
	if err != nil {
		t.Errorf("was not expecting err %s", err)
	}
	return ds
}

func TestDatstoreInit(t *tt.T) {
	ds := getDatastore(t)

	/* Check that the metadata table exists? */
	stmt := `SELECT key, value FROM db_metadata;`
	rows, err := ds.Data.Queryx(stmt)
	if err != nil {
		t.Errorf("was not expecting err %s", err)
	}

	rows.Next()

	var k, v string
	err = rows.Scan(&k, &v)
	if err != nil {
		t.Errorf("was not expecting err %s", err)
	}
	if k != "migrations" {
		t.Errorf("expecting %s, got %s", "migrations", k)
	}
	val, err  := strconv.ParseInt(v, 10, 32)
	if err != nil {
		t.Errorf("was not expecting err %s", err)
	}

	if int(val) != ds.CountMigrations() - 1 {
		t.Errorf("expecting %d, got %s", ds.CountMigrations() - 1, v)
	}
}

func checkMailState(t *tt.T, ds *Datastore, checkState ScheduleState, count int) {
	for _, state := range []ScheduleState { UNSENT, INPROG, FAILED, SENT} {
		mails, err := ds.ListJobs(&state)

		if err != nil {
			t.Errorf("was not expecting error %s", err)
		}

		if state != checkState {
			if len(mails) != 0 {
				t.Errorf("expecting %d mails, have %d in state %s", 0, len(mails), state)
			}
		} else {
			if len(mails) != count {
				t.Errorf("expecting %d mails, have %d in state %s", count, len(mails), state)
			}
		}
	}
}

func TestDataSaveMail(t *tt.T) {
	ds := getDatastore(t)

	/* Put some mail in! */
	mail := &Mail{
		JobKey: "hello",
		ToAddr: "hi@example.com",
		ToName: sql.NullString{String:"base58 student", Valid: true,},
		FromAddr: sql.NullString{String:"bye@example.com", Valid: true,},
		Title: "Example email",
		HTMLBody: "<html><body><p>hello!</p></body></html>",
		TextBody: "hello!",
		SendAt: Timestamp(time.Now()),
		Attachments: AttachSet([]*Attachment{
			&Attachment{
				Content: []byte("hhihihi"),
				Type: "app/json",
				Name: "greetings.txt",
			},
		}),
	}
	err := ds.ScheduleMail(mail)
	if err != nil {
		t.Errorf("was not expecting err %s", err)
	}

	/* Check that was inserted! */
	checkMailState(t, ds, UNSENT, 1)

	/* Try to reinsert */
	err = ds.ScheduleMail(mail)
	if err == nil {
		t.Errorf("was expecting err, didn't get one")
	}
	checkMailState(t, ds, UNSENT, 1)

	/* make sure data in == data out */
	resMail, err :=  ds.GetMail(mail.IdemKey())
	if err != nil {
		t.Errorf("was not expecting err %s", err)
	}
	/* We have to set the state to UNSENT, db default */
	mail.State = UNSENT
	opt := cmp.Comparer(func (x, y Timestamp) bool {
		return time.Time(x).UTC().Unix()  == time.Time(y).UTC().Unix()
	})

	if !cmp.Equal(mail, resMail, opt) {
		t.Errorf("was expecting %+v, got %+v", mail, resMail)
	}

	mails, err := ds.GetJob(mail.JobKey)
	if err != nil {
		t.Errorf("was not expecting err %s", err)
	}
	if len(mails) != 1 {
		t.Errorf("was expecting 1 mail for jobkey")
	}
	if !cmp.Equal(mail, mails[0], opt) {
		t.Errorf("was expecting %+v, got %+v", mail, resMail)
	}

	/* Update to inprog! */
	err = ds.SetState(mail.IdemKey(), INPROG)
	if err != nil {
		t.Errorf("was not expecting err %s", err)
	}
	checkMailState(t, ds, INPROG, 1)

	/* Reset state to "failed" 'on start' */
	ds.ResetInProgress()
	checkMailState(t, ds, FAILED, 1)

	/* Test deleting a job! */
	ds.DeleteJob(mail.JobKey)
	mails, _ = ds.GetJob(mail.JobKey)
	if len(mails) > 0 {
		t.Errorf("was expecting mails to be gone")
	}
}
