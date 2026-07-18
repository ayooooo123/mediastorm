package debrid

import (
	"context"
	"fmt"
	"testing"
)

func TestIsProviderUnavailableErrorIgnoresCanceledSourceRequest(t *testing.T) {
	err := fmt.Errorf("stream aborted: %w", &SourceError{
		Provider: "test",
		Err:      context.Canceled,
	})
	if IsProviderUnavailableError(err) {
		t.Fatalf("IsProviderUnavailableError(%v) = true, want false", err)
	}
}

func TestIsProviderUnavailableErrorIncludesSourceDeadline(t *testing.T) {
	err := &SourceError{Provider: "test", Err: context.DeadlineExceeded}
	if !IsProviderUnavailableError(err) {
		t.Fatalf("IsProviderUnavailableError(%v) = false, want true", err)
	}
}
