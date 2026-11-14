package bss

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"acs/internal/accounts"
)

// Every call to the operator API names the customer the context acts
// for, and none when there is none.
func TestACSClientNamesTheActingCustomer(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.Header.Get(ActingCustomerHeader))
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusAccepted)
		}
		json.NewEncoder(w).Encode(map[string]any{"command_key": "ck", "status": "QUEUED", "parameters": map[string]any{}, "names": []string{}})
	}))
	defer srv.Close()
	c := NewACSClient(srv.URL, 0, "service-token")
	const customer = "11111111-2222-3333-4444-555555555555"
	ctx := accounts.WithCustomer(context.Background(), customer)

	_, _ = c.SetParameters(ctx, "dev", nil)
	_, _ = c.GetDevice(ctx, "dev")
	_, _ = c.GetJobStatus(ctx, "ck")
	_, _ = c.GetParameters(ctx, "dev", []string{"a"})
	_, _ = c.GetParameterNames(ctx, "dev")
	_, _ = c.GetWiFiNetworks(ctx, "dev")
	if len(got) != 6 {
		t.Fatalf("calls seen = %v", got)
	}
	for _, g := range got {
		if g != "PUT "+customer && g != "GET "+customer {
			t.Errorf("call %q did not name the customer", g)
		}
	}

	got = nil
	_, _ = c.GetDevice(context.Background(), "dev")                            // nobody said
	_, _ = c.GetDevice(accounts.WithCustomer(context.Background(), ""), "dev") // an account nobody owns
	for _, g := range got {
		if g != "GET " {
			t.Errorf("call without a customer sent %q", g)
		}
	}
}
