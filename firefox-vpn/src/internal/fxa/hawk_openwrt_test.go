package fxa

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"regexp"
	"testing"
)

// Verify as the server does, rather than accepting the client's own output.
// Hex and Base64 both decode as strings: checking just header presence missed
// this bug in the original client and broke every authenticated sign-in step.
func TestOpenWrtHawkServerVerification(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	for _, payload := range []string{`{"code":"123456"}`, ""} {
		header, err := hawkHeader("post", "https://api.accounts.firefox.com/v1/session/verify_code?x=1", "test-id", key, payload)
		if err != nil {
			t.Fatal(err)
		}
		fields := map[string]string{}
		for _, pair := range regexp.MustCompile(`(\w+)="([^"]*)"`).FindAllStringSubmatch(header, -1) {
			fields[pair[1]] = pair[2]
		}
		if payload != "" {
			digest := sha256.Sum256([]byte("hawk.1.payload\napplication/json\n" + payload + "\n"))
			if fields["hash"] != base64.StdEncoding.EncodeToString(digest[:]) {
				t.Fatalf("payload hash is not Hawk Base64: %q", fields["hash"])
			}
		} else if fields["hash"] != "" {
			t.Fatal("empty body must omit hash")
		}
		// Hawk normalized request ends in an empty ext field and a newline.
		normalized := fmt.Sprintf("hawk.1.header\n%s\n%s\nPOST\n/v1/session/verify_code?x=1\napi.accounts.firefox.com\n443\n%s\n\n", fields["ts"], fields["nonce"], fields["hash"])
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(normalized))
		got, err := base64.StdEncoding.DecodeString(fields["mac"])
		if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
			t.Fatal("server rejected Hawk MAC")
		}
	}
}
