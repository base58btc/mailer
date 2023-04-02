package mail

import (
	"encoding/json"
	tt "testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestAttachment(t *tt.T) {
	a := Attachment{
		Content: []byte("new content to write in"),
		Type: "text/plain",
		Name: "content.txt",
	}

	v, err := a.Value()
	if err != nil {
		t.Errorf("was not expecting err")
	}

	var b Attachment
	err = (&b).Scan(v)
	if err != nil {
		t.Errorf("was not expecting err")
	}

	if !cmp.Equal(a, b) {
		t.Errorf("was expecting %+v, got %+v", a, b)
	}

	/* Let's go through JSON now */
	data, err := json.Marshal(a)
	if err != nil {
		t.Errorf("was not expecting err %s", err)
	}

	json.Unmarshal(data, &b)

	if !cmp.Equal(a, b) {
		t.Errorf("was expecting %+v, got %+v", a, b)
	}
}

func TestAttachments(t *tt.T) {
	var a, b AttachSet
	a = []*Attachment{
		&Attachment{
			Content: []byte("new content to write in"),
			Type: "text/plain",
			Name: "content.txt",
		},
		&Attachment{
			Content: []byte("{\"first_past\": \"the post\""),
			Type: "application/javascript",
			Name: "json.txt",
		},
		&Attachment{
			Content: []byte("well hi there"),
			Type: "text/text",
			Name: "greeting.txt",
		},
	}

	v, err := a.Value()
	if err != nil {
		t.Errorf("was not expecting err %s", err)
	}

	err = (&b).Scan(v)
	if err != nil {
		t.Errorf("was not expecting err %s", err)
	}

	if !cmp.Equal(a, b) {
		t.Errorf("was expecting %+v, got %+v", a, b)
	}

	/* Let's go through JSON now */
	data, err := json.Marshal(a)
	if err != nil {
		t.Errorf("was not expecting err %s", err)
	}

	json.Unmarshal(data, &b)

	if !cmp.Equal(a, b) {
		t.Errorf("was expecting %+v, got %+v", a, b)
	}
}

func TestTimestamp(t *tt.T) {

	var a Timestamp
	stamp := int64(1680358878)
	ts := Timestamp(time.Unix(stamp, 0))

	v, err := ts.Value()
	if err != nil {
		t.Errorf("was not expecting err")
	}

	err = (&a).Scan(v)
	if err != nil {
		t.Errorf("was not expecting err")
	}

	res := time.Time(a)
	if res.UTC().Unix() != stamp {
		t.Errorf("expecting %d, got %d", stamp, res.UTC().Unix())
	}
}
