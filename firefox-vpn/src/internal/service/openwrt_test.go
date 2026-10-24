package service

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"firefox-vpn/internal/config"
	"firefox-vpn/internal/fxa"
)

func TestOpenWrtNewAccountBecomesSavedDefault(t *testing.T) {
	d := newTestDaemon(t)
	first := addTestAccount(t, d, "first")
	if err := d.activateAccount(first); err != nil {
		t.Fatal(err)
	}
	setCoreControlPlane(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodHead {
			return coreResponse(r, http.StatusOK, "", quotaHeader(2)), nil
		}
		return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL)
	})
	token := &fxa.TokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 3600}
	acc, err := d.addSignedInAccount("new@example.com", "", token)
	if err != nil {
		t.Fatal(err)
	}
	active, ok := d.mgr.Active()
	if !ok || active.ID != acc.ID {
		t.Fatal("new account was not activated")
	}
	saved, _, err := config.Load(d.paths.Config)
	if err != nil || len(saved.Accounts.Order) != 2 || saved.Accounts.Order[0].ID != acc.ID || saved.Accounts.Order[1].ID != first {
		t.Fatalf("new default order was not saved: %+v, %v", saved.Accounts.Order, err)
	}
	// Signing an existing account in again must not duplicate it or steal priority.
	if _, err := d.addSignedInAccount("first@example.com", "", token); err != nil {
		t.Fatal(err)
	}
	if order := d.config().Accounts.Order; len(order) != 2 || order[0].ID != acc.ID {
		t.Fatalf("re-login changed default: %+v", order)
	}
}

func TestOpenWrtBackgroundConnectionTest(t *testing.T) {
	// Reject without a running engine, and don't mistake a quota read for an IP check.
	stopped := newTestDaemon(t)
	if r := newDashboardClient(t, stopped).do(http.MethodPost, "/api/test?background=1", nil); r.Code != http.StatusConflict {
		t.Fatalf("test accepted without proxy: %d", r.Code)
	}
	d, fixture := runLazyDaemon(t)
	c := newDashboardClient(t, d)
	// Hold server-list lookup to prove the API returns before slow network IO.
	gate := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(gate) })
	setCoreControlPlane(t, func(r *http.Request) (*http.Response, error) {
		<-gate
		return fixture.controlPlane.handle(r)
	})
	r := c.do(http.MethodPost, "/api/test?background=1", nil)
	if r.Code != http.StatusAccepted {
		t.Fatalf("background test: %d %s", r.Code, r.Body)
	}
	if view, _ := d.StatusView(); !view.Testing {
		t.Fatal("status does not report ongoing test")
	}
	if r := c.do(http.MethodPost, "/api/test?background=1", nil); r.Code != http.StatusAccepted {
		t.Fatal("duplicate request failed")
	}
	release.Do(func() { close(gate) })
	waitForCondition(t, 5*time.Second, func() bool {
		view, err := d.StatusView()
		return err == nil && !view.Testing && view.LastTest != nil
	})
	view, _ := d.StatusView()
	if fixture.passes.Load() != 1 || fixture.connects.Load() == 0 || view.LastTest.Error == "" {
		t.Fatalf("probe did not wake proxy/report fixture refusal: %+v", view.LastTest)
	}
}
