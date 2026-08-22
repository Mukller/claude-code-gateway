package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

type eventStreamReader struct {
	src io.Reader
	buf bytes.Buffer
	eof bool
}

func WrapBedrockStream(r io.Reader) io.Reader { return &eventStreamReader{src: r} }

func BedrockBody(rc io.ReadCloser) io.ReadCloser {
	return struct {
		io.Reader
		io.Closer
	}{WrapBedrockStream(rc), rc}
}

func (e *eventStreamReader) Read(p []byte) (int, error) {
	for e.buf.Len() == 0 {
		if e.eof {
			return 0, io.EOF
		}
		err := e.nextFrame()
		if err != nil {
			e.eof = true
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return 0, io.EOF
			}
			return 0, err
		}
	}
	return e.buf.Read(p)
}

func (e *eventStreamReader) readFull(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(e.src, b); err != nil {
		return nil, err
	}
	return b, nil
}

func (e *eventStreamReader) nextFrame() error {
	prelude, err := e.readFull(12)
	if err != nil {
		return err
	}
	total := int(binary.BigEndian.Uint32(prelude[0:4]))
	headersLen := int(binary.BigEndian.Uint32(prelude[4:8]))
	if total < 16 || headersLen > total-16 {
		return fmt.Errorf("eventstream: bad frame prelude")
	}
	rest, err := e.readFull(total - 12)
	if err != nil {
		return err
	}
	headers := rest[:headersLen]
	payload := rest[headersLen : total-12-4]

	msgType, contentType := "", ""
	hp := headers
	for len(hp) > 0 {
		nl := int(hp[0])
		hp = hp[1:]
		if nl > len(hp) {
			return fmt.Errorf("eventstream: bad header name")
		}
		name := string(hp[:nl])
		hp = hp[nl:]
		if len(hp) < 1 {
			return fmt.Errorf("eventstream: truncated header")
		}
		vt := hp[0]
		hp = hp[1:]
		var val []byte
		skip := 0
		switch vt {
		case 0, 1:
		case 2:
			skip = 1
		case 3:
			skip = 2
		case 4:
			skip = 4
		case 5, 8:
			skip = 8
		case 9:
			skip = 16
		case 6, 7:
			if len(hp) < 2 {
				return fmt.Errorf("eventstream: truncated header value")
			}
			vl := int(binary.BigEndian.Uint16(hp[:2]))
			hp = hp[2:]
			if vl > len(hp) {
				return fmt.Errorf("eventstream: bad header value length")
			}
			val = hp[:vl]
			hp = hp[vl:]
		default:
			return fmt.Errorf("eventstream: unknown header type %d", vt)
		}
		if skip > 0 {
			if skip > len(hp) {
				return fmt.Errorf("eventstream: truncated fixed header")
			}
			hp = hp[skip:]
		}
		switch name {
		case ":message-type":
			msgType = string(val)
		case ":content-type":
			contentType = string(val)
		}
	}

	if msgType == "exception" {
		return fmt.Errorf("bedrock stream exception: %s", truncateBytes(payload, 300))
	}
	if msgType != "event" {
		return nil
	}
	if contentType == "application/json" {
		var ev struct {
			Bytes string `json:"bytes"`
		}
		if json.Unmarshal(payload, &ev) == nil && ev.Bytes != "" {
			raw, derr := base64.StdEncoding.DecodeString(ev.Bytes)
			if derr == nil {
				e.buf.Write(raw)
			}
		}
		return nil
	}
	e.buf.Write(payload)
	return nil
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
