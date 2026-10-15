package collab

import (
	"math"
	"testing"
)

func TestTransform(t *testing.T) {
	tests := []struct {
		name     string
		incoming Operation
		applied  Operation
		want     Operation
	}{
		{
			name:     "insert over insert shifts right",
			incoming: Operation{Kind: OperationInsert, Position: 4, Text: "Y"},
			applied:  Operation{Kind: OperationInsert, Position: 2, Text: "X"},
			want:     Operation{Kind: OperationInsert, Position: 5, Text: "Y"},
		},
		{
			name:     "same-position insert preserves server order",
			incoming: Operation{Kind: OperationInsert, Position: 2, Text: "Y"},
			applied:  Operation{Kind: OperationInsert, Position: 2, Text: "X"},
			want:     Operation{Kind: OperationInsert, Position: 3, Text: "Y"},
		},
		{
			name:     "insert over delete moves left",
			incoming: Operation{Kind: OperationInsert, Position: 6, Text: "X"},
			applied:  Operation{Kind: OperationDelete, Position: 2, Length: 3},
			want:     Operation{Kind: OperationInsert, Position: 3, Text: "X"},
		},
		{
			name:     "insert inside deleted range clamps to start",
			incoming: Operation{Kind: OperationInsert, Position: 4, Text: "X"},
			applied:  Operation{Kind: OperationDelete, Position: 2, Length: 3},
			want:     Operation{Kind: OperationInsert, Position: 2, Text: "X"},
		},
		{
			name:     "delete over earlier insert moves right",
			incoming: Operation{Kind: OperationDelete, Position: 4, Length: 2},
			applied:  Operation{Kind: OperationInsert, Position: 2, Text: "XX"},
			want:     Operation{Kind: OperationDelete, Position: 6, Length: 2},
		},
		{
			name:     "delete over internal insert expands",
			incoming: Operation{Kind: OperationDelete, Position: 2, Length: 4},
			applied:  Operation{Kind: OperationInsert, Position: 4, Text: "XX"},
			want:     Operation{Kind: OperationDelete, Position: 2, Length: 6},
		},
		{
			name:     "delete over disjoint earlier delete moves left",
			incoming: Operation{Kind: OperationDelete, Position: 6, Length: 2},
			applied:  Operation{Kind: OperationDelete, Position: 2, Length: 3},
			want:     Operation{Kind: OperationDelete, Position: 3, Length: 2},
		},
		{
			name:     "delete over overlapping delete removes overlap",
			incoming: Operation{Kind: OperationDelete, Position: 3, Length: 4},
			applied:  Operation{Kind: OperationDelete, Position: 1, Length: 3},
			want:     Operation{Kind: OperationDelete, Position: 1, Length: 3},
		},
		{
			name:     "fully overlapping delete becomes no-op",
			incoming: Operation{Kind: OperationDelete, Position: 2, Length: 2},
			applied:  Operation{Kind: OperationDelete, Position: 1, Length: 4},
			want:     Operation{Kind: OperationDelete, Position: 1, Length: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Transform(tt.incoming, tt.applied)
			if err != nil {
				t.Fatalf("Transform() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Transform() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestApplyUsesRuneOffsets(t *testing.T) {
	got, err := Apply("a界b", Operation{Kind: OperationInsert, Position: 2, Text: "好"})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got != "a界好b" {
		t.Fatalf("Apply() = %q, want %q", got, "a界好b")
	}
}

func TestTransformRejectsOverflowingDeleteRange(t *testing.T) {
	_, err := Transform(
		Operation{Kind: OperationDelete, Position: 1, Length: math.MaxInt},
		Operation{Kind: OperationInsert, Position: 0, Text: "x"},
	)
	if err == nil {
		t.Fatal("Transform() unexpectedly accepted an overflowing delete range")
	}
}
