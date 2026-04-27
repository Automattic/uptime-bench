package dnsserver

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
)

// buildQueryA encodes a minimal DNS A-record query for name with transaction id 0x1234.
func buildQueryA(name string) []byte {
	var qname []byte
	for _, label := range bytes.Split([]byte(name), []byte(".")) {
		qname = append(qname, byte(len(label)))
		qname = append(qname, label...)
	}
	qname = append(qname, 0)

	msg := []byte{
		0x12, 0x34, // ID
		0x01, 0x00, // RD=1
		0x00, 0x01, // QDCOUNT=1
		0x00, 0x00, // ANCOUNT
		0x00, 0x00, // NSCOUNT
		0x00, 0x00, // ARCOUNT
	}
	msg = append(msg, qname...)
	msg = append(msg, 0x00, 0x01) // TYPE A
	msg = append(msg, 0x00, 0x01) // CLASS IN
	return msg
}

// TestReadTCPMessage_PartialRead is a regression test for the bug where
// HandleTCP used conn.Read directly and lost bytes when the kernel returned
// a short read. Wrapping the input in iotest.OneByteReader forces every read
// to return one byte at a time; readTCPMessage must consume the full message.
func TestReadTCPMessage_PartialRead(t *testing.T) {
	query := buildQueryA("example.com")
	framed := make([]byte, 2+len(query))
	binary.BigEndian.PutUint16(framed[:2], uint16(len(query)))
	copy(framed[2:], query)

	r := iotest.OneByteReader(bytes.NewReader(framed))
	got, err := readTCPMessage(r)
	if err != nil {
		t.Fatalf("readTCPMessage: %v", err)
	}
	if !bytes.Equal(got, query) {
		t.Fatalf("got %x, want %x", got, query)
	}
}

func TestReadTCPMessage_RejectsZeroLength(t *testing.T) {
	r := bytes.NewReader([]byte{0x00, 0x00})
	if _, err := readTCPMessage(r); err == nil {
		t.Fatal("expected error for zero-length, got nil")
	}
}

func TestReadTCPMessage_RejectsTruncated(t *testing.T) {
	// Length prefix says 5 bytes, but only 2 follow.
	r := bytes.NewReader([]byte{0x00, 0x05, 0xab, 0xcd})
	_, err := readTCPMessage(r)
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("got %v, want io.ErrUnexpectedEOF", err)
	}
}

// TestHandleTCP_DeadlineEnforced is a regression test for the bug where the
// TCP handler had no read deadline, letting an idle client hold a goroutine
// indefinitely. We open a real TCP connection, never write to it, and verify
// the handler returns within TCPReadTimeout + a small slop.
func TestHandleTCP_DeadlineEnforced(t *testing.T) {
	if testing.Short() {
		t.Skip("uses real timer; skip in -short mode")
	}
	// Use a temporarily reduced timeout for the test by relying on the
	// constant. We cannot mutate it portably, so we just assert the handler
	// completes within TCPReadTimeout + 1s of slop.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	registry := control.NewRegistry()
	zones := testZones(nil)

	done := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			close(done)
			return
		}
		HandleTCP(conn, registry, zones, nil)
		close(done)
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	start := time.Now()
	select {
	case <-done:
		elapsed := time.Since(start)
		if elapsed > TCPReadTimeout+2*time.Second {
			t.Fatalf("handler took %v, expected ≤ %v", elapsed, TCPReadTimeout+2*time.Second)
		}
	case <-time.After(TCPReadTimeout + 3*time.Second):
		t.Fatalf("handler did not return within %v — deadline missing?", TCPReadTimeout+3*time.Second)
	}
}

// TestServeUDP_LatencyDoesNotSerialize is a regression test for the bug
// where dns_latency caused all UDP queries to serialize behind one
// time.Sleep in the read loop. After the fix each query is dispatched to
// its own goroutine, so two queries with a 500ms latency overlap rather
// than stacking to 1s.
func TestServeUDP_LatencyDoesNotSerialize(t *testing.T) {
	if testing.Short() {
		t.Skip("uses real timer; skip in -short mode")
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{
		Type:     "dns_latency",
		Duration: 10 * time.Second,
		Rate:     1.0,
		Params:   map[string]any{"added_latency": "500ms"},
	}, 0)
	zones := testZones(ZoneMap{
		"example.com": {IP: net.ParseIP("10.0.0.1"), TTL: 30},
	})

	go ServeUDP(pc, registry, zones, nil)
	addr := pc.LocalAddr().(*net.UDPAddr)
	query := buildQueryA("example.com")

	// Fire two queries back-to-back, time how long both responses take to arrive.
	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			c, err := net.DialUDP("udp", nil, addr)
			if err != nil {
				t.Errorf("dial: %v", err)
				return
			}
			defer c.Close()
			if _, err := c.Write(query); err != nil {
				t.Errorf("write: %v", err)
				return
			}
			c.SetReadDeadline(time.Now().Add(3 * time.Second))
			buf := make([]byte, 512)
			if _, err := c.Read(buf); err != nil {
				t.Errorf("read: %v", err)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Serialized: two 500ms sleeps in sequence ≈ 1s.
	// Parallel: both sleep concurrently ≈ 500ms.
	// Allow generous slop for goroutine scheduling.
	if elapsed > 800*time.Millisecond {
		t.Fatalf("two parallel queries took %v with 500ms latency each — they appear to serialize", elapsed)
	}
}

// TestBuildResponse_DNSLatencyReportsDelay confirms the latency value
// flows from the registry through BuildResponse to the caller, who is
// responsible for the actual sleep.
func TestBuildResponse_DNSLatencyReportsDelay(t *testing.T) {
	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{
		Type:     "dns_latency",
		Duration: 10 * time.Second,
		Rate:     1.0,
		Params:   map[string]any{"added_latency": "250ms"},
	}, 0)
	zones := testZones(ZoneMap{
		"example.com": {IP: net.ParseIP("10.0.0.1"), TTL: 30},
	})

	_, delay := BuildResponse(buildQueryA("example.com"), registry, zones, nil)
	if delay != 250*time.Millisecond {
		t.Fatalf("got delay %v, want 250ms", delay)
	}
}
