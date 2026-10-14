package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/oku-trade/oku-cli/pkg/privy"
	"github.com/oku-trade/oku-cli/pkg/signing"
)

// privySigner never holds or serializes the Privy authorization key outside
// the in-memory Session. Every signature is checked against the stored agent.
type privySigner struct {
	session           *privy.Session
	walletID, address string
}

func (s *privySigner) Address() string { return s.address }
func (s *privySigner) SignDigest([32]byte) (signing.Signature, error) {
	return signing.Signature{}, errors.New("Privy requires cancellable typed-data signing")
}
func (s *privySigner) SignAgent(ctx context.Context, connectionID [32]byte) (signing.Signature, error) {
	raw, err := s.session.SignTypedData(ctx, s.walletID, privy.HyperliquidAgentTypedData("a", connectionID))
	if err != nil {
		return signing.Signature{}, err
	}
	v := raw[64]
	if v < 27 {
		v += 27
	}
	if v != 27 && v != 28 {
		return signing.Signature{}, errors.New("invalid Privy signature recovery id")
	}
	sig := signing.Signature{R: "0x" + hex.EncodeToString(raw[:32]), S: "0x" + hex.EncodeToString(raw[32:64]), V: uint64(v)}
	recovered, err := signing.RecoverAddress(signing.AgentDigest(connectionID), sig)
	if err != nil {
		return signing.Signature{}, err
	}
	if !strings.EqualFold(recovered, s.address) {
		return signing.Signature{}, errors.New("Privy signed with a different wallet than the approved agent")
	}
	return sig, nil
}
