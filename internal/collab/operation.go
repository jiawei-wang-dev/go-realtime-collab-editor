package collab

import "fmt"

type OperationKind string

const (
	OperationInsert OperationKind = "insert"
	OperationDelete OperationKind = "delete"
)

type Document struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Version uint64 `json:"version"`
}

type Operation struct {
	ID          string        `json:"id,omitempty"`
	UserID      string        `json:"user_id,omitempty"`
	BaseVersion uint64        `json:"base_version,omitempty"`
	Kind        OperationKind `json:"kind"`
	Position    int           `json:"position"`
	Text        string        `json:"text,omitempty"`
	Length      int           `json:"length,omitempty"`
}

type AppliedOperation struct {
	Operation
	Version uint64 `json:"version"`
}

func (op Operation) Validate(content string) error {
	runes := []rune(content)
	if op.Position < 0 || op.Position > len(runes) {
		return fmt.Errorf("position %d is outside document", op.Position)
	}

	switch op.Kind {
	case OperationInsert:
		if op.Text == "" {
			return fmt.Errorf("insert text must not be empty")
		}
		if op.Length != 0 {
			return fmt.Errorf("insert length must be zero")
		}
	case OperationDelete:
		if op.Text != "" {
			return fmt.Errorf("delete text must be empty")
		}
		if op.Length < 0 || op.Length > len(runes)-op.Position {
			return fmt.Errorf("delete range is outside document")
		}
	default:
		return fmt.Errorf("unsupported operation kind %q", op.Kind)
	}

	return nil
}

func Apply(content string, op Operation) (string, error) {
	if err := op.Validate(content); err != nil {
		return "", err
	}

	runes := []rune(content)
	switch op.Kind {
	case OperationInsert:
		inserted := []rune(op.Text)
		result := make([]rune, 0, len(runes)+len(inserted))
		result = append(result, runes[:op.Position]...)
		result = append(result, inserted...)
		result = append(result, runes[op.Position:]...)
		return string(result), nil
	case OperationDelete:
		result := make([]rune, 0, len(runes)-op.Length)
		result = append(result, runes[:op.Position]...)
		result = append(result, runes[op.Position+op.Length:]...)
		return string(result), nil
	default:
		return "", fmt.Errorf("unsupported operation kind %q", op.Kind)
	}
}
