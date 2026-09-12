package subscription

import (
	"bytes"
	"strings"
	"testing"
)

func TestSubscriptionRejectsOversizedInputBeforeNormalization(t *testing.T) {
	_, err := ParseGeneralSubscription(bytes.Repeat([]byte(" "), MaxSubscriptionBytes+1))
	if err == nil || !strings.Contains(err.Error(), "input exceeds") {
		t.Fatalf("expected input size rejection, got %v", err)
	}
}
