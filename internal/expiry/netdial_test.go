package expiry

import (
	"bufio"
	"context"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// TestRegistryNetworkPinsTheFamily covers the mapping between the admin setting
// and the network string handed to net.Dialer.
func TestRegistryNetworkPinsTheFamily(t *testing.T) {
	cases := map[string]string{
		"":     "tcp",
		"auto": "tcp",
		"junk": "tcp",
		"ipv4": "tcp4",
		"IPv4": "tcp4",
		"4":    "tcp4",
		"tcp4": "tcp4",
		"ipv6": "tcp6",
		"v6":   "tcp6",
		"tcp6": "tcp6",
	}
	for input, want := range cases {
		if got := registryNetwork(input); got != want {
			t.Fatalf("registryNetwork(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestFamilyNetwork covers the per-address family of the diagnostic pass: an
// IPv4-mapped IPv6 address is an IPv4 address.
func TestFamilyNetwork(t *testing.T) {
	cases := map[string]string{
		"52.37.99.5":        "tcp4",
		"::ffff:52.37.99.5": "tcp4",
		"::1":               "tcp6",
		"2600:1f13::1":      "tcp6",
	}
	for input, want := range cases {
		if got := familyNetwork(net.ParseIP(input)); got != want {
			t.Fatalf("familyNetwork(%s) = %q, want %q", input, got, want)
		}
	}
}

// startWhoisStub listens on the loopback of one family and answers every
// connection with a minimal registry response. It skips the test when the family
// cannot be bound at all.
func startWhoisStub(t *testing.T, host string) string {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("cannot listen on %s (%v)", host, err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Answer AFTER reading the query: closing with unread data would
			// reset the connection and the client would see ECONNRESET instead
			// of the response.
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				_, _ = bufio.NewReader(conn).ReadString('\n')
				_, _ = conn.Write([]byte("Registry Expiry Date: 2028-01-09T00:00:00Z\r\n"))
			}(conn)
		}
	}()
	return listener.Addr().String()
}

// TestWhoisQueryHonorsThePinnedFamily is the regression test of the incident the
// setting exists for: the family the operator pinned must be the one that is
// used (and named in the error), never silently replaced by the other one.
func TestWhoisQueryHonorsThePinnedFamily(t *testing.T) {
	ipv4Target := startWhoisStub(t, "127.0.0.1")
	client4 := &WhoisClient{Timeout: 2 * time.Second, IPVersion: models.ExpiryIPVersion4}
	raw, err := client4.Query(context.Background(), ipv4Target, "example.io")
	if err != nil {
		t.Fatalf("Query over ipv4: %v", err)
	}
	if !strings.Contains(raw, "Registry Expiry Date") {
		t.Fatalf("raw = %q", raw)
	}

	pinned6 := &WhoisClient{Timeout: 2 * time.Second, IPVersion: models.ExpiryIPVersion6}
	if _, err := pinned6.Query(context.Background(), ipv4Target, "example.io"); err == nil {
		t.Fatal("tcp6 must not fall back to an IPv4 address")
	} else if !strings.Contains(err.Error(), "tcp6") {
		t.Fatalf("the error must name tcp6, got %v", err)
	}

	ipv6Target := startWhoisStub(t, "::1")
	if _, err := client4.Query(context.Background(), ipv6Target, "example.io"); err == nil {
		t.Fatal("tcp4 must not fall back to an IPv6 address")
	}
	client6 := &WhoisClient{Timeout: 2 * time.Second, IPVersion: models.ExpiryIPVersion6}
	if _, err := client6.Query(context.Background(), ipv6Target, "example.io"); err != nil {
		t.Fatalf("Query over ipv6: %v", err)
	}

	// An unset family keeps Go's happy eyeballs and still reaches loopback.
	auto := &WhoisClient{Timeout: 2 * time.Second}
	if _, err := auto.Query(context.Background(), ipv4Target, "example.io"); err != nil {
		t.Fatalf("Query over auto: %v", err)
	}
}

// TestDescribeDialAttempts pins the message the operator reads: every resolved
// address with its own error, plus the hint that explains an IPv4-only container
// and the one that spots throttling.
func TestDescribeDialAttempts(t *testing.T) {
	ipv4 := net.ParseIP("52.37.99.5")
	ipv6 := net.ParseIP("2600:1f13:101:c200:9b9b:7011:8bd8:a4af")
	timeout := &net.OpError{Op: "dial", Net: "tcp4", Addr: &net.TCPAddr{IP: ipv4, Port: 43}, Err: os.ErrDeadlineExceeded}
	unreachable := &net.OpError{Op: "dial", Net: "tcp6", Addr: &net.TCPAddr{IP: ipv6, Port: 43}, Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}

	message := describeDialAttempts("whois.nic.io", []dialAttempt{{ip: ipv4, err: timeout}, {ip: ipv6, err: unreachable}})
	for _, want := range []string{"whois.nic.io", "52.37.99.5", "2600:1f13", "no IPv6 route"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message %q is missing %q", message, want)
		}
	}

	// An address that answers on the probe points at throttling instead.
	message = describeDialAttempts("whois.nic.io", []dialAttempt{{ip: ipv4, err: timeout}, {ip: ipv4}})
	if !strings.Contains(message, "rate limited") {
		t.Fatalf("message = %q", message)
	}
}

// TestUnreachableNetwork tells "this host has no route to that family" apart from
// a refused connection or a timeout.
func TestUnreachableNetwork(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp4", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	if unreachableNetwork(refused) {
		t.Fatal("a refused connection is not an unreachable family")
	}
	if !unreachableNetwork(&net.OpError{Op: "dial", Net: "tcp6", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}) {
		t.Fatal("ENETUNREACH must be recognised")
	}
	if !unreachableNetwork(&net.OpError{Op: "dial", Net: "tcp6", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}) {
		t.Fatal("EHOSTUNREACH must be recognised")
	}
}

// TestDialRegistryExplainsAFailedDial covers the diagnostic pass end to end: a
// failed "auto" dial names the address it probed, a pinned family keeps the
// original error untouched.
func TestDialRegistryExplainsAFailedDial(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	target := listener.Addr().String()
	_ = listener.Close() // the port is closed now: every dial is refused

	if _, err := dialRegistry(context.Background(), models.ExpiryIPVersionAuto, target, time.Second); err == nil {
		t.Fatal("dialing a closed port must fail")
	} else if !strings.Contains(err.Error(), "failed:") || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("the diagnostic error must name the probed address, got %v", err)
	}

	if _, err := dialRegistry(context.Background(), models.ExpiryIPVersion4, target, time.Second); err == nil {
		t.Fatal("dialing a closed port must fail")
	} else if strings.Contains(err.Error(), "failed:") {
		t.Fatalf("a pinned family must keep the original error, got %v", err)
	}
}
