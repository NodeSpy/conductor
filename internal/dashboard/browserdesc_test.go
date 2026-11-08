package dashboard

import (
	"encoding/json"
	"strings"
	"testing"
)

// A widening wallet change's step-up description never carries a typed value.
func TestDescribeWalletOpHidesTypedValues(t *testing.T) {
	req := json.RawMessage(`{"name":"site","values":{"password":"hunter2-typed","username":"ada"},"refs":{"otp":"{{ vault \"house\" \"site/otp\" }}"}}`)
	d := describeWalletOp("browser-login-add", req)
	for _, secret := range []string{"hunter2-typed", "ada\""} {
		if strings.Contains(d, secret) {
			t.Fatalf("description shows a typed value %q:\n%s", secret, d)
		}
	}
	for _, want := range []string{"password", "username", "(typed, stored write-only)", "site/otp", "browser-login-add"} {
		if !strings.Contains(d, want) {
			t.Fatalf("description lacks %q:\n%s", want, d)
		}
	}
}
