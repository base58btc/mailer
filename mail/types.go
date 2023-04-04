package mail

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"io/ioutil"
)

type (

	AttachSet []*Attachment
	Timestamp time.Time

	MailRequest struct {
		JobKey string `json:"job_key"`
		ToAddr string `json:"to_addr"`
		ToName string `json:"to_name,omitempty"`
		FromAddr string `json:"from_addr,omitempty"`
		FromName string `json:"from_name,omitempty"`
		ReplyTo string `json:"reply_to,omitempty"`
		Title string    `json:"title"`
		HTMLBody string `json:"html_body"`
		TextBody string `json:"text_body"`
		Attachments AttachSet `json:"attachments,omitempty"`
		SendAt float64 `json:"send_at"`
	}

	ReturnVal struct {
		Success bool   `json:"success"`
		Code int       `json:"code"`
		Message string `json:"error,omitempty"`
	}

	JobDelete struct {
		JobKey string `json:"job_key"`
	}

	Mail struct {
		JobKey string `db:"job_key"`
		ToAddr string `db:"to_addr"`
		ToName sql.NullString `db:"to_name"`
		FromAddr sql.NullString `db:"from_addr"`
		FromName sql.NullString `db:"from_name"`
		ReplyTo sql.NullString  `db:"reply_to"`
		Title string
		HTMLBody string `db:"html_body"`
		TextBody string `db:"text_body"`
		Attachments AttachSet `db:"attachments"`
		SendAt Timestamp `db:"send_at"`
		State ScheduleState
		TryCount int `db:"try_count"`
	}

	Attachment struct {
		Content []byte
		Type string
		Name string
	}
)

func ConvertMailRequest(job MailRequest) (*Mail, error) {
	m := &Mail{
		JobKey: job.JobKey,
		ToAddr: job.ToAddr,
		ToName: sql.NullString{
			Valid: job.ToName != "",
			String: job.ToName,
		},
		FromAddr: sql.NullString{
			Valid: job.FromAddr != "",
			String: job.FromAddr,
		},
		FromName: sql.NullString{
			Valid: job.FromName != "",
			String: job.FromName,
		},
		ReplyTo: sql.NullString{
			Valid: job.ReplyTo != "",
			String: job.ReplyTo,
		},
		Title: job.Title,
		HTMLBody: job.HTMLBody,
		TextBody: job.TextBody,
		Attachments: job.Attachments,
		SendAt: Timestamp(time.Unix(int64(job.SendAt), 0)),
	}

	if m.HTMLBody == "" && m.TextBody == "" {
		return nil, fmt.Errorf("Must provide either html_body or text_body")
	}

	return m, nil
}


func (m *Mail) IdemKey() string {
	h := sha256.New()
	h.Write([]byte(m.JobKey))
	h.Write([]byte(m.ToAddr))
	h.Write([]byte(m.Title))
	return hex.EncodeToString(h.Sum(nil))
}

func putString(tval uint8, buf []byte, item string) []byte {
	buf_item := []byte(item)
	return putBytes(tval, buf, buf_item)
}

func putBytes(tval uint8, buf []byte, content []byte) []byte {
	buf = append(buf, []byte{tval}...)

	l := make([]byte, 4)
	size := uint32(len(content))
	binary.LittleEndian.PutUint32(l, size)
	buf = append(buf, l...)
	buf = append(buf, content...)
	return buf
}

func (t Timestamp) Value() (driver.Value, error) {
	return time.Time(t).UTC().Unix(), nil
}

func (t *Timestamp) Scan(src interface{}) error {
	var val int64

	switch src.(type) {
	case int64:
		val = src.(int64)
	default:
		return fmt.Errorf("no match for timestamp src")
	}

	*t = Timestamp(time.Unix(val, 0))
	return nil
}

func (as *AttachSet) UnmarshalJSON(data []byte) error {
	var attachs []*Attachment
	json.Unmarshal(data, &attachs)

	*as = AttachSet(attachs)
	return nil
}

func (as AttachSet) MarshalJSON() ([]byte, error) {
	return json.Marshal([]*Attachment(as))
}

