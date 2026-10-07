package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// LocalCheckEvidence describes only stdio initialization, never a model run.
type LocalCheckEvidence struct {
	Frames      []LocalCheckFrame `json:"frames"`
	Initialize  int               `json:"initialize"`
	Initialized int               `json:"initialized"`
	ResponseAt  int64             `json:"responseAt"`
	Error       string            `json:"error,omitempty"`
}

// LocalCheckFrame retains the exact direction and raw protocol frame.
type LocalCheckFrame struct {
	Direction string `json:"direction"`
	Raw       string `json:"raw"`
}

// CheckLocalHandshake shares framing, response IDs and initialize parameters
// with the product driver. It exposes no general RPC or business operation.
// The caller must close the transport on context cancellation.
func CheckLocalHandshake(ctx context.Context, input io.Reader, output io.Writer) (LocalCheckEvidence, error) {
	e := LocalCheckEvidence{Frames: []LocalCheckFrame{}}
	if err := ctx.Err(); err != nil {
		return e, err
	}
	send := func(value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if _, err = output.Write(append(raw, '\n')); err != nil {
			return err
		}
		e.Frames = append(e.Frames, LocalCheckFrame{"sent", string(raw)})
		return nil
	}
	if err := send(map[string]any{"id": 1, "method": "initialize", "params": initializeParams()}); err != nil {
		return e, err
	}
	e.Initialize = 1
	reader := bufio.NewReaderSize(io.LimitReader(input, 8<<20), 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return e, err
		}
		raw, err := readFrameBounded(reader, 1<<20)
		if err != nil {
			return e, fmt.Errorf("initialize read: %w", err)
		}
		e.Frames = append(e.Frames, LocalCheckFrame{"received", string(raw)})
		var f frame
		if err := json.Unmarshal(raw, &f); err != nil {
			return e, err
		}
		if f.Method != "" {
			if f.ID != nil {
				return e, errors.New("server request forbidden in local check")
			}
			continue
		}
		var id int64
		if err = responseID(f, &id); err != nil || id != 1 {
			return e, errors.New("initialize response ID mismatch")
		}
		if f.Error != nil {
			return e, f.Error
		}
		var object map[string]json.RawMessage
		if err = json.Unmarshal(f.Result, &object); err != nil || object == nil {
			return e, errors.New("initialize response must be a non-error object")
		}
		e.ResponseAt = time.Now().UnixMilli()
		if err := send(map[string]any{"method": "initialized"}); err != nil {
			return e, err
		}
		e.Initialized = 1
		return e, nil
	}
}
