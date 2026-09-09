//go:build linux || darwin

package ports

import (
	"context"
	"errors"
	"net"
	"strconv"
	"syscall"

	"eve/internal/domain"
	"golang.org/x/sys/unix"
)

// ProbeTCP exclusively binds IPv4 wildcard and, where supported, IPv6-only
// wildcard. It disables address/port reuse even where Go's listener defaults
// enable it. Both sockets are released before returning; no process is started.
func ProbeTCP(ctx context.Context, port int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if port < 1 || port > 65535 {
		return &domain.Error{Code: "E_PORT_RANGE", Message: "TCP port must be between 1 and 65535", Port: port}
	}
	lc := net.ListenConfig{Control: exclusiveSocket}
	v4, err := lc.Listen(ctx, "tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		return probeError(ctx, port, err)
	}
	defer v4.Close()
	v6, err := lc.Listen(ctx, "tcp6", net.JoinHostPort("::", strconv.Itoa(port)))
	if err != nil {
		// IPv6 may be disabled on an otherwise supported host. Other failures
		// (including permission errors and busy ports) are never "unsupported".
		if errors.Is(err, unix.EAFNOSUPPORT) || errors.Is(err, unix.EPROTONOSUPPORT) || errors.Is(err, unix.EADDRNOTAVAIL) {
			return ctx.Err()
		}
		return probeError(ctx, port, err)
	}
	if err := v6.Close(); err != nil {
		return probeError(ctx, port, err)
	}
	return ctx.Err()
}

func exclusiveSocket(network, _ string, raw syscall.RawConn) error {
	var optionErr error
	err := raw.Control(func(fd uintptr) {
		for _, opt := range []int{unix.SO_REUSEADDR, unix.SO_REUSEPORT} {
			if optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, opt, 0); optionErr != nil {
				return
			}
		}
		if network == "tcp6" {
			optionErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, 1)
		}
	})
	if err != nil {
		return err
	}
	return optionErr
}

func probeError(ctx context.Context, port int, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, unix.EADDRINUSE) {
		return &domain.Error{Code: "E_PORT_OCCUPIED", Message: "TCP port is occupied; EVE does not identify or stop its owner", Port: port}
	}
	return &domain.Error{Code: "E_PORT_PROBE", Message: "exclusive TCP availability could not be established", Port: port}
}
