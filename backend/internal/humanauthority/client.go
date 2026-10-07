package humanauthority

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

const (
	offerPollInterval = 400 * time.Millisecond
	dialRetryInterval = 500 * time.Millisecond
)

// Expiration closes the old connection and cancels its desktop window.
// The client reconnects; the service requires an explicit reopen for this item.
var errOfferExpiredUnattended = errors.New("human decision offer expired unattended")

// DecisionHandler is implemented by the ClearDev service. The transport does
// not interpret decision kinds.
type DecisionHandler interface {
	BackfillHumanDecisionRequests(context.Context) error
	IssueHumanDecisionOffer(context.Context, string) (core.HumanDecisionOffer, bool, error)
	ApplyHumanDecisionResult(context.Context, core.HumanDecisionResult) error
	DismissHumanDecisionDispatch(context.Context, core.DismissHumanDecisionDispatchCommand) error
	DismissOpenHumanDecisionDispatches(context.Context, string, core.HumanDecisionDispatchOutcome, time.Time) error
}

// Client is the daemon side of the private desktop channel.
type Client struct {
	endpoint     string
	token        string
	desktopRunID string
	handler      DecisionHandler
	registry     *core.DecisionRegistry
	now          func() time.Time
	logger       *slog.Logger
	state        *ConnectionState
}

// NewClient constructs the private daemon client with an optional shared transport observation.
func NewClient(endpoint, token, desktopRunID string, handler DecisionHandler, logger *slog.Logger, states ...*ConnectionState) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	var state *ConnectionState
	if len(states) > 0 {
		state = states[0]
	}
	if state == nil {
		state = &ConnectionState{}
	}
	return &Client{
		state:    state,
		endpoint: endpoint, token: token, desktopRunID: desktopRunID,
		handler: handler, registry: core.ProductionDecisionRegistry(),
		now: func() time.Time { return time.Now().UTC() }, logger: logger,
	}
}

func (c *Client) Run(ctx context.Context) {
	if c == nil || c.handler == nil || !c.enabled() {
		return
	}
	for ctx.Err() == nil {
		if err := c.handler.BackfillHumanDecisionRequests(ctx); err != nil && ctx.Err() == nil {
			c.logger.Warn("human authority: backfill failed")
		}
		conn, err := Dial(ctx, c.endpoint)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(dialRetryInterval):
				continue
			}
		}
		if err := c.serve(ctx, conn); err != nil && ctx.Err() == nil {
			if errors.Is(err, errOfferExpiredUnattended) {
				c.logger.Info("human authority: decision offer expired; waiting for an explicit reopen")
				continue
			}
			c.logger.Warn("human authority: desktop channel closed")
		}
	}
}

func (c *Client) enabled() bool {
	return c.endpoint != "" && c.token != "" && c.desktopRunID != ""
}

func (c *Client) serve(ctx context.Context, conn net.Conn) error {
	defer func() { c.state.connected.Store(false); _ = conn.Close() }()
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	if err := PerformClientHandshake(conn, c.token, c.desktopRunID); err != nil {
		return err
	}
	if err := c.handler.DismissOpenHumanDecisionDispatches(ctx, c.desktopRunID, core.HumanDecisionDispatchDisconnected, c.now()); err != nil {
		return err
	}
	c.state.connected.Store(true)
	c.logger.Info("human authority: desktop connected")
	return c.loop(ctx, conn)
}

func (c *Client) loop(ctx context.Context, conn net.Conn) error {
	var inFlight *core.HumanDecisionOffer
	var inFlightDeadline time.Time
	defer func() {
		if inFlight == nil {
			return
		}
		outcome := core.HumanDecisionDispatchDisconnected
		if !c.now().Before(inFlightDeadline) {
			outcome = core.HumanDecisionDispatchExpired
		}
		err := c.handler.DismissHumanDecisionDispatch(context.WithoutCancel(ctx), core.DismissHumanDecisionDispatchCommand{
			Nonce: inFlight.Nonce, DesktopRunID: c.desktopRunID,
			Outcome: outcome, At: c.now(),
		})
		if err != nil {
			c.logger.Warn("human authority: disconnect receipt failed", "error", err)
		}
	}()
	for ctx.Err() == nil {
		if inFlight != nil && !c.now().Before(inFlightDeadline) {
			if err := c.handler.DismissHumanDecisionDispatch(context.WithoutCancel(ctx), core.DismissHumanDecisionDispatchCommand{Nonce: inFlight.Nonce, DesktopRunID: c.desktopRunID, Outcome: core.HumanDecisionDispatchExpired, At: c.now()}); err != nil {
				return err
			}
			inFlight = nil
			return errOfferExpiredUnattended
		}
		if inFlight == nil {
			offer, ok, err := c.handler.IssueHumanDecisionOffer(ctx, c.desktopRunID)
			if err != nil {
				return err
			}
			if ok {
				deadline, err := time.Parse(time.RFC3339, offer.ExpiresAt)
				if err != nil {
					return err
				}
				copied := offer
				inFlight = &copied
				inFlightDeadline = deadline
				if err := WriteFrame(conn, offer); err != nil {
					return err
				}
			}
		}
		_ = conn.SetReadDeadline(time.Now().Add(offerPollInterval))
		raw, err := ReadFrame(conn)
		if err != nil {
			if isTimeout(err) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				continue
			}
			return err
		}
		result, err := core.ParseHumanDecisionResult(raw, c.registry)
		if err != nil {
			c.logger.Warn("human authority: desktop result was invalid")
			continue
		}
		if err := c.handler.ApplyHumanDecisionResult(ctx, result); err != nil {
			c.logger.Warn("human authority: desktop result was not applied", "error", err)
			return err
		}
		if inFlight != nil && result.Nonce == inFlight.Nonce {
			inFlight = nil
		}
	}
	return ctx.Err()
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// ServeFakeDesktop is a test helper: listen, handshake as Electron, then invoke
// decide for each offer until the context ends.
func ServeFakeDesktop(ctx context.Context, listener net.Listener, token, desktopRunID string, decide func(core.HumanDecisionOffer) core.HumanDecisionChoice) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := PerformServerHandshake(conn, token, desktopRunID); err != nil {
		return err
	}
	registry := core.ProductionDecisionRegistry()
	for ctx.Err() == nil {
		raw, err := ReadFrame(conn)
		if err != nil {
			return err
		}
		offer, err := core.ParseHumanDecisionOffer(raw, registry)
		if err != nil {
			return fmt.Errorf("fake desktop offer: %w", err)
		}
		choice := decide(offer)
		result := core.HumanDecisionResult{
			ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind,
			DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind,
			BindingSchemaVersion: offer.BindingSchemaVersion, Binding: offer.Binding,
			ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: choice,
		}
		if err := WriteFrame(conn, result); err != nil {
			return err
		}
	}
	return ctx.Err()
}
