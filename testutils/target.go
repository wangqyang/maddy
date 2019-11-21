package testutils

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"io/ioutil"
	"reflect"
	"sort"
	"testing"

	"github.com/emersion/go-message/textproto"
	"github.com/foxcpp/maddy/buffer"
	"github.com/foxcpp/maddy/config"
	"github.com/foxcpp/maddy/exterrors"
	"github.com/foxcpp/maddy/module"
)

type Msg struct {
	MsgMeta  *module.MsgMetadata
	MailFrom string
	RcptTo   []string
	Body     []byte
	Header   textproto.Header
}

type Target struct {
	Messages        []Msg
	DiscardMessages bool

	StartErr       error
	RcptErr        map[string]error
	BodyErr        error
	PartialBodyErr map[string]error
	AbortErr       error
	CommitErr      error

	InstName string
}

/*
module.Module is implemented with dummy functions for logging done by MsgPipeline code.
*/

func (dt Target) Init(*config.Map) error {
	return nil
}

func (dt Target) InstanceName() string {
	if dt.InstName != "" {
		return dt.InstName
	}
	return "test_instance"
}

func (dt Target) Name() string {
	return "test_target"
}

type testTargetDelivery struct {
	msg Msg
	tgt *Target
}

type testTargetDeliveryPartial struct {
	testTargetDelivery
}

func (dt *Target) Start(msgMeta *module.MsgMetadata, mailFrom string) (module.Delivery, error) {
	if dt.PartialBodyErr != nil {
		return &testTargetDeliveryPartial{
			testTargetDelivery: testTargetDelivery{
				tgt: dt,
				msg: Msg{MsgMeta: msgMeta, MailFrom: mailFrom},
			},
		}, dt.StartErr
	}
	return &testTargetDelivery{
		tgt: dt,
		msg: Msg{MsgMeta: msgMeta, MailFrom: mailFrom},
	}, dt.StartErr
}

func (dtd *testTargetDelivery) AddRcpt(to string) error {
	if dtd.tgt.RcptErr != nil {
		if err := dtd.tgt.RcptErr[to]; err != nil {
			return err
		}
	}

	dtd.msg.RcptTo = append(dtd.msg.RcptTo, to)
	return nil
}

func (dtd *testTargetDeliveryPartial) BodyNonAtomic(c module.StatusCollector, header textproto.Header, buf buffer.Buffer) {
	if dtd.tgt.PartialBodyErr != nil {
		for rcpt, err := range dtd.tgt.PartialBodyErr {
			c.SetStatus(rcpt, err)
		}
		return
	}

	dtd.msg.Header = header

	body, err := buf.Open()
	if err != nil {
		for rcpt, err := range dtd.tgt.PartialBodyErr {
			c.SetStatus(rcpt, err)
		}
		return
	}
	defer body.Close()

	dtd.msg.Body, err = ioutil.ReadAll(body)
	if err != nil {
		for rcpt, err := range dtd.tgt.PartialBodyErr {
			c.SetStatus(rcpt, err)
		}
	}
}

func (dtd *testTargetDelivery) Body(header textproto.Header, buf buffer.Buffer) error {
	if dtd.tgt.PartialBodyErr != nil {
		return errors.New("partial failure occurred, no additional information available")
	}
	if dtd.tgt.BodyErr != nil {
		return dtd.tgt.BodyErr
	}

	dtd.msg.Header = header

	body, err := buf.Open()
	if err != nil {
		return err
	}
	defer body.Close()

	if dtd.tgt.DiscardMessages {
		// Don't bother.
		_, err = io.Copy(ioutil.Discard, body)
		return err
	}

	dtd.msg.Body, err = ioutil.ReadAll(body)
	return err
}

func (dtd *testTargetDelivery) Abort() error {
	return dtd.tgt.AbortErr
}

func (dtd *testTargetDelivery) Commit() error {
	if dtd.tgt.CommitErr != nil {
		return dtd.tgt.CommitErr
	}
	if dtd.tgt.DiscardMessages {
		return nil
	}
	dtd.tgt.Messages = append(dtd.tgt.Messages, dtd.msg)
	return nil
}

func DoTestDelivery(t *testing.T, tgt module.DeliveryTarget, from string, to []string) string {
	return DoTestDeliveryMeta(t, tgt, from, to, &module.MsgMetadata{})
}

func DoTestDeliveryMeta(t *testing.T, tgt module.DeliveryTarget, from string, to []string, msgMeta *module.MsgMetadata) string {
	t.Helper()

	id, err := DoTestDeliveryErrMeta(t, tgt, from, to, msgMeta)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	return id
}

