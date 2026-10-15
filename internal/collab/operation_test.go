package collab

import (
	"math"
	"testing"
)

func TestApplyRejectsInvalidBounds(t *testing.T) {
	tests := []struct {
		name      string
		operation Operation
	}{
		{
			name:      "negative insert position",
			operation: Operation{Kind: OperationInsert, Position: -1, Text: "x"},
		},
		{
			name:      "insert position after document",
			operation: Operation{Kind: OperationInsert, Position: 4, Text: "x"},
		},
		{
			name:      "negative delete position",
			operation: Operation{Kind: OperationDelete, Position: -1, Length: 1},
		},
		{
			name:      "negative delete length",
			operation: Operation{Kind: OperationDelete, Position: 0, Length: -1},
		},
		{
			name:      "delete range after document",
			operation: Operation{Kind: OperationDelete, Position: 2, Length: 2},
		},
		{
			name:      "delete range integer overflow",
			operation: Operation{Kind: OperationDelete, Position: 1, Length: math.MaxInt},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Apply("abc", test.operation); err == nil {
				t.Fatalf("Apply(%#v) unexpectedly succeeded", test.operation)
			}
		})
	}
}

func TestApplyDeleteUsesRuneOffsets(t *testing.T) {
	got, err := Apply("a界好b", Operation{Kind: OperationDelete, Position: 1, Length: 2})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got != "ab" {
		t.Fatalf("Apply() = %q, want %q", got, "ab")
	}
}
