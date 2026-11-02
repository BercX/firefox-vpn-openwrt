package fxa

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"firefox-vpn/internal/controlplane"
)

func TestRefreshFastlyChallenge(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		name := "accepted"
		if blocked {
			name = "still blocked"
		}
		t.Run(name, func(t *testing.T) {
			previous := controlplane.Transport()
			t.Cleanup(func() {
				controlplane.SetTransport(previous)
				for _, target := range []string{fxaAuthServer + "/", fxaOAuthServer + "/", fxaSiteURL} {
					u, _ := url.Parse(target)
					controlplane.SetCookies(u, []*http.Cookie{
						{Name: "fastly-test", Path: "/", MaxAge: -1},
						{Name: "fastly-test", Path: "/v1", MaxAge: -1},
					})
				}
			})
			requests, solves := 0, 0
			controlplane.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if !strings.Contains(r.UserAgent(), "Firefox/") || r.UserAgent() != fastlySolverUserAgent {
					t.Fatalf("API and solver must use the same Firefox identity: %q", r.UserAgent())
				}
				status, body := http.StatusOK, ""
				header := make(http.Header)
				cookie, _ := r.Cookie("fastly-test")
				switch r.URL.Path {
				case "/v1/oauth/token":
					requests++
					if r.URL.Host != "oauth.accounts.firefox.com" || r.Method != "POST" || r.Header.Get("Accept") != "*/*" {
						t.Fatalf("wrong refresh endpoint or headers: %s %s %v", r.Method, r.URL, r.Header)
					}
					var grant map[string]string
					if err := json.NewDecoder(r.Body).Decode(&grant); err != nil || grant["grant_type"] != "refresh_token" || grant["refresh_token"] != "saved-refresh" {
						t.Fatalf("refresh body was lost on retry: %v %v", grant, err)
					}
					if cookie == nil || blocked {
						status = http.StatusNotAcceptable
					} else {
						body = `{"access_token":"renewed","expires_in":3600}`
					}
				case "/":
					if cookie == nil {
						body = `<title>Client Challenge</title><script src="/_fs-ch-test/script.js"></script>`
					}
				case "/_fs-ch-test/script.js":
					body = `init([{"ty":"pat","data":{}}], "challenge-token", "/_fs-ch-test", true);`
				case "/_fs-ch-test/pat":
					status = http.StatusUnauthorized
				case "/_fs-ch-test/fst-post-back":
					solves++
					header.Set("Set-Cookie", "fastly-test=solved; Path=/; Secure; HttpOnly")
					body = `{"status":"success"}`
				default:
					t.Fatalf("unexpected request: %s", r.URL)
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			}))

			token, err := RefreshToken(context.Background(), "saved-refresh")
			if blocked {
				if err == nil || !strings.Contains(err.Error(), "HTTP 406") || requests != maxChallengeAttempts || solves != maxChallengeAttempts-1 {
					t.Fatalf("unbounded or unused challenge retries: requests=%d solves=%d err=%v", requests, solves, err)
				}
			} else if err != nil || token.AccessToken != "renewed" || token.RefreshToken != "saved-refresh" || requests != 2 || solves != 1 {
				t.Fatalf("refresh did not recover: token=%+v requests=%d solves=%d err=%v", token, requests, solves, err)
			}
		})
	}
}

func TestHawkGrantKeepsAuthHost(t *testing.T) {
	useFxaFixture(t, func(r *http.Request, body map[string]any) (int, string) {
		if r.URL.String() != fxaAuthServer+"/oauth/token" || body["grant_type"] != "fxa-credentials" || !strings.HasPrefix(r.Header.Get("Authorization"), "Hawk ") {
			t.Fatalf("Hawk grant must stay on the auth host: %s %v", r.URL, body)
		}
		if r.UserAgent() != fastlySolverUserAgent || r.Header.Get("Accept") != "*/*" {
			t.Fatal("Hawk grant must use the shared Firefox headers")
		}
		return 200, `{"access_token":"new","refresh_token":"refresh"}`
	})
	if _, err := OAuthToken(context.Background(), strings.Repeat("ab", 32)); err != nil {
		t.Fatal(err)
	}
}
