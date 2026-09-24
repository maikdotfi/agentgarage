// Package hosting is how the garage runs privately on one VPS: garage setup,
// which makes the host from nothing and is safe to run again, and the daily
// database snapshots that let a dead host be rebuilt the same way.
package hosting

// Where a host made by Setup keeps things.
const (
	Home = "/var/lib/garage" // databases, workspaces, the socket; owned by the garage user
	Keys = "/etc/garage"     // signing and master keys, trusted keys, r2.env
)
