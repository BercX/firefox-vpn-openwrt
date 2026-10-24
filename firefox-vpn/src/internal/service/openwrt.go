// SPDX-License-Identifier: MIT
package service

import (
	"net/http"
	"time"

	"firefox-vpn/internal/proxy"
)

// The probe can outlast rpcd's request timeout. Keep it in the daemon and
// report progress/results through the existing status API instead.
func (s *dashboardServer) handleBackgroundTest(w http.ResponseWriter, r *http.Request) {
	d := s.d
	if d.currentEngine() == nil {
		writeError(w, http.StatusConflict, "Connect the proxy before checking its public IP")
		return
	}
	if d.autoTesting.CompareAndSwap(false, true) {
		d.mu.Lock()
		d.exit, d.lastTest = nil, nil
		d.mu.Unlock()
		go func() {
			defer d.autoTesting.Store(false)
			if _, err := d.ConnectionTest(); err != nil {
				d.mu.Lock()
				d.lastTest = &proxy.ConnectionTestResult{At: time.Now(), Error: err.Error()}
				d.mu.Unlock()
			}
		}()
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"testing": true})
}
