package humanauthority

import "sync/atomic"

// ConnectionState observes the private transport only. It grants no authority.
type ConnectionState struct{ connected atomic.Bool }

// Connected reports whether handshake and prior-dispatch cleanup succeeded.
func (s *ConnectionState) Connected() bool { return s != nil && s.connected.Load() }
