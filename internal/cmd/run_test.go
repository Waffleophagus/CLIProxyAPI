package cmd

import (
	"strings"
	"testing"
)

func TestStartServiceReturnsBuildError(t *testing.T) {
	if errStart := StartService(nil, ""); errStart == nil || !strings.Contains(errStart.Error(), "configuration is required") {
		t.Fatalf("expected missing configuration error, got %v", errStart)
	}
}
