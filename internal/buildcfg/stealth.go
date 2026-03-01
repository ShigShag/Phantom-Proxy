//go:build !stealth

package buildcfg

var (
	CertCN         = "phantom-proxy"  // TLS cert CommonName
	SSHChannelType = "phantom-tunnel" // SSH channel type
	SSHUser        = "phantom"        // SSH client username
	SaltPrefix     = "phantom"        // DeterministicSalt prefix
	AuthLabel      = "phantom-proxy"  // label for auth key derivation
	DefaultWSPath  = "/ws"            // default WebSocket path
)
