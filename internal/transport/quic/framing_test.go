package quictransport

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
)

func TestFramingRoundTripAndConsecutiveMessages(t *testing.T) {
	var buffer bytes.Buffer
	messages := []protocol.Message{{Type: protocol.TypeAuthOK}, {Type: protocol.TypeJoin, DocID: "doc"}}
	for _, message := range messages {
		if err := writeMessage(&buffer, message, 1024); err != nil {
			t.Fatal(err)
		}
	}
	for _, expected := range messages {
		actual, err := readMessage(&buffer, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if actual.Type != expected.Type || actual.DocID != expected.DocID {
			t.Fatalf("message = %#v, want %#v", actual, expected)
		}
	}
}

func TestFramingHandlesPartialReadsAndWrites(t *testing.T) {
	var destination bytes.Buffer
	writer := &limitedWriter{writer: &destination, limit: 2}
	payload := []byte(`{"type":"auth_ok"}`)
	if err := writeFrame(writer, payload, 1024); err != nil {
		t.Fatal(err)
	}
	reader := &limitedReader{reader: bytes.NewReader(destination.Bytes()), limit: 1}
	actual, err := readFrame(reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, payload) {
		t.Fatalf("payload = %q, want %q", actual, payload)
	}
}

func TestFramingRejectsInvalidAndOversizedLengths(t *testing.T) {
	var zero [4]byte
	if _, err := readFrame(bytes.NewReader(zero[:]), 10); !errors.Is(err, ErrInvalidFrameLength) {
		t.Fatalf("zero length error = %v", err)
	}
	var oversized [4]byte
	binary.BigEndian.PutUint32(oversized[:], 11)
	if _, err := readFrame(bytes.NewReader(oversized[:]), 10); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("oversized error = %v", err)
	}
	if err := writeFrame(io.Discard, make([]byte, 11), 10); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("oversized write error = %v", err)
	}
}

func TestFramingEOF(t *testing.T) {
	if _, err := readFrame(bytes.NewReader([]byte{0, 0}), 10); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("header EOF error = %v", err)
	}
	frame := []byte{0, 0, 0, 3, 'a'}
	if _, err := readFrame(bytes.NewReader(frame), 10); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("payload EOF error = %v", err)
	}
}

func TestConcurrentFramedWritesRemainWholeWhenSerialized(t *testing.T) {
	var destination lockedBuffer
	const count = 100
	var wait sync.WaitGroup
	for index := range count {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			destination.messageMu.Lock()
			defer destination.messageMu.Unlock()
			if err := writeMessage(&destination, protocol.Message{Type: protocol.TypeError, Message: string(rune('a' + index%26))}, 1024); err != nil {
				t.Errorf("write message: %v", err)
			}
		}(index)
	}
	wait.Wait()
	for range count {
		if _, err := readMessage(bytes.NewReader(destination.nextFrame()), 1024); err != nil {
			t.Fatal(err)
		}
	}
}

type limitedWriter struct {
	writer io.Writer
	limit  int
}

func (w *limitedWriter) Write(payload []byte) (int, error) {
	if len(payload) > w.limit {
		payload = payload[:w.limit]
	}
	return w.writer.Write(payload)
}

type limitedReader struct {
	reader io.Reader
	limit  int
}

func (r *limitedReader) Read(payload []byte) (int, error) {
	if len(payload) > r.limit {
		payload = payload[:r.limit]
	}
	return r.reader.Read(payload)
}

type lockedBuffer struct {
	bytes.Buffer
	messageMu sync.Mutex
}

func (b *lockedBuffer) nextFrame() []byte {
	if b.Len() < 4 {
		return nil
	}
	header := b.Next(4)
	length := binary.BigEndian.Uint32(header)
	return append(header, b.Next(int(length))...)
}