func (as AttachSet) Value() (driver.Value, error) {
	/* Handle nulls! */
	if len(as) == 0 {
		return nil, nil
	}

	var b []byte
	for i := 0; i < len(as); i++ {
		attach := as[i]
		var content []byte
		vals, err := attach.Value()
		if err != nil {
			return b, err
		}
		content = vals.([]byte)

		/* Prefix the TLV with a length. lmao */
		l := make([]byte, 4)
		size := uint32(len(content))
		binary.LittleEndian.PutUint32(l, size)
		b = append(b, l...)
		b = append(b, content...)
	}
	return b, nil
}

/* Pull out all the attachments !*/
func (as *AttachSet) Scan(src interface{}) error {
	if src == nil {
		set := make([]*Attachment, 0)
		*as = AttachSet(set)
		return nil
	}

	var source []byte
	var set AttachSet

	switch src.(type) {
	case []byte:
		/* Do nothing */
	default:
		return errors.New("Expected byte blob for attachment")
	}
	source = src.([]byte)

	/* Read out the length */
	var ptr int = 0
	length := len(source)
	for ptr < length {
		size := int(binary.LittleEndian.Uint32(source[ptr:ptr+4]))

		ptr += 4
		if ptr + size > length {
			return fmt.Errorf("Invalid length: %d, limit %d", ptr, length)
		}

		attachment := &Attachment{}
		err := attachment.Plump(source[ptr:ptr+size])
		if err != nil {
			return err
		}

		ptr += size
		set = append(set, attachment)
	}

	*as = set
	return nil
}

func (a Attachment) MarshalJSON() ([]byte, error) {
	value, err := a.Value()

	if err != nil {
		return nil, err
	}

	val := value.([]byte)
	return json.Marshal(val)
}

func (a *Attachment) UnmarshalJSON(data []byte) error {
	/* We wrap gzipped attachments in base64 encoding */
	var buf []byte
	json.Unmarshal(data, &buf)

	return a.Plump(buf)
}

/* Write out the attachments as gzipped blob data */
func (a Attachment) Value() (driver.Value, error) {
	var b []byte

	/* TLV! 1: name, 2: type, 3: content */
	b = putString(0x01, b, a.Name)
	b = putString(0x02, b, a.Type)
	b = putBytes(0x03, b, a.Content)

	zipped := make([]byte, 0, len(b))
	buf := bytes.NewBuffer(zipped)
	w := gzip.NewWriter(buf)
	w.Write(b)
	w.Close()
	return buf.Bytes(), nil
}

func (a *Attachment) Scan(src interface{}) error {
	var source []byte
	var attach Attachment

	switch src.(type) {
	case []byte:
		/* Do nothing */
	default:
		return errors.New("Expected byte blob for attachment")
	}

	source = src.([]byte)
	err := attach.Plump(source)
	if err != nil {
		return err
	}

	*a = attach
	return nil
}

/* Now we go backward! */
func (a *Attachment) Plump(buf []byte) error {
	/* First things first, we unzip the src */
	reader, _ := gzip.NewReader(bytes.NewReader(buf))
	src, err := ioutil.ReadAll(reader)
	if err != nil {
		reader.Close()
		return err
	}
	reader.Close()

	/* Then we parse the data */
	length := len(src)
	ptr := 0

	for ptr < length {
		/* Read off type + len */
		typ_val := src[ptr:ptr+1]
		typ := int(uint8(typ_val[0]))

		ptr += 1
		typLen := int(binary.LittleEndian.Uint32(src[ptr:ptr+4]))

		ptr += 4
		if ptr + typLen > length {
			return fmt.Errorf("Attachment parsing overflowed for type %d", typ)
		}

		switch (typ) {
		case 0x01:
			a.Name = string(src[ptr:ptr+typLen])
		case 0x02:
			a.Type = string(src[ptr:ptr+typLen])
		case 0x03:
			a.Content = src[ptr:ptr+typLen]
		default:
			return fmt.Errorf("attachment type not known: %d", typ)
		}

		ptr += typLen
	}

	return nil
}
