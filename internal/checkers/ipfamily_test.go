package checkers

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// TestPlanFor is the table of the whole feature: the combination of what the
// monitor asks for (`config.ip_family`) and the turn of the worker decides which
// family the probe insists on and whether that family is negotiable.
func TestPlanFor(t *testing.T) {
	cases := []struct {
		name       string
		preference string
		turn       string
		want       probePlan
	}{
		{"rotation on the ipv4 turn", models.IPFamilyAlternate, models.IPFamily4, probePlan{family: models.IPFamily4}},
		{"rotation on the ipv6 turn", models.IPFamilyAlternate, models.IPFamily6, probePlan{family: models.IPFamily6}},
		{"rotation without a turn pins nothing", models.IPFamilyAlternate, "", probePlan{}},
		{"rotation ignores an unreadable turn", models.IPFamilyAlternate, "nope", probePlan{}},
		{"auto (the default) never pins", models.IPFamilyAuto, models.IPFamily6, probePlan{}},
		{"an empty preference is the default (auto), which never pins", "", models.IPFamily4, probePlan{}},
		{"an unreadable preference falls back to the plain dial", "nope", models.IPFamily6, probePlan{}},
		{"ipv4 is a hard pin and ignores the turn", models.IPFamily4, models.IPFamily6, probePlan{family: models.IPFamily4, hard: true}},
		{"ipv6 is a hard pin", models.IPFamily6, models.IPFamily4, probePlan{family: models.IPFamily6, hard: true}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := planFor(testCase.preference, testCase.turn); got != testCase.want {
				t.Fatalf("planFor(%q, %q) = %+v, want %+v", testCase.preference, testCase.turn, got, testCase.want)
			}
		})
	}
}

// TestLiteralFamily covers the classification of an IP literal, including the
// IPv4-mapped form that net.Dialer itself reads as IPv4.
func TestLiteralFamily(t *testing.T) {
	cases := map[string]string{
		"52.37.99.5":        models.IPFamily4,
		"::ffff:52.37.99.5": models.IPFamily4,
		"::1":               models.IPFamily6,
		"2600:1f13::1":      models.IPFamily6,
	}
	for input, want := range cases {
		if got := literalFamily(net.ParseIP(input)); got != want {
			t.Errorf("literalFamily(%s) = %q, want %q", input, got, want)
		}
	}
}

// TestProbeTarget is the core of the soft rotation: the family is only insisted
// on when the target really has an address in it, so a single stack target keeps
// working while a hard pin always names the family the operator asked for.
func TestProbeTarget(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name        string
		plan        probePlan
		address     string
		wantNetwork string
		wantTarget  string
	}{
		{
			name:        "no preference keeps the happy eyeballs dial",
			plan:        probePlan{},
			address:     "example.com:443",
			wantNetwork: "tcp",
			wantTarget:  "example.com:443",
		},
		{
			name:        "a soft ipv6 turn keeps the name when the literal is ipv4",
			plan:        probePlan{family: models.IPFamily6},
			address:     "127.0.0.1:8080",
			wantNetwork: "tcp",
			wantTarget:  "127.0.0.1:8080",
		},
		{
			name:        "a soft ipv4 turn uses the literal the target has",
			plan:        probePlan{family: models.IPFamily4},
			address:     "127.0.0.1:8080",
			wantNetwork: "tcp4",
			wantTarget:  "127.0.0.1:8080",
		},
		{
			name:        "a hard pin keeps the family the target does not have",
			plan:        probePlan{family: models.IPFamily6, hard: true},
			address:     "127.0.0.1:8080",
			wantNetwork: "tcp6",
			wantTarget:  "127.0.0.1:8080",
		},
		{
			name:        "a hard pin survives an address without a port",
			plan:        probePlan{family: models.IPFamily6, hard: true},
			address:     "example.com",
			wantNetwork: "tcp6",
			wantTarget:  "example.com",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			network, target := probeTarget(ctx, testCase.plan, testCase.address)
			if network != testCase.wantNetwork || target != testCase.wantTarget {
				t.Fatalf("probeTarget(%+v, %q) = (%q, %q), want (%q, %q)",
					testCase.plan, testCase.address, network, target, testCase.wantNetwork, testCase.wantTarget)
			}
		})
	}
}

