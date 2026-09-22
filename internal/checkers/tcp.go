package checkers

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// checkTCP opens a TCP connection (optionally writing a payload and expecting a
// response) and reports the time it took to complete the handshake.
func checkTCP(ctx context.Context, monitor *models.Monitor) Result {
	cfg := monitor.Config
	address := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	dialer := &net.Dialer{Timeout: timeoutFromContext(ctx)}

	started := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", address)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return Result{
			Status:    models.StatusDown,
			LatencyMS: latency,
			Message:   "tcp connect failed: " + describeRequestError(err),
		}
	}
	defer func() { _ = conn.Close() }()

	if strings.TrimSpace(cfg.Send) != "" {
		if err := conn.SetWriteDeadline(time.Now().Add(timeoutFromContext(ctx))); err != nil {
			return Result{Status: models.StatusDown, LatencyMS: latency, Message: "could not set the write deadline"}
		}
		if _, err := conn.Write([]byte(cfg.Send)); err != nil {
			return Result{
				Status:    models.StatusDown,
				LatencyMS: latency,
				Message:   "tcp write failed: " + err.Error(),
			}
		}
	}

	if strings.TrimSpace(cfg.Expect) != "" {
		if err := conn.SetReadDeadline(time.Now().Add(timeoutFromContext(ctx))); err != nil {
			return Result{Status: models.StatusDown, LatencyMS: latency, Message: "could not set the read deadline"}
		}
		buffer := make([]byte, 4096)
		read, err := conn.Read(buffer)
		if err != nil {
			return Result{
				Status:    models.StatusDown,
				LatencyMS: latency,
				Message:   "tcp read failed: " + err.Error(),
			}
		}
		received := strings.TrimSpace(string(buffer[:read]))
		if !strings.Contains(received, cfg.Expect) {
			return Result{
				Status:    models.StatusDown,
				LatencyMS: latency,
				Message:   fmt.Sprintf("expected %q, received %q", cfg.Expect, received),
			}
		}
		return Result{
			Status:    models.StatusUp,
			LatencyMS: latency,
			Message:   fmt.Sprintf("connected and matched %q", cfg.Expect),
		}
	}

	return Result{
		Status:    models.StatusUp,
		LatencyMS: latency,
		Message:   "tcp connection established",
	}
}
