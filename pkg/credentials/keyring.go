package credentials

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const sshKeyVariable = "BOARD_SSH_KEY"

func sshKeyReference(inherited []string) string {
	var reference string
	for _, entry := range inherited {
		if value, ok := strings.CutPrefix(entry, sshKeyVariable+"="); ok {
			reference = value
		}
	}
	return reference
}

func loadKeyring(ctx context.Context, reference string) (agent.Agent, error) {
	if !strings.HasPrefix(reference, "op://") {
		return nil, errors.New("BOARD_SSH_KEY must be an op:// reference")
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	data, err := exec.CommandContext(ctx, "op", "read", "--no-newline", reference).Output()
	defer clear(data)
	if err != nil {
		return nil, errors.New("cannot resolve BOARD_SSH_KEY through 1Password")
	}
	key, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return nil, errors.New("BOARD_SSH_KEY must resolve to an unencrypted SSH private key")
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		return nil, errors.New("cannot load BOARD_SSH_KEY into the signing agent")
	}
	return keyring, nil
}
