package collab

import (
	"fmt"
	"math"
)

// Transform rebases an incoming operation over an operation that has already
// been applied. Positions and lengths are Unicode code-point offsets.
func Transform(incoming, applied Operation) (Operation, error) {
	if err := validateTransformOperand(incoming); err != nil {
		return Operation{}, fmt.Errorf("invalid incoming operation: %w", err)
	}
	if err := validateTransformOperand(applied); err != nil {
		return Operation{}, fmt.Errorf("invalid applied operation: %w", err)
	}

	transformed := incoming

	switch {
	case incoming.Kind == OperationInsert && applied.Kind == OperationInsert:
		if applied.Position <= transformed.Position {
			transformed.Position += len([]rune(applied.Text))
		}
	case incoming.Kind == OperationInsert && applied.Kind == OperationDelete:
		transformed.Position = transformPositionOverDelete(
			transformed.Position,
			applied.Position,
			applied.Length,
		)
	case incoming.Kind == OperationDelete && applied.Kind == OperationInsert:
		insertLength := len([]rune(applied.Text))
		deleteEnd := transformed.Position + transformed.Length
		if applied.Position <= transformed.Position {
			transformed.Position += insertLength
		} else if applied.Position < deleteEnd {
			transformed.Length += insertLength
		}
	case incoming.Kind == OperationDelete && applied.Kind == OperationDelete:
		start := transformPositionOverDelete(
			transformed.Position,
			applied.Position,
			applied.Length,
		)
		end := transformPositionOverDelete(
			transformed.Position+transformed.Length,
			applied.Position,
			applied.Length,
		)
		transformed.Position = start
		transformed.Length = max(0, end-start)
	default:
		return Operation{}, fmt.Errorf("unsupported transform %q over %q", incoming.Kind, applied.Kind)
	}

	return transformed, nil
}

func validateTransformOperand(operation Operation) error {
	if operation.Position < 0 {
		return fmt.Errorf("position must not be negative")
	}
	switch operation.Kind {
	case OperationInsert:
		if operation.Text == "" {
			return fmt.Errorf("insert text must not be empty")
		}
	case OperationDelete:
		if operation.Length < 0 || operation.Length > math.MaxInt-operation.Position {
			return fmt.Errorf("delete range overflows")
		}
	default:
		return fmt.Errorf("unsupported operation kind %q", operation.Kind)
	}
	return nil
}

func transformPositionOverDelete(position, deletePosition, deleteLength int) int {
	deleteEnd := deletePosition + deleteLength
	if position <= deletePosition {
		return position
	}
	if position >= deleteEnd {
		return position - deleteLength
	}
	return deletePosition
}
