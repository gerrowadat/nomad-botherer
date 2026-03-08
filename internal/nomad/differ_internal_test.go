package nomad

import (
	"errors"
	"testing"
)

func TestIsNotFound_Nil(t *testing.T) {
	if isNotFound(nil) {
		t.Error("nil error should not be considered not-found")
	}
}

func TestIsNotFound_404InMessage(t *testing.T) {
	if !isNotFound(errors.New("Unexpected response code: 404")) {
		t.Error("error containing '404' should be considered not-found")
	}
}

func TestIsNotFound_NotFoundText(t *testing.T) {
	if !isNotFound(errors.New("job not found")) {
		t.Error("error containing 'not found' should be considered not-found")
	}
}

func TestIsNotFound_NotFoundTextCaseInsensitive(t *testing.T) {
	if !isNotFound(errors.New("Job Not Found")) {
		t.Error("'not found' check should be case-insensitive")
	}
}

func TestIsNotFound_OtherError(t *testing.T) {
	if isNotFound(errors.New("500 internal server error")) {
		t.Error("non-404/not-found error should not be considered not-found")
	}
}
