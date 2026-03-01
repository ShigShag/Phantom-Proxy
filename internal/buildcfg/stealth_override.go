//go:build stealth

package buildcfg

var (
	CertCN         = "localhost"
	SSHChannelType = "direct-tcpip"
	SSHUser        = "admin"
	SaltPrefix     = "svc"
	AuthLabel      = "service-auth"
	DefaultWSPath  = "/health"
)
