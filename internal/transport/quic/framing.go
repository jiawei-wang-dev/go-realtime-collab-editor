package quictransport

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
)

var (
	ErrInvalidFrameLength = errors.New("quic transport: invalid frame length")
	ErrMessageTooLarge    = errors.New("quic transport: message too large")
	ErrBadMessage         = errors.New("quic transport: bad message")
)

func readFrame(reader io.Reader, maxMessageSize uint32) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 {
		return nil, ErrInvalidFrameLength
	}
	if length > maxMessageSize {
		return nil, ErrMessageTooLarge
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func writeFrame(writer io.Writer, payload []byte, maxMessageSize uint32) error {
	if len(payload) == 0 {
		return ErrInvalidFrameLength
	}
	if uint64(len(payload)) > uint64(maxMessageSize) {
		return ErrMessageTooLarge
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeAll(writer, header[:]); err != nil {
		return err
	}
	return writeAll(writer, payload)
}

func readMessage(reader io.Reader, maxMessageSize uint32) (protocol.Message, error) {
	payload, err := readFrame(reader, maxMessageSize)
	if err != nil {
		return protocol.Message{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var message protocol.Message
	if err := decoder.Decode(&message); err != nil {
		return protocol.Message{}, fmt.Errorf("%w: decode message: %v", ErrBadMessage, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return protocol.Message{}, fmt.Errorf("%w: message must contain exactly one JSON value", ErrBadMessage)
	}
	return message, nil
}

func writeMessage(writer io.Writer, message protocol.Message, maxMessageSize uint32) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	return writeFrame(writer, payload, maxMessageSize)
}

func writeAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return nil
}
