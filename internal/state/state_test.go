package state

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func fakeRedis(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	store := map[string]string{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				rd := bufio.NewReader(c)
				for {
					args, err := readArgs(rd)
					if err != nil {
						return
					}
					if len(args) == 0 {
						continue
					}
					cmd := strings.ToUpper(args[0])
					switch cmd {
					case "PING":
						fmt.Fprint(c, "+PONG\r\n")
					case "AUTH", "SELECT":
						fmt.Fprint(c, "+OK\r\n")
					case "SET":
						mu.Lock()
						store[args[1]] = args[2]
						if len(args) >= 5 && strings.EqualFold(args[3], "PX") {
							ms, _ := strconv.Atoi(args[4])
							time.AfterFunc(time.Duration(ms)*time.Millisecond, func() {
								mu.Lock()
								delete(store, args[1])
								mu.Unlock()
							})
						}
						mu.Unlock()
						fmt.Fprint(c, "+OK\r\n")
					case "GET":
						mu.Lock()
						v, ok := store[args[1]]
						mu.Unlock()
						if !ok {
							fmt.Fprint(c, "$-1\r\n")
						} else {
							fmt.Fprintf(c, "$%d\r\n%s\r\n", len(v), v)
						}
					case "INCRBY":
						delta, _ := strconv.ParseInt(args[2], 10, 64)
						mu.Lock()
						cur, _ := strconv.ParseInt(store[args[1]], 10, 64)
						cur += delta
						store[args[1]] = strconv.FormatInt(cur, 10)
						mu.Unlock()
						fmt.Fprintf(c, ":%d\r\n", cur)
					case "INCRBYFLOAT":
						delta, _ := strconv.ParseFloat(args[2], 64)
						mu.Lock()
						cur, _ := strconv.ParseFloat(store[args[1]], 64)
						cur += delta
						out := strconv.FormatFloat(cur, 'f', -1, 64)
						store[args[1]] = out
						mu.Unlock()
						fmt.Fprintf(c, "$%d\r\n%s\r\n", len(out), out)
					default:
						fmt.Fprintf(c, "-ERR unknown command '%s'\r\n", cmd)
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func readArgs(rd *bufio.Reader) ([]string, error) {
	line, err := rd.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("not array")
	}
	n, _ := strconv.Atoi(line[1:])
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		hdr, err := rd.ReadString('\n')
		if err != nil {
			return nil, err
		}
		hdr = strings.TrimRight(hdr, "\r\n")
		if !strings.HasPrefix(hdr, "$") {
			return nil, fmt.Errorf("not bulk")
		}
		l, _ := strconv.Atoi(hdr[1:])
		buf := make([]byte, l+2)
		if _, err := ioReadFull(rd, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:l]))
	}
	return args, nil
}

func TestRedisStoreAgainstFakeServer(t *testing.T) {
	addr := fakeRedis(t)
	rs, err := NewRedis("redis://"+addr, "test:")
	if err != nil {
		t.Fatal(err)
	}
	if !rs.Healthy() {
		t.Fatal("expected healthy")
	}

	n, err := rs.IncrWindow("rpm:k1", time.Minute, 1)
	if err != nil || n != 1 {
		t.Fatalf("incr1 = %d %v", n, err)
	}
	n, _ = rs.IncrWindow("rpm:k1", time.Minute, 2)
	if n != 3 {
		t.Fatalf("incr2 = %d", n)
	}
	got, _ := rs.GetCounter("rpm:k1")
	if got != 3 {
		t.Fatalf("counter = %d", got)
	}

	f, _ := rs.IncrFloat("budget:x", 0.25)
	f, _ = rs.IncrFloat("budget:x", 1.5)
	if f < 1.74 || f > 1.76 {
		t.Fatalf("float = %v", f)
	}
	gf, _ := rs.GetFloat("budget:x")
	if gf != f {
		t.Fatalf("getfloat mismatch: %v vs %v", gf, f)
	}

	if err := rs.SetTTLBytes("cache:k", []byte("hello"), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	raw, found, err := rs.GetBytes("cache:k")
	if err != nil || !found || string(raw) != "hello" {
		t.Fatalf("cache roundtrip: %v %v %s", found, err, raw)
	}
	if _, found, _ := rs.GetBytes("cache:missing"); found {
		t.Fatal("missing key must not be found")
	}

	prefixed := rs.GetPrefix()
	if !strings.HasPrefix(prefixed, "test:") {
		t.Fatalf("prefix = %q", prefixed)
	}
}

func TestMemoryStoreBasics(t *testing.T) {
	m := NewMemory()
	n, _ := m.IncrWindow("w", time.Minute, 5)
	if n != 5 {
		t.Fatalf("n=%d", n)
	}
	f, _ := m.IncrFloat("f", 1.25)
	f, _ = m.IncrFloat("f", 2.5)
	if f != 3.75 {
		t.Fatalf("f=%v", f)
	}
	m.SetTTLBytes("b", []byte("xyz"), 0)
	v, ok, _ := m.GetBytes("b")
	if !ok || string(v) != "xyz" {
		t.Fatal("bytes roundtrip failed")
	}
}
