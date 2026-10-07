//go:build windows

package humanauthority

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

func Listen(endpoint string) (net.Listener, error) {
	listener, err := winio.ListenPipe(endpoint, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;OW)",
	})
	if err != nil {
		return nil, fmt.Errorf("listen for human-authority channel: %w", err)
	}
	return listener, nil
}

func Dial(ctx context.Context, endpoint string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := winio.DialPipeContext(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return conn, nil
}
