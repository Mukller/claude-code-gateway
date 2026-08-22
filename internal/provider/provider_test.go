package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSignAWSRequestDocVector(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet,
		"https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	creds := awsCreds{
		accessKey: "AKIDEXAMPLE",
		secretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
	}
	now := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	signAWSRequest(req, nil, creds, "us-east-1", "iam", now)

	auth := req.Header.Get("Authorization")
	wantSig := "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if !strings.Contains(auth, "Signature="+wantSig) {
		t.Fatalf("signature mismatch\n got: %s\nwant substring: Signature=%s", auth, wantSig)
	}
	if !strings.Contains(auth, "Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request") {
		t.Fatalf("bad credential scope: %s", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=content-type;host;x-amz-date") {
		t.Fatalf("unexpected signed headers: %s", auth)
	}
}

func TestAWSEscape(t *testing.T) {
	cases := map[string]string{
		"claude-v1":                  "claude-v1",
		"us.anthropic.claude-4:v1:0": "us.anthropic.claude-4%3Av1%3A0",
		"a b/c&d":                    "a%20b/c%26d",
	}
	for in, want := range cases {
		if got := AWSEscape(in); got != want {
			t.Fatalf("AWSEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

type namedHeader struct {
	name  string
	value string
}

func buildFrame(msgType, contentType, payload string, rawPayload bool) []byte {
	var hdr bytes.Buffer
	for _, h := range []namedHeader{
		{":message-type", msgType},
		{":content-type", contentType},
	} {
		hdr.WriteByte(byte(len(h.name)))
		hdr.WriteString(h.name)
		hdr.WriteByte(7)
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(h.value)))
		hdr.Write(l[:])
		hdr.WriteString(h.value)
	}
	headers := hdr.Bytes()

	var payloadBytes []byte
	if rawPayload {
		payloadBytes = []byte(payload)
	} else {
		payloadBytes, _ = json.Marshal(map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte(payload))})
	}

	total := 12 + len(headers) + len(payloadBytes) + 4
	frame := make([]byte, 0, total)
	var u32 [4]byte
	binary.BigEndian.PutUint32(u32[:], uint32(total))
	frame = append(frame, u32[:]...)
	binary.BigEndian.PutUint32(u32[:], uint32(len(headers)))
	frame = append(frame, u32[:]...)
	frame = append(frame, 0, 0, 0, 0)
	frame = append(frame, headers...)
	frame = append(frame, payloadBytes...)
	frame = append(frame, 0, 0, 0, 0)
	return frame
}

func TestBedrockStreamDecode(t *testing.T) {
	inner1 := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"
	inner2 := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	raw := append(buildFrame("event", "application/json", inner1, false),
		buildFrame("event", "application/json", inner2, false)...)

	got, err := io.ReadAll(WrapBedrockStream(bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != inner1+inner2 {
		t.Fatalf("decoded mismatch:\n%q\nwant %q", got, inner1+inner2)
	}
}

func TestBedrockStreamException(t *testing.T) {
	raw := buildFrame("exception", "application/json", `{"message":"ThrottlingException"}`, true)
	_, err := io.ReadAll(WrapBedrockStream(bytes.NewReader(raw)))
	if err == nil || !strings.Contains(err.Error(), "ThrottlingException") {
		t.Fatalf("expected exception error, got %v", err)
	}
}

func TestParseAWSKey(t *testing.T) {
	c, err := parseAWSKey("AKID:secret")
	if err != nil || c.accessKey != "AKID" || c.secretKey != "secret" || c.sessionToken != "" {
		t.Fatalf("basic parse failed: %+v %v", c, err)
	}
	c, err = parseAWSKey("AKID:secret:SESSION")
	if err != nil || c.sessionToken != "SESSION" {
		t.Fatalf("session parse failed: %+v %v", c, err)
	}
	if _, err := parseAWSKey("justonepart"); err == nil {
		t.Fatal("expected error for malformed key")
	}
}
