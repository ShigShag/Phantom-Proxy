package config

// ServerConfig holds server-side configuration.
type ServerConfig struct {
	ListenAddr    string
	Transport     string
	Secret        string
	SOCKS5Addr    string
	LocalForwards []PortForward
	LogLevel      string

	// TLS
	CertFile string
	KeyFile  string
	CAFile   string

	// SSH
	HostKeyFile string

	// HTTP
	HTTPPath string
}

// ClientConfig holds client-side configuration.
type ClientConfig struct {
	ServerAddr     string
	Transport      string
	Secret         string
	RemoteForwards []PortForward
	LogLevel       string

	// TLS
	CertFile   string
	KeyFile    string
	CAFile     string
	SkipVerify bool

	// SSH
	ClientKeyFile string

	// HTTP
	HTTPPath  string
	HostHeader string
	UserAgent string
}

// PortForward describes a single port-forward rule.
type PortForward struct {
	Bind   string
	Target string
}
