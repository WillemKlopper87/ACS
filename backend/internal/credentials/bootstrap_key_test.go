package credentials

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBootstrapCommandKeyFitsTR069ParameterKey(t *testing.T) {
	for i := 0; i < 50; i++ {
		key := bootstrapCommandKey(uuid.New().String())
		if len(key) != 32 {
			t.Fatalf("bootstrapCommandKey length = %d (%q), want exactly 32", len(key), key)
		}
		if !strings.HasPrefix(key, "bootstrap-") || strings.Contains(key[len("bootstrap-"):], "-") {
			t.Fatalf("bootstrapCommandKey = %q, want bootstrap-<22 hex>", key)
		}
	}
}
