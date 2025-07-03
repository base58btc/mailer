package mail

import (
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/jmoiron/sqlx"
)

var db *sqlx.DB

var db_setup_exec = []string{
	"CREATE TABLE db_metadata (key TEXT NOT NULL PRIMARY KEY, value TEXT);",
	"INSERT INTO db_metadata (key, value) values ('migrations', -1);",
}

var db_migration_exec = []string{
	`CREATE TABLE scheduled 
		(	
			idem_key TEXT NOT NULL PRIMARY KEY,
			job_key TEXT NOT NULL,
			to_addr TEXT NOT NULL,
			to_name TEXT,
			from_addr TEXT,
			from_name TEXT,
			reply_to TEXT,
			title TEXT NOT NULL,
			html_body TEXT NOT NULL,
			text_body TEXT NOT NULL,
			attachments BLOB,
			send_at BIGINT NOT NULL,
			state TEXT NOT NULL DEFAULT 'unsent',
			try_count INT NOT NULL DEFAULT 0
	        );`,
	`ALTER TABLE scheduled ADD COLUMN mail_domain TEXT;`,
	`ALTER TABLE scheduled ADD COLUMN sub TEXT;`,
	`ALTER TABLE scheduled ADD COLUMN missive TEXT;`,
}

func (ds *Datastore) CurrMigrations() int {
	return len(db_migration_exec)
}

type ScheduleState string
const (
	UNSENT ScheduleState = "unsent"
	INPROG ScheduleState = "inprog"
	FAILED ScheduleState = "failed"
	SENT ScheduleState = "sent"
)

func setupTables() error {
	stmt := `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='db_metadata';`
	var count int
	err := db.Get(&count, stmt)
	if err != nil {
		return err
	}

	if count > 0 {
		return nil
	}

	for _, quer := range db_setup_exec {
		db.MustExec(quer)
	}

	return nil
}

func (ds *Datastore) CountMigrations() int {
	return len(db_migration_exec)
}

func currentMigration() (int, error) {
	var migration int
	keyLookup := `SELECT value FROM db_metadata WHERE key = 'migrations';`

	err := db.Get(&migration, keyLookup)
	if err != nil {
		return 0, err
	}

	return migration, nil
}

func initDatabase(dBConn string) (err error) {
	db, err = sqlx.Open("sqlite3", dBConn)
	if err != nil {
		return err
	}

	db.SetMaxIdleConns(8)
	db.SetMaxOpenConns(8)
	err = setupTables()
	if err != nil {
		return err
	}

	curr_migrate, err := currentMigration()
	if err != nil {
		return err
	}

	fmt.Println("current migration:", curr_migrate)
	migrate_to := len(db_migration_exec) - 1
	if curr_migrate > migrate_to {
		return fmt.Errorf("Saved migration %d > proposed list %d\n", curr_migrate, migrate_to)
	}

	/* Start a transaction */
	tx := db.MustBegin()
	for i, migration := range db_migration_exec {
		if i <= curr_migrate {
			continue
		}

		fmt.Println("executing migration:", migration)
		/* execute the migration */
		tx.MustExec(migration)
	}

	/* Save the migration index to the database */
	tx.MustExec(`UPDATE db_metadata SET value = ? WHERE key = 'migrations';`, migrate_to)

	/* Commit the updates */
	err = tx.Commit()

	if err == nil && curr_migrate != migrate_to {
		fmt.Printf("Rolled database forward from %d to %d\n", curr_migrate, migrate_to)
	}

	return err
}

func (ds *Datastore) SetState(idemKey string, state ScheduleState) error {
	stmt := `UPDATE scheduled SET state = ? WHERE idem_key = ?`
	_, err := ds.Data.Exec(stmt, state, idemKey)
	return err
}

func (ds *Datastore) ListJobs(state *ScheduleState) ([]*Mail, error) {
	stmt := `SELECT job_key, sub, missive, to_addr, to_name, from_addr, from_name, reply_to, title, html_body, text_body, attachments, send_at, state, try_count, mail_domain from scheduled WHERE state = ?`

	var mail []*Mail
	err := ds.Data.Select(&mail, stmt, state)
	return mail, err
}

