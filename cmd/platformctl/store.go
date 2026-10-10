package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// secretStore resolves a --secret source that names a reference in the
// operator's secret store. Resolve answers the value and never prints it; its
// errors name the reference, never a value.
type secretStore interface {
	Resolve(ctx context.Context, field, ref string) (string, error)
}

// receiveSecretCmd is the hidden command beekeeper runs as its consumer: it
// reads the value on stdin and hands it to the platformctl that asked,
// through the socket that one listens on, printing nothing.
const receiveSecretCmd = "receive-secret"

// maxSecretBytes bounds what one supplied value may be.
const maxSecretBytes = 1 << 20

// resolveTimeout bounds one reference's resolution: beekeeper gives up on an
// unanswering vault within a minute.
const resolveTimeout = 2 * time.Minute

// beekeeperStore resolves beekeeper:<ref> through beekeeper's consumer
// contract, `beekeeper secret copy <ref> -- <consumer>`: beekeeper reads the
// value in its own process and hands it on stdin to a consumer carrying
// --secret <name>=-. The consumer is this binary's receive-secret, which
// passes the value back over a unix socket in a directory only the caller
// can open. The value travels on stdin and the socket alone, never on an
// argv, in the environment or in a file; beekeeper answers the consumer's
// output with the value redacted, and the consumer prints none.
type beekeeperStore struct {
	// bin is the beekeeper command, self this binary (os.Executable when
	// empty).
	bin, self string
}

func (s beekeeperStore) Resolve(ctx context.Context, field, ref string) (string, error) {
	if s.self == "" {
		self, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("finding platformctl's own binary for beekeeper's consumer: %w", err)
		}
		s.self = self
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "platformctl-secret-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "s")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return "", err
	}
	defer func() { _ = ln.Close() }()

	type received struct {
		value []byte
		err   error
	}
	got := make(chan received, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- received{err: err}
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(resolveTimeout))
		b, err := io.ReadAll(io.LimitReader(conn, maxSecretBytes+1))
		got <- received{value: b, err: err}
	}()

	//nolint:gosec // beekeeper and this binary, the arguments built here; the reference names no value
	cmd := exec.CommandContext(ctx, s.bin, "secret", "copy", ref, "--",
		s.self, receiveSecretCmd, "--socket", socket, "--secret", field+"=-")
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = ln.Close()
		return "", fmt.Errorf("beekeeper secret copy %s: %w%s", ref, err, lastLine(out))
	}
	// beekeeper waits for its consumer, so a value handed back is queued on
	// the socket by now; nothing queued is a consumer that never connected.
	_ = ln.SetDeadline(time.Now().Add(5 * time.Second))
	r := <-got
	switch {
	case r.err != nil:
		return "", fmt.Errorf("beekeeper secret copy %s: the consumer handed nothing back: %w", ref, r.err)
	case len(r.value) > maxSecretBytes:
		return "", fmt.Errorf("beekeeper secret copy %s: the value is over %d bytes", ref, maxSecretBytes)
	}
	return string(r.value), nil
}

// lastLine is beekeeper's last output line, which says why it failed; its
// output never carries a value (it redacts the consumer's).
func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return ": " + l
	}
	return ""
}

// newReceiveSecretCmd is beekeeper's consumer (beekeeperStore): the value on
// stdin to the socket, nothing printed. --secret <field>=- is what makes it a
// consumer beekeeper hands a value to; the field only names the value in an
// error.
func newReceiveSecretCmd() *cobra.Command {
	var socket, secret string
	cmd := leaf(receiveSecretCmd+" --socket <path> --secret <field>=-", "Hand a value beekeeper supplies on stdin to the platformctl that asked", func(pos []string, _, stderr io.Writer) int {
		field, ok := strings.CutSuffix(secret, "=-")
		if len(pos) != 0 || socket == "" || !ok || field == "" {
			return usageError(stderr, receiveSecretCmd+" --socket <path> --secret <field>=-")
		}
		if err := receiveSecret(os.Stdin, socket); err != nil {
			return fail(stderr, fmt.Errorf("--secret %s: %w", field, err))
		}
		return exitOK
	})
	cmd.Hidden = true
	cmd.Flags().StringVar(&socket, "socket", "", "the socket the asking platformctl listens on")
	cmd.Flags().StringVar(&secret, "secret", "", "<field>=-: the value is on stdin")
	return cmd
}

// receiveSecret copies the value on stdin to the socket.
func receiveSecret(stdin io.Reader, socket string) error {
	value, err := io.ReadAll(io.LimitReader(stdin, maxSecretBytes+1))
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}
	if len(value) > maxSecretBytes {
		return fmt.Errorf("the value is over %d bytes", maxSecretBytes)
	}
	conn, err := net.DialTimeout("unix", socket, 10*time.Second)
	if err != nil {
		return err
	}
	if _, err := conn.Write(value); err != nil {
		_ = conn.Close()
		return err
	}
	return conn.Close()
}
