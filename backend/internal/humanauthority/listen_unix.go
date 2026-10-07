//go:build !windows

package humanauthority

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"
)

func Listen(endpoint string) (net.Listener, error) {
	_ = os.Remove(endpoint)
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		return nil, fmt.Errorf("listen for human-authority channel: %w", err)
	}
	return listener, nil
}

func Dial(ctx context.Context, endpoint string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", endpoint)
	if err != nil {
		return nil, err
	}
	return conn, nil
}
