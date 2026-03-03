package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ShigShag/Phantom-Proxy/internal/buildcfg"
	pcrypto "github.com/ShigShag/Phantom-Proxy/internal/crypto"
	"github.com/ShigShag/Phantom-Proxy/internal/transport"
)

func init() {
	transport.Register(&SSH{})
}

// SSH implements the Transport interface using SSH channels.
type SSH struct{}

func (s *SSH) Name() string { return "ssh" }

// loadOrGenerateSigner loads a private key from file, or generates an ephemeral ed25519 key.
func loadOrGenerateSigner(keyFile string) (ssh.Signer, error) {
	if keyFile != "" {
		keyData, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("read key %s: %w", keyFile, err)
		}
		signer, err := ssh.ParsePrivateKey(keyData)
		if err != nil {
			return nil, fmt.Errorf("parse key %s: %w", keyFile, err)
		}
		return signer, nil
	}

	// Auto-generate ephemeral ed25519 key.
	priv, err := pcrypto.GenerateED25519Key()
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("create signer: %w", err)
	}
	return signer, nil
}

// Dial connects to an SSH server and opens a custom channel, returning it as a net.Conn.
func (s *SSH) Dial(addr string, cfg *transport.Config) (net.Conn, error) {
	signer, err := loadOrGenerateSigner(cfg.ClientKeyFile)
	if err != nil {
		return nil, err
	}

	sshCfg := &ssh.ClientConfig{
		User: buildcfg.SSHUser,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	rawConn, err := cfg.DialRawTCP(context.Background(), addr)
	if err != nil {
		return nil, err
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(rawConn, addr, sshCfg)
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)

	ch, chanReqs, err := client.OpenChannel(buildcfg.SSHChannelType, nil)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("open channel: %w", err)
	}
	go ssh.DiscardRequests(chanReqs)

	tcpAddr, _ := net.ResolveTCPAddr("tcp", addr)
	return &channelConn{
		Channel:    ch,
		sshConn:    client,
		localAddr:  &net.TCPAddr{},
		remoteAddr: tcpAddr,
	}, nil
}

// Listen starts an SSH server that accepts the custom channel type and bridges it to net.Listener.
func (s *SSH) Listen(addr string, cfg *transport.Config) (net.Listener, error) {
	signer, err := loadOrGenerateSigner(cfg.HostKeyFile)
	if err != nil {
		return nil, err
	}

	sshCfg := &ssh.ServerConfig{
		NoClientAuth: true,
	}
	sshCfg.AddHostKey(signer)

	tcpLn, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	ln := &sshListener{
		tcpLn:  tcpLn,
		sshCfg: sshCfg,
		connCh: make(chan net.Conn, 16),
		done:   make(chan struct{}),
	}
	go ln.acceptLoop()
	return ln, nil
}

// sshListener bridges SSH channel acceptance to the net.Listener interface.
type sshListener struct {
	tcpLn  net.Listener
	sshCfg *ssh.ServerConfig
	connCh chan net.Conn
	done   chan struct{}
	once   sync.Once
}

func (l *sshListener) acceptLoop() {
	for {
		tcpConn, err := l.tcpLn.Accept()
		if err != nil {
			select {
			case <-l.done:
				return
			default:
				continue
			}
		}
		go l.handleSSHConn(tcpConn)
	}
}

func (l *sshListener) handleSSHConn(tcpConn net.Conn) {
	sshConn, chans, reqs, err := ssh.NewServerConn(tcpConn, l.sshCfg)
	if err != nil {
		tcpConn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != buildcfg.SSHChannelType {
			newCh.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		ch, reqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(reqs)

		conn := &channelConn{
			Channel:    ch,
			sshConn:    sshConn,
			localAddr:  tcpConn.LocalAddr(),
			remoteAddr: tcpConn.RemoteAddr(),
		}

		select {
		case l.connCh <- conn:
		case <-l.done:
			conn.Close()
			return
		}
	}
}

func (l *sshListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connCh:
		return conn, nil
	case <-l.done:
		return nil, errors.New("listener closed")
	}
}

func (l *sshListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.tcpLn.Close()
}

func (l *sshListener) Addr() net.Addr {
	return l.tcpLn.Addr()
}

// channelConn adapts ssh.Channel to net.Conn.
type channelConn struct {
	ssh.Channel
	sshConn    interface{ Close() error }
	localAddr  net.Addr
	remoteAddr net.Addr
	closeOnce  sync.Once
}

func (c *channelConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *channelConn) RemoteAddr() net.Addr { return c.remoteAddr }

func (c *channelConn) SetDeadline(t time.Time) error     { return nil }
func (c *channelConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *channelConn) SetWriteDeadline(t time.Time) error { return nil }

func (c *channelConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		err = c.Channel.Close()
		c.sshConn.Close()
	})
	return err
}
