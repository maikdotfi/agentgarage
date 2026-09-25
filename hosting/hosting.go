// Package hosting is how the garage runs privately on one host: garage setup,
// which makes the host from nothing and is safe to run again, the daily
// database snapshots that let a dead host be rebuilt the same way, and the
// releases through the bucket that let the garage update itself.
package hosting

// Where a host made by Setup keeps things.
const (
	Home = "/var/lib/garage" // databases, workspaces, the socket; owned by the garage user
	Keys = "/etc/garage"     // signing and master keys, trusted keys, r2.env
)
