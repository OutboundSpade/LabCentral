package application

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestEphemeralSecretRedactsFormattingAndJSON(t *testing.T) {
	t.Parallel()
	secret := NewEphemeralSecret([]byte("correct horse battery staple"))
	for _, rendered := range []string{
		fmt.Sprint(secret),
		fmt.Sprintf("%v", secret),
		fmt.Sprintf("%+v", secret),
		fmt.Sprintf("%#v", secret),
	} {
		if strings.Contains(rendered, "correct horse") || rendered != "[REDACTED]" {
			t.Fatalf("secret formatting was not redacted: %q", rendered)
		}
	}
	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatalf("marshal secret: %v", err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("secret serialized data: %s", encoded)
	}
	copyOfSecret := secret.Bytes()
	copyOfSecret[0] = 'X'
	if secret.Bytes()[0] == 'X' {
		t.Fatal("secret bytes escaped without copying")
	}
	secret.Destroy()
	if secret.Bytes() != nil {
		t.Fatal("destroy did not clear secret")
	}
}