func DoTestDeliveryNonAtomic(t *testing.T, c module.StatusCollector, tgt module.DeliveryTarget, from string, to []string) string {
	t.Helper()

	IDRaw := sha1.Sum([]byte(t.Name()))
	encodedID := hex.EncodeToString(IDRaw[:])

	body := buffer.MemoryBuffer{Slice: []byte("foobar\r\n")}
	msgMeta := module.MsgMetadata{
		DontTraceSender: true,
		ID:              encodedID,
	}
	t.Log("-- tgt.Start", from)
	delivery, err := tgt.Start(&msgMeta, from)
	if err != nil {
		t.Log("-- ... tgt.Start", from, err, exterrors.Fields(err))
		t.Fatalf("Unexpected err: %v %+v", err, exterrors.Fields(err))
		return encodedID
	}
	for _, rcpt := range to {
		t.Log("-- delivery.AddRcpt", rcpt)
		if err := delivery.AddRcpt(rcpt); err != nil {
			t.Log("-- ... delivery.AddRcpt", rcpt, err, exterrors.Fields(err))
			t.Log("-- delivery.Abort")
			if err := delivery.Abort(); err != nil {
				t.Log("-- delivery.Abort:", err, exterrors.Fields(err))
			}
			t.Fatalf("Unexpected err: %v %+v", err, exterrors.Fields(err))
			return encodedID
		}
	}
	t.Log("-- delivery.BodyNonAtomic")
	hdr := textproto.Header{}
	hdr.Add("B", "2")
	hdr.Add("A", "1")
	delivery.(module.PartialDelivery).BodyNonAtomic(c, hdr, body)
	t.Log("-- delivery.Commit")
	if err := delivery.Commit(); err != nil {
		t.Fatalf("Unexpected err: %v %+v", err, exterrors.Fields(err))
	}

	return encodedID
}

const DeliveryData = "A: 1\n" +
	"B: 2\n" +
	"\n" +
	"foobar\n"

func DoTestDeliveryErr(t *testing.T, tgt module.DeliveryTarget, from string, to []string) (string, error) {
	return DoTestDeliveryErrMeta(t, tgt, from, to, &module.MsgMetadata{})
}

func DoTestDeliveryErrMeta(t *testing.T, tgt module.DeliveryTarget, from string, to []string, msgMeta *module.MsgMetadata) (string, error) {
	t.Helper()

	IDRaw := sha1.Sum([]byte(t.Name()))
	encodedID := hex.EncodeToString(IDRaw[:])

	body := buffer.MemoryBuffer{Slice: []byte("foobar\r\n")}
	msgMeta.DontTraceSender = true
	msgMeta.ID = encodedID
	t.Log("-- tgt.Start", from)
	delivery, err := tgt.Start(msgMeta, from)
	if err != nil {
		t.Log("-- ... tgt.Start", from, err, exterrors.Fields(err))
		return encodedID, err
	}
	for _, rcpt := range to {
		t.Log("-- delivery.AddRcpt", rcpt)
		if err := delivery.AddRcpt(rcpt); err != nil {
			t.Log("-- ... delivery.AddRcpt", rcpt, err, exterrors.Fields(err))
			t.Log("-- delivery.Abort")
			if err := delivery.Abort(); err != nil {
				t.Log("-- delivery.Abort:", err, exterrors.Fields(err))
			}
			return encodedID, err
		}
	}
	t.Log("-- delivery.Body")
	hdr := textproto.Header{}
	hdr.Add("B", "2")
	hdr.Add("A", "1")
	if err := delivery.Body(hdr, body); err != nil {
		t.Log("-- ... delivery.Body", err, exterrors.Fields(err))
		t.Log("-- delivery.Abort")
		if err := delivery.Abort(); err != nil {
			t.Log("-- ... delivery.Abort:", err, exterrors.Fields(err))
		}
		return encodedID, err
	}
	t.Log("-- delivery.Commit")
	if err := delivery.Commit(); err != nil {
		t.Log("-- ... delivery.Commit", err, exterrors.Fields(err))
		return encodedID, err
	}

	return encodedID, err
}

func CheckTestMessage(t *testing.T, tgt *Target, indx int, sender string, rcpt []string) {
	t.Helper()

	if len(tgt.Messages) <= indx {
		t.Errorf("wrong amount of messages received, want at least %d, got %d", indx+1, len(tgt.Messages))
		return
	}
	msg := tgt.Messages[indx]

	CheckMsg(t, &msg, sender, rcpt)
}

func CheckMsg(t *testing.T, msg *Msg, sender string, rcpt []string) {
	t.Helper()

	idRaw := sha1.Sum([]byte(t.Name()))
	encodedId := hex.EncodeToString(idRaw[:])

	if msg.MsgMeta.ID != encodedId {
		t.Errorf("empty or wrong delivery context for passed message? %+v", msg.MsgMeta)
	}
	if msg.MailFrom != sender {
		t.Errorf("wrong sender, want %s, got %s", sender, msg.MailFrom)
	}

	sort.Strings(rcpt)
	sort.Strings(msg.RcptTo)
	if !reflect.DeepEqual(msg.RcptTo, rcpt) {
		t.Errorf("wrong recipients, want %v, got %v", rcpt, msg.RcptTo)
	}
	if string(msg.Body) != "foobar\r\n" {
		t.Errorf("wrong body, want '%s', got '%s'", "foobar", string(msg.Body))
	}
}