// TestProbeTargetResolvesANameToTheFamily covers the lookup step: with a family
// to insist on, a NAME is resolved here so the chosen address can be dialed as a
// literal (one path per execution). localhost is the one name whose answer is
// known locally; the assertion adapts to what the resolver of the machine
// actually publishes.
func TestProbeTargetResolvesANameToTheFamily(t *testing.T) {
	ctx := context.Background()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, "localhost")
	if err != nil {
		t.Skipf("localhost does not resolve here: %v", err)
	}
	for _, family := range []string{models.IPFamily4, models.IPFamily6} {
		has, other := false, false
		for _, entry := range ips {
			if literalFamily(entry.IP) == family {
				has = true
			} else {
				other = true
			}
		}
		network, target := probeTarget(ctx, probePlan{family: family}, "localhost:9")
		if has {
			if want := "tcp" + models.NetworkFor(family); network != want {
				t.Fatalf("%s: network = %q, want %q", family, network, want)
			}
			host, _, err := net.SplitHostPort(target)
			if err != nil {
				t.Fatalf("%s: target = %q: %v", family, target, err)
			}
			literal := net.ParseIP(host)
			if literal == nil || literalFamily(literal) != family {
				t.Fatalf("%s: target = %q, want a %s literal", family, target, family)
			}
			continue
		}
		if other && network != "tcp" {
			t.Fatalf("%s: a family the name does not have must fall back, got %q", family, network)
		}
	}
}

// TestCheckTCPPinsTheFamily is the end to end proof of the feature: a hard pin
// against a literal of the other family is down (and the error names the family),
// while the soft rotation of `alternate` keeps a single stack target reachable on
// both turns.
func TestCheckTCPPinsTheFamily(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener address %T", listener.Addr())
	}

	tcpMonitor := func(family string) *models.Monitor {
		config := models.MonitorConfig{Host: "127.0.0.1", Port: address.Port, IPFamily: family}
		monitor := &models.Monitor{Name: "tcp", Type: models.MonitorTypeTCP, TimeoutSeconds: 3, Config: config}
		monitor.Config.Normalize(monitor.Type)
		return monitor
	}

	// The listener is IPv4 only, so pinning IPv4 reaches it and pinning IPv6 must
	// fail: tcp6 to a literal IPv4 address never leaves the host.
	if result := Check(context.Background(), tcpMonitor(models.IPFamily4), ""); result.Status != models.StatusUp {
		t.Fatalf("a pinned ipv4 probe must reach an IPv4 listener: %v %s", result.Status, result.Message)
	}
	result := Check(context.Background(), tcpMonitor(models.IPFamily6), "")
	if result.Status != models.StatusDown {
		t.Fatalf("a pinned ipv6 probe must not fall back to IPv4: %v", result.Status)
	}
	if !strings.Contains(result.Message, "tcp6") {
		t.Fatalf("the failure must name the pinned family, got %q", result.Message)
	}

	// The default rotation uses both turns, but never reports a target down for a
	// family it does not have.
	for _, turn := range []string{models.IPFamily4, models.IPFamily6} {
		if result := Check(context.Background(), tcpMonitor(models.IPFamilyAlternate), turn); result.Status != models.StatusUp {
			t.Fatalf("the %s turn must keep a single stack target up: %v %s", turn, result.Status, result.Message)
		}
	}

	// auto has no rotation either and keeps working.
	if result := Check(context.Background(), tcpMonitor(models.IPFamilyAuto), models.IPFamily6); result.Status != models.StatusUp {
		t.Fatalf("auto must keep working: %v %s", result.Status, result.Message)
	}
}
