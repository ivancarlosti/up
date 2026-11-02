package beszel

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// keySeedDomain separates this derivation from every other use of the cluster
// key, and carries a version so a future change can be introduced without
// silently invalidating the KEY of every installed agent.
const keySeedDomain = "github.com/ivancarlosti/up/beszel/host-key/v1"

// HostSigner derives the SSH signer the agents authenticate us with.
//
// The agent proves it trusts us by checking a signature over its own token with
// the public key it was started with (its KEY variable). That key pair must be
// the SAME on every node of a cluster, because the agent may be handed over to
// any of them when the node it was talking to goes away; and it must survive a
// restart, because the operator pasted its public half into a container years
// earlier.
//
// Deriving it from the cluster private key gives both properties for free: the
// key is already shared by every node, already persisted in the settings table,
// and already rotated as one operation. Nothing new is stored, and no second
// secret can drift.
//
// The consequence worth knowing: regenerating the cluster key rotates this key
// too, and every agent then has to be restarted with the new one. Admin > Cluster
// says so next to the button.
func HostSigner(clusterKey string) (ssh.Signer, error) {
	if clusterKey == "" {
		return nil, fmt.Errorf("the cluster private key is empty")
	}
	seed := sha256.Sum256([]byte(keySeedDomain + "\x00" + clusterKey))
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(seed[:]))
	if err != nil {
		return nil, fmt.Errorf("building the agent host key: %w", err)
	}
	return signer, nil
}

// HostPublicKey renders the signer as the authorized_keys line the operator
// pastes into the agent KEY variable.
func HostPublicKey(signer ssh.Signer) string {
	return string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
}

// SignToken produces the signature the agent expects in FingerprintRequest.
//
// The challenge is the token itself and the signature is the raw SSH blob (the
// agent wraps it in an ssh.Signature before verifying), so this is not a general
// purpose signing primitive: it is exactly what the fingerprints handshake
// expects, and nothing else may be signed with it.
func SignToken(signer ssh.Signer, token string) ([]byte, error) {
	signature, err := signer.Sign(nil, []byte(token))
	if err != nil {
		return nil, fmt.Errorf("signing the agent token: %w", err)
	}
	return signature.Blob, nil
}

// HostPublicKeyFingerprint is the SHA-256 fingerprint of the host key, shown in
// the UI so an operator can compare it with what the agent logged.
func HostPublicKeyFingerprint(signer ssh.Signer) string {
	sum := sha256.Sum256(signer.PublicKey().Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}
