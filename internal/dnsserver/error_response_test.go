package dnsserver

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
)

// buildQueryWithOPT returns a DNS query that includes an EDNS0 OPT
// pseudo-RR in the additional section, matching what every modern
// resolver (including dig with default options) sends. Used to
// reproduce the bug where errorResponse copied the entire query —
// including the OPT bytes — but reported ARCOUNT=0, leaving 23
// orphaned bytes at the end.
func buildQueryWithOPT(name string, qtype uint16) []byte {
	q := buildQuery(name, qtype)
	// Header now needs ARCOUNT=1 because we're adding an OPT.
	binary.BigEndian.PutUint16(q[10:12], 1)

	// OPT pseudo-RR: name=root(0), type=41, class=UDP-payload-size,
	// ttl=extended-rcode/flags, rdlength=12, rdata=COOKIE option
	// (option-code=10, option-length=8, 8-byte client cookie).
	// Total: 1 + 2 + 2 + 4 + 2 + (4 + 8) = 23 bytes — exactly the
	// length the dig "23 extra bytes at end" warning reports.
	opt := []byte{
		0x00,       // name: root
		0x00, 0x29, // type: OPT (41)
		0x10, 0x00, // CLASS: UDP payload size = 4096
		0x00, 0x00, 0x00, 0x00, // extended RCODE + version + flags
		0x00, 0x0c, // RDLENGTH = 12
		0x00, 0x0a, // option-code: COOKIE (10)
		0x00, 0x08, // option-length: 8
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, // client cookie
	}
	return append(q, opt...)
}

// TestErrorResponse_NoTrailingOPTBytes — the regression: an NXDOMAIN
// reply to a dig-with-EDNS query must not carry the original OPT
// pseudo-RR past the end of the section the header advertises.
// Length-of-response must equal exactly header(12) + question.
func TestErrorResponse_NoTrailingOPTBytes(t *testing.T) {
	query := buildQueryWithOPT("example.com", 6) // qtype 6 = SOA
	wantQuestion := query[12 : len(query)-23]    // strip the 23-byte OPT we appended
	wantLen := 12 + len(wantQuestion)

	resp, _ := BuildResponse(query, control.NewRegistry(), ZoneMap{}, nil)
	if len(resp) != wantLen {
		t.Fatalf("response len = %d, want %d (header + question, no trailing OPT)", len(resp), wantLen)
	}
	if got := binary.BigEndian.Uint16(resp[6:8]); got != 0 {
		t.Fatalf("ANCOUNT = %d, want 0", got)
	}
	if got := binary.BigEndian.Uint16(resp[10:12]); got != 0 {
		t.Fatalf("ARCOUNT = %d, want 0 (no OPT echoed)", got)
	}
	if got := binary.BigEndian.Uint16(resp[4:6]); got != 1 {
		t.Fatalf("QDCOUNT = %d, want 1", got)
	}
	if rcode := resp[3] & 0x0F; rcode != 3 {
		t.Fatalf("RCODE = %d, want 3 (NXDOMAIN)", rcode)
	}
}

// TestErrorResponse_FORMERROmitsQuestionSection — the FORMERR path
// (parseQueryName failed) shouldn't echo a question section that
// might itself be malformed. Header-only response with QDCOUNT=0
// is the safer answer.
func TestErrorResponse_FORMERROmitsQuestionSection(t *testing.T) {
	// Truncated query: header says QDCOUNT=1 but no question bytes
	// follow. parseQueryName should bail with qEnd=0, triggering
	// the FORMERR path.
	q := []byte{
		0x12, 0x34, // ID
		0x01, 0x00, // RD=1
		0x00, 0x01, // QDCOUNT=1
		0x00, 0x00, // ANCOUNT
		0x00, 0x00, // NSCOUNT
		0x00, 0x00, // ARCOUNT
		// no question section
	}
	resp, _ := BuildResponse(q, control.NewRegistry(), ZoneMap{}, nil)
	if len(resp) != 12 {
		t.Fatalf("FORMERR response len = %d, want 12 (header only)", len(resp))
	}
	if got := binary.BigEndian.Uint16(resp[4:6]); got != 0 {
		t.Fatalf("QDCOUNT = %d, want 0 on FORMERR with unparseable question", got)
	}
	if rcode := resp[3] & 0x0F; rcode != 1 {
		t.Fatalf("RCODE = %d, want 1 (FORMERR)", rcode)
	}
}

// TestErrorResponse_NoTrailingBytesOnSERVFAIL — same defense, this
// time on the SERVFAIL path (dns_servfail failure injection). The
// path runs before parseQueryName-driven NXDOMAIN, so the bug would
// have shown up here too if errorResponse still copied the query
// verbatim.
func TestErrorResponse_NoTrailingBytesOnSERVFAIL(t *testing.T) {
	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{Type: "dns_servfail", Rate: 1.0, Duration: time.Hour}, 0)

	query := buildQueryWithOPT("example.com", 1)
	wantLen := 12 + (len(query) - 12 - 23) // header + question, no OPT

	resp, _ := BuildResponse(query, registry, ZoneMap{}, nil)
	if len(resp) != wantLen {
		t.Fatalf("SERVFAIL response len = %d, want %d", len(resp), wantLen)
	}
	if rcode := resp[3] & 0x0F; rcode != 2 {
		t.Fatalf("RCODE = %d, want 2 (SERVFAIL)", rcode)
	}
}
