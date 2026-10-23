package provision

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	rkOld  = "ssh-ed25519 AAAAold"
	rkNext = "ssh-ed25519 AAAAnext"
)

type rotHarness struct {
	status, next, cur string
	runErr            map[string]error
	calls, events     []string
	pins              [][]string
	out               bytes.Buffer
}

func (h *rotHarness) rotation(keys ...string) rotation {
	return rotation{
		Keys: keys, Out: &h.out,
		Run: func(_ context.Context, argv ...string) (string, error) {
			cmd := argv[len(argv)-1]
			h.calls = append(h.calls, cmd)
			if err := h.runErr[cmd]; err != nil {
				return "", err
			}
			switch cmd {
			case "status":
				return h.status + "\n", nil
			case "next":
				return h.next + " comment\n", nil
			}
			return h.cur + "\n", nil
		},
		Pin: func(k []string) error {
			h.pins = append(h.pins, append([]string{}, k...))
			h.events = append(h.events, "pin")
			return nil
		},
		Reconnect: func(context.Context) error { h.events = append(h.events, "reconnect"); return nil },
	}
}

func TestRotateFresh(t *testing.T) {
	h := &rotHarness{status: "pending", next: rkNext, cur: rkNext}
	got, err := rotateHostKey(context.Background(), h.rotation(rkOld))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{rkNext}) {
		t.Fatalf("got %v", got)
	}
	if want := [][]string{{rkOld, rkNext}, {rkNext}}; !reflect.DeepEqual(h.pins, want) {
		t.Fatalf("pins %v", h.pins)
	}
	if want := []string{"pin", "reconnect", "pin"}; !reflect.DeepEqual(h.events, want) {
		t.Fatalf("events %v", h.events)
	}
}

func TestRotateAlreadyRotated(t *testing.T) {
	h := &rotHarness{status: "rotated"}
	got, err := rotateHostKey(context.Background(), h.rotation(rkNext))
	if err != nil || !reflect.DeepEqual(got, []string{rkNext}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if len(h.pins) != 0 || !reflect.DeepEqual(h.calls, []string{"status"}) {
		t.Fatalf("pins %v calls %v", h.pins, h.calls)
	}
}

func TestRotateRerunAfterNext(t *testing.T) {
	// A crash after next and Pin: both keys are pinned, the VM still says pending.
	h := &rotHarness{status: "pending", next: rkNext, cur: rkNext}
	got, err := rotateHostKey(context.Background(), h.rotation(rkOld, rkNext))
	if err != nil || !reflect.DeepEqual(got, []string{rkNext}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if h.pins[0][0] != rkOld || len(h.pins[0]) != 2 {
		t.Fatalf("pins %v", h.pins)
	}
}

func TestRotateRerunAfterActivate(t *testing.T) {
	h := &rotHarness{status: "rotated", cur: rkNext}
	got, err := rotateHostKey(context.Background(), h.rotation(rkOld, rkNext))
	if err != nil || !reflect.DeepEqual(got, []string{rkNext}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if want := [][]string{{rkNext}}; !reflect.DeepEqual(h.pins, want) {
		t.Fatalf("pins %v", h.pins)
	}
	if want := []string{"reconnect", "pin"}; !reflect.DeepEqual(h.events, want) {
		t.Fatalf("events %v", h.events)
	}
}

func TestRotateToolMissing(t *testing.T) {
	h := &rotHarness{runErr: map[string]error{"status": errors.New("sudo: /usr/local/sbin/spinup-host-key: command not found")}}
	got, err := rotateHostKey(context.Background(), h.rotation(rkOld))
	if err != nil || !reflect.DeepEqual(got, []string{rkOld}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if !strings.Contains(h.out.String(), "warning") || len(h.pins) != 0 {
		t.Fatalf("out %q pins %v", h.out.String(), h.pins)
	}
}

func TestRotateServedKeyNotPinned(t *testing.T) {
	h := &rotHarness{status: "rotated", cur: "ssh-ed25519 AAAAother"}
	_, err := rotateHostKey(context.Background(), h.rotation(rkOld, rkNext))
	if err == nil || !strings.Contains(err.Error(), "Try:") || len(h.pins) != 0 {
		t.Fatalf("err %v pins %v", err, h.pins)
	}
}

func TestRotateVMDidNotTakeKey(t *testing.T) {
	h := &rotHarness{status: "pending", next: rkNext, cur: rkOld}
	_, err := rotateHostKey(context.Background(), h.rotation(rkOld))
	if err == nil || !strings.Contains(err.Error(), "did not take") {
		t.Fatalf("err %v", err)
	}
	if len(h.pins) != 1 {
		t.Fatalf("pins %v", h.pins)
	}
}

func TestRotateBadKeyAndErrors(t *testing.T) {
	h := &rotHarness{status: "pending", next: "not a key"}
	if _, err := rotateHostKey(context.Background(), h.rotation(rkOld)); err == nil {
		t.Fatal("no error for bad key")
	}
	h = &rotHarness{status: "weird"}
	if _, err := rotateHostKey(context.Background(), h.rotation(rkOld)); err == nil {
		t.Fatal("no error for bad status")
	}
	h = &rotHarness{status: "pending", runErr: map[string]error{"status": errors.New("connection reset")}}
	if _, err := rotateHostKey(context.Background(), h.rotation(rkOld)); err == nil {
		t.Fatal("no error for run failure")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rotateHostKey(ctx, h.rotation(rkOld)); err == nil {
		t.Fatal("no error for canceled context")
	}
}
