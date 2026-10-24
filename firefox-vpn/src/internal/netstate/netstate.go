// Package netstate checks Linux's default routes without sending probes.
// A network without internet access, or
// behind a captive portal, counts as connected.
package netstate

// Connected reports whether a default route exists. If the state cannot be
// read, it reports true so a failed check never holds the program back.
func Connected() bool {
	return connected()
}
