package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/Suhaibinator/kms/internal/crypto"
)

// newPrompter returns a no-echo passphrase prompter bound to the CLI's stdin,
// or nil when stdin is not a terminal (so Unseal fails fast instead of hanging,
// per plan 11.2.1(5)).
func (c *CLI) newPrompter() crypto.PassphrasePrompter {
	if c.Stdin == nil || !term.IsTerminal(int(c.Stdin.Fd())) {
		return nil
	}
	return func(confirm bool) ([]byte, error) {
		fmt.Fprint(c.Stderr, "Master passphrase: ")
		p1, err := term.ReadPassword(int(c.Stdin.Fd()))
		fmt.Fprintln(c.Stderr)
		if err != nil {
			return nil, err
		}
		if !confirm {
			return p1, nil
		}
		fmt.Fprint(c.Stderr, "Confirm passphrase: ")
		p2, err := term.ReadPassword(int(c.Stdin.Fd()))
		fmt.Fprintln(c.Stderr)
		if err != nil {
			crypto.Zero(p1)
			return nil, err
		}
		match := bytes.Equal(p1, p2)
		crypto.Zero(p2)
		if !match {
			crypto.Zero(p1)
			return nil, errors.New("passphrases do not match")
		}
		return p1, nil
	}
}

// readNewPassphrase reads a fresh master passphrase (twice, with confirmation)
// for rotation. It requires a TTY; there is no environment fallback so a new
// key is never derived from an unintended source.
func (c *CLI) readNewPassphrase() ([]byte, error) {
	if c.Stdin == nil || !term.IsTerminal(int(c.Stdin.Fd())) {
		return nil, errors.New("a TTY is required to enter a new master passphrase")
	}
	prompt := c.newPrompter()
	// newPrompter's confirm path prints "Master passphrase"/"Confirm"; that is
	// adequate for rotation and keeps a single no-echo implementation.
	return prompt(true)
}

// unseal acquires and verifies the master key against store using the same
// resolution order as serve: key file, then KMS_MASTER_PASSPHRASE, then an
// interactive TTY prompt. createIfMissing generates a key file (or prompts for
// a new passphrase) when initializing a fresh database.
func (c *CLI) unseal(ctx context.Context, store crypto.KeyMetadataStore, keyFile string, createIfMissing bool) (*crypto.Keyring, error) {
	opts := crypto.UnsealOptions{
		KeyFilePath:            keyFile,
		CreateKeyFileIfMissing: createIfMissing,
		Passphrase:             []byte(os.Getenv("KMS_MASTER_PASSPHRASE")),
		Prompt:                 c.newPrompter(),
	}
	return crypto.Unseal(ctx, store, opts)
}