func (ds *Datastore) GetToSendBatch(when time.Time, batchSize int) ([]*Mail, error) {
	stmt := `SELECT job_key, sub, missive, to_addr, to_name, from_addr, from_name, reply_to, title, html_body, text_body, attachments, send_at, state, try_count, mail_domain
		FROM scheduled 
		WHERE 
			   ((state = 'failed' AND try_count < 20) 
			OR state = 'unsent')
			AND send_at <= ?
		ORDER BY send_at
		LIMIT ?`

	var mail []*Mail
	err := ds.Data.Select(&mail, stmt, when.UTC().Unix(), batchSize)
	if err != nil {
		return nil, err
	}

	if len(mail) == 0 {
		return mail, nil
	}

	/* Update all of these guys to be 'inprog' */
	keys := make([]string, len(mail))
	for i, m := range mail {
		keys[i] = m.IdemKey()
	}
	update := `UPDATE scheduled SET state = 'inprog' WHERE idem_key IN (?);`
	exec, args, err := sqlx.In(update, keys)
	if err != nil {
		return nil, err
	}
	if _, err = ds.Data.Exec(exec, args...); err != nil {
		return nil, err
	}

	return mail, err
}

func (ds *Datastore) GetJob(jobKey string) ([]*Mail, error) {
	stmt := `SELECT job_key, sub, missive, to_addr, to_name, from_addr, from_name, reply_to, title, html_body, text_body, attachments, send_at, state, try_count, mail_domain FROM scheduled WHERE job_key = ?`

	var mail []*Mail
	err := ds.Data.Select(&mail, stmt, jobKey)
	return mail, err
}

func (ds *Datastore) DeleteJob(jobKey string) {
	stmt := `DELETE FROM scheduled 
			WHERE job_key = ?
			AND (state = 'unsent' OR state = 'failed')`
	ds.Data.MustExec(stmt, jobKey)
}

func (ds *Datastore) DeleteSubscription(subKey string) {
	stmt := `DELETE FROM scheduled
			WHERE sub = ?
			AND (state = 'unsent' OR state = 'failed')`
	ds.Data.MustExec(stmt, subKey)
}

func (ds *Datastore) CancelMissive(subKey string) {
	stmt := `DELETE FROM scheduled
			WHERE missive = ?
			AND state != 'sent'`
	ds.Data.MustExec(stmt, subKey)
}

func (ds *Datastore) CancelJob(jobKey string) {
	stmt := `DELETE FROM scheduled WHERE job_key = ? AND state != 'sent'`
	ds.Data.MustExec(stmt, jobKey)
}


func (ds *Datastore) RescheduleFailed(idemKey string, tryCount int, sendAt int64) {
	stmt := `UPDATE scheduled 
		SET 
			state = 'failed', 
			try_count = ?,
			send_at = ?
		WHERE idem_key = ?`
	ds.Data.MustExec(stmt, tryCount, sendAt, idemKey)
}

func (ds *Datastore) MarkSent(idemKey string) {
	stmt := `UPDATE scheduled 
		SET 
			state = 'sent'
		WHERE idem_key = ?`
	ds.Data.MustExec(stmt, idemKey)
}

func (ds *Datastore) GetMail(idemKey string) (*Mail, error) {
	stmt := `SELECT job_key, to_addr, to_name, from_addr, from_name, reply_to, title, html_body, text_body, attachments, send_at, state, try_count, mail_domain from scheduled WHERE idem_key = ?`

	var mail Mail
	err := ds.Data.Get(&mail, stmt, idemKey)
	return &mail, err
}

func (ds *Datastore) ScheduleMail(m *Mail) error {
	stmt := `INSERT INTO scheduled (
			idem_key,
			job_key,
			sub,
			missive,
			to_addr,
			to_name,
			from_addr,
			from_name,
			reply_to,
			title,
			html_body,
			text_body,
			attachments,
			send_at,
			mail_domain
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := ds.Data.Exec(stmt, m.IdemKey(), m.JobKey, m.Sub, m.Missive, m.ToAddr, m.ToName, m.FromAddr, m.FromName, m.ReplyTo, m.Title, m.HTMLBody, m.TextBody, m.Attachments, m.SendAt, m.Domain)

	return err
}

func (ds *Datastore) ResetInProgress() {
	stmt := `UPDATE scheduled SET state = 'failed' WHERE state = 'inprog';`
	ds.Data.MustExec(stmt)
}

type Datastore struct {
	Data *sqlx.DB
}

/* Service that you can schedule emails to send out */
func DatastoreNew(dbConn string) (*Datastore, error) {
	err := initDatabase(dbConn)
	if err != nil {
		return nil, err
	}

	ds := &Datastore{ Data: db, }

	/* Always reset on start */
	ds.ResetInProgress()

	return ds, nil
}
