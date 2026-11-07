package stripe

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestVerifyWebhook(t *testing.T) {
	now := time.Unix(1800000000, 0)
	payload := []byte(`{"id":"evt_1","type":"invoice.paid","api_version":"2026-10-28.endive","data":{"object":{"customer":"cus_1"}}}`)
	header := SignatureHeader(payload, "new", now)
	event, err := VerifyWebhook(payload, header+",v1=bad", []string{"old", "new"}, now)
	if err != nil || event.ID != "evt_1" || event.Customer() != "cus_1" || event.APIVersion != APIVersion {
		t.Fatalf("event = %+v, error = %v", event, err)
	}
	cases := []struct {
		name    string
		payload []byte
		header  string
		secrets []string
		now     time.Time
	}{
		{"missing header", payload, "", []string{"new"}, now},
		{"missing timestamp", payload, "v1=abcd", []string{"new"}, now},
		{"bad timestamp", payload, "t=bad,v1=abcd", []string{"new"}, now},
		{"missing v1", payload, "t=1800000000,v0=abcd", []string{"new"}, now},
		{"old", payload, header, []string{"new"}, now.Add(301 * time.Second)},
		{"future", payload, header, []string{"new"}, now.Add(-301 * time.Second)},
		{"no secrets", payload, header, nil, now},
		{"blank secret", payload, header, []string{"new", " "}, now},
		{"wrong secret", payload, header, []string{"wrong"}, now},
		{"changed payload", append(append([]byte(nil), payload...), ' '), header, []string{"new"}, now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := VerifyWebhook(tc.payload, tc.header, tc.secrets, tc.now); err == nil {
				t.Fatal("accepted invalid webhook")
			}
		})
	}
	for _, body := range []string{`null`, `[]`, `{`, `{"id":"evt_1"}`, `{"type":"invoice.paid"}`} {
		if _, err := VerifyWebhook([]byte(body), SignatureHeader([]byte(body), "new", now), []string{"new"}, now); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	for _, delta := range []time.Duration{-300 * time.Second, 300 * time.Second} {
		if _, err := VerifyWebhook(payload, header, []string{"new"}, now.Add(delta)); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasPrefix(header, "t=1800000000,v1=") {
		t.Fatal(header)
	}
}

func TestEventCustomer(t *testing.T) {
	for _, tc := range []struct{ object, want string }{
		{`{"object":"customer","id":"cus_2"}`, "cus_2"},
		{`{"customer":{"id":"cus_3"}}`, "cus_3"},
		{`{"id":"sub_1"}`, ""}, {`null`, ""}, {`{"customer":42}`, ""},
	} {
		e := Event{}
		e.Data.Object = json.RawMessage(tc.object)
		if got := e.Customer(); got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.object, got, tc.want)
		}
	}
}

func TestSignatureKnownVector(t *testing.T) {
	payload := []byte(`{"id":"evt_vector","type":"invoice.paid"}`)
	now := time.Unix(1800000000, 0)
	expected := "t=1800000000,v1=19074b235e1550fe807a3ad49959bbdcb5c64fc6adb89b12b46dd4347bfd96e8"
	if got := SignatureHeader(payload, "whsec_vector", now); got != expected {
		t.Fatalf("signature = %s", got)
	}
	header := "t=1800000000,v1=" + strings.Repeat("0", 64) + "," + strings.SplitN(expected, ",", 2)[1]
	if _, err := VerifyWebhook(payload, header, []string{"wrong", "whsec_vector"}, now); err != nil {
		t.Fatal(err)
	}
}
