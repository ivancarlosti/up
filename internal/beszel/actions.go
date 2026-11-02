package beszel

import (
	"context"
	"errors"
	"time"

	"golang.org/x/crypto/ssh"
)

// Fingerprint runs the authentication handshake with the agent.
//
// It signs the token with the host key the agent was given, and the agent answers
// with the fingerprint of its own machine. Until this call succeeds the agent
// refuses every other action ("hub not verified"), which is what makes the
// fingerprint the identity of the machine standing behind a token.
func (c *Conn) Fingerprint(ctx context.Context, token string, signer ssh.Signer) (FingerprintMessage, error) {
	signature, err := SignToken(signer, token)
	if err != nil {
		return FingerprintMessage{}, err
	}
	response, err := c.request(ctx, ActionCheckFingerprint, FingerprintRequest{
		Signature:   signature,
		NeedSysInfo: true,
	}, requestTimeout)
	if err != nil {
		return FingerprintMessage{}, err
	}
	if response.Fingerprnt == nil {
		return FingerprintMessage{}, errors.New("the agent answered the handshake without a fingerprint")
	}
	return *response.Fingerprnt, nil
}

// Sample asks the agent for the current counters of the machine.
func (c *Conn) Sample(ctx context.Context, options DataRequestOptions) (*CombinedData, error) {
	response, err := c.request(ctx, ActionGetData, options, requestTimeout)
	if err != nil {
		return nil, err
	}
	if response.SystemData == nil {
		return nil, errors.New("the agent answered without system data")
	}
	return response.SystemData, nil
}

// Smart asks for the SMART report the agent already has cached.
//
// The agent refreshes it in the background (SMART_INTERVAL) and answers with the
// last copy, so this call is cheap enough to run on a timer.
func (c *Conn) Smart(ctx context.Context) (SmartReport, error) {
	response, err := c.request(ctx, ActionGetSmartData, struct{}{}, requestTimeout)
	if err != nil {
		return SmartReport{}, err
	}
	disks := response.SmartData
	if disks == nil {
		disks = map[string]Smart{}
	}
	return SmartReport{Disks: disks, Complete: response.SmartComplete, At: time.Now().UTC()}, nil
}
