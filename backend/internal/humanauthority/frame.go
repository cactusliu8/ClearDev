package humanauthority

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const maxFrameBytes = 65536

func boundedFrameSize(n int) (uint32, error) {
	if n < 0 || n > maxFrameBytes {
		return 0, errors.New("human-authority frame exceeds 65536 bytes")
	}
	return uint32(n), nil //nolint:gosec // n is already bounded by maxFrameBytes.
}

func WriteFrame(w io.Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode human-authority frame: %w", err)
	}
	size, err := boundedFrameSize(len(body))
	if err != nil {
		return err
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], size)
	if _, err := w.Write(header[:]); err != nil {
		return fmt.Errorf("write human-authority frame header: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("write human-authority frame body: %w", err)
	}
	return nil
}

func ReadFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maxFrameBytes {
		return nil, fmt.Errorf("human-authority frame length %d is not allowed", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("read human-authority frame body: %w", err)
	}
	if !utf8.Valid(body) {
		return nil, errors.New("human-authority frame is not UTF-8")
	}
	return body, nil
}

func validateStrictObject(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read JSON: %w", err)
	}
	if first != json.Delim('{') {
		return errors.New("payload must be exactly one JSON object")
	}
	if err := consumeObject(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("payload must contain exactly one JSON object")
		}
		return err
	}
	return nil
}

func consumeObject(decoder *json.Decoder) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("JSON object key is not a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("JSON field %q is repeated", key)
		}
		seen[key] = struct{}{}
		if err := consumeValue(decoder); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if end != json.Delim('}') {
		return errors.New("unterminated JSON object")
	}
	return nil
}

func consumeArray(decoder *json.Decoder) error {
	for decoder.More() {
		if err := consumeValue(decoder); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if end != json.Delim(']') {
		return errors.New("unterminated JSON array")
	}
	return nil
}

func consumeValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("JSON cannot contain null")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return consumeObject(decoder)
	case '[':
		return consumeArray(decoder)
	default:
		return errors.New("unexpected JSON delimiter")
	}
}
