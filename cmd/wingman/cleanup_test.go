package main

import (
	"errors"
	"testing"
)

func TestClosePeerSessionReturnsCleanupError(t *testing.T) {
	closeErr := errors.New("close failed")
	calls := 0
	err := closePeerSession(func() error {
		calls++
		return closeErr
	}, nil)
	if !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want cause %v", err, closeErr)
	}
	if calls != 1 {
		t.Fatalf("close calls = %d, want 1", calls)
	}
}

func TestClosePeerSessionPreservesChatError(t *testing.T) {
	chatErr := errors.New("chat failed")
	closeErr := errors.New("close failed")
	calls := 0
	err := closePeerSession(func() error {
		calls++
		return closeErr
	}, chatErr)
	if err != chatErr {
		t.Fatalf("error = %v, want original error %v", err, chatErr)
	}
	if calls != 1 {
		t.Fatalf("close calls = %d, want 1", calls)
	}
}

func TestClosePeerSessionPreservesResultAfterCleanupSuccess(t *testing.T) {
	chatErr := errors.New("chat failed")
	for _, result := range []error{nil, chatErr} {
		if err := closePeerSession(func() error { return nil }, result); err != result {
			t.Fatalf("error = %v, want %v", err, result)
		}
	}
}
