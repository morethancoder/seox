package apperr

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestFromMapsContextToTimeout(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, context.Canceled, fmt.Errorf("get: %w", context.Canceled)} {
		if e := From(err); e.Code != Timeout || ExitCode(e.Code) != 7 {
			t.Errorf("%v -> %s", err, e.Code)
		}
	}
	if e := From(errors.New("x")); e.Code != Internal {
		t.Error("plain error should be internal")
	}
	if ExitCode(NotImplemented) != 1 || ExitCode(InvalidInput) != 2 || ExitCode(LoginRequired) != 4 {
		t.Error("exit codes changed")
	}
}
