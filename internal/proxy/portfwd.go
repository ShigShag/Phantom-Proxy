package proxy

import (
	"fmt"
	"log/slog"
	"net"

	"github.com/hashicorp/yamux"

	"github.com/ShigShag/Phantom-Proxy/internal/proto"
)

// LocalForward listens on bindAddr (server side) and forwards connections
// through the yamux session to the client, which dials targetAddr.
func LocalForward(session *yamux.Session, bindAddr, targetAddr string, logger *slog.Logger) error {
	ln, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", bindAddr, err)
	}

	logger.Info("local forward", "bind", bindAddr, "target", targetAddr)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				logger.Debug("local forward accept", "error", err)
				return
			}
			go localForwardConn(session, conn, targetAddr, logger)
		}
	}()

	return nil
}

func localForwardConn(session *yamux.Session, local net.Conn, targetAddr string, logger *slog.Logger) {
	defer local.Close()

	stream, err := session.Open()
	if err != nil {
		logger.Debug("open stream for local forward", "error", err)
		return
	}
	defer stream.Close()

	if err := proto.WriteConnect(stream, targetAddr); err != nil {
		logger.Debug("send CONNECT for local forward", "error", err)
		return
	}

	ok, errMsg, err := proto.ReadConnectAck(stream)
	if err != nil {
		logger.Debug("read CONNECT_ACK for local forward", "error", err)
		return
	}
	if !ok {
		logger.Debug("local forward CONNECT rejected", "target", targetAddr, "error", errMsg)
		return
	}

	relay(stream, local)
}

// RemoteForward listens on bindAddr (client side) and forwards connections
// through the yamux session to the server, which dials targetAddr.
func RemoteForward(session *yamux.Session, bindAddr, targetAddr string, logger *slog.Logger) error {
	ln, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", bindAddr, err)
	}

	logger.Info("remote forward", "bind", bindAddr, "target", targetAddr)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				logger.Debug("remote forward accept", "error", err)
				return
			}
			go remoteForwardConn(session, conn, targetAddr, logger)
		}
	}()

	return nil
}

func remoteForwardConn(session *yamux.Session, local net.Conn, targetAddr string, logger *slog.Logger) {
	defer local.Close()

	stream, err := session.Open()
	if err != nil {
		logger.Debug("open stream for remote forward", "error", err)
		return
	}
	defer stream.Close()

	if err := proto.WriteConnect(stream, targetAddr); err != nil {
		logger.Debug("send CONNECT for remote forward", "error", err)
		return
	}

	ok, errMsg, err := proto.ReadConnectAck(stream)
	if err != nil {
		logger.Debug("read CONNECT_ACK for remote forward", "error", err)
		return
	}
	if !ok {
		logger.Debug("remote forward CONNECT rejected", "target", targetAddr, "error", errMsg)
		return
	}

	relay(stream, local)
}
