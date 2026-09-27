package checkers

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// liveTarget is a real dual stack endpoint used by the live test below:
// Cloudflare's resolver hostname answers on 1.1.1.1/1.0.0.1 and on
// 2606:4700:4700::1111/::1001, with port 443 open on both.
const liveTarget = "one.one.one.one:443"

// TestLiveIPFamilyDialPlan is the live smoke test of the rotation: it dials a
// real dual stack host once per family and checks the address the connection
// really landed on, which is the only thing a unit test cannot prove (the plan
// only decides the network and the address handed to net.Dialer).
//
//	UP_LIVE_NET=1 go test ./internal/checkers/ -run TestLiveIPFamilyDialPlan -v
//
// It is opt-in because it needs the public internet; `go test ./...` never dials
// anything. A family this network does not have is reported, not failed - the
// soft rotation exists precisely because such a network is normal.
func TestLiveIPFamilyDialPlan(t *testing.T) {
	if os.Getenv("UP_LIVE_NET") != "1" {
		t.Skip("set UP_LIVE_NET=1 to dial the real internet")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	host, _, err := net.SplitHostPort(liveTarget)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", liveTarget, err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		t.Skipf("%s does not resolve here: %v", host, err)
	}

	reached := 0
	for _, family := range []string{models.IPFamily4, models.IPFamily6} {
		available := false
		for _, entry := range ips {
			if literalFamily(entry.IP) == family {
				available = true
				break
			}
		}
		if !available {
			t.Logf("this network has no %s address for %s", family, host)
			continue
		}

		conn, err := dialProbe(ctx, probePlan{family: family}, liveTarget, 5*time.Second)
		if err != nil {
			t.Errorf("dialing %s over %s: %v", liveTarget, family, err)
			continue
		}
		remote, ok := conn.RemoteAddr().(*net.TCPAddr)
		if !ok {
			t.Fatalf("unexpected remote address %T", conn.RemoteAddr())
		}
		_ = conn.Close()
		if got := literalFamily(remote.IP); got != family {
			t.Errorf("the %s turn connected to %s (%s)", family, remote, got)
			continue
		}
		t.Logf("%s reached %s", family, remote)
		reached++
	}
	if reached == 0 {
		t.Skip("neither family is reachable from this network")
	}
}
