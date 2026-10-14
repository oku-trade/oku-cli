// Package signing implements Hyperliquid L1 agent signing (sign_l1_action),
// ported from trade/src/app/features/perps/lib/hyperliquid{,Hash}.ts.
//
// Only the mainnet "a" source is supported because the Oku backend hardcodes
// Hyperliquid mainnet (account/src/connectapi/perps/signing.go).
package signing

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

// Signature is an EVM signature in the shape ConfirmAction expects.
type Signature struct {
	R string `json:"r"`
	S string `json:"s"`
	V uint64 `json:"v"`
}

// SignedAction mirrors gfxcafe.oku.account.v2.SignedAction.
type SignedAction struct {
	ActionJSON string    `json:"actionJson"`
	Nonce      string    `json:"nonce"` // int64 encoded as string per protojson
	Signature  Signature `json:"signature"`
}

// Signer signs an L1 connection id. Implementations must never expose key
// material through this interface.
type Signer interface {
	Address() string
	SignDigest(digest [32]byte) (Signature, error)
}

func keccak(parts ...[]byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// ActionHash returns keccak256(msgpack(action) || nonce_be64 || vaultFlag).
func ActionHash(actionJSON string, vault string, nonce uint64) ([32]byte, error) {
	packed, err := EncodeActionJSON(actionJSON)
	if err != nil {
		return [32]byte{}, err
	}
	nb := make([]byte, 8)
	binary.BigEndian.PutUint64(nb, nonce)
	vb := []byte{0}
	if vault != "" {
		addr, err := parseAddress(vault)
		if err != nil {
			return [32]byte{}, fmt.Errorf("vault address: %w", err)
		}
		vb = append([]byte{1}, addr...)
	}
	return keccak(packed, nb, vb), nil
}

func parseAddress(a string) ([]byte, error) {
	h := strings.TrimPrefix(a, "0x")
	if len(h) != 40 {
		return nil, errors.New("must be a 20-byte hex address")
	}
	return hex.DecodeString(strings.ToLower(h))
}

var (
	domainTypeHash = keccak([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	agentTypeHash  = keccak([]byte("Agent(string source,bytes32 connectionId)"))
)

// AgentDigest returns the EIP-712 digest for Agent{source:"a", connectionId}
// in domain {name:"Exchange", version:"1", chainId:1337, verifyingContract:0x0}.
func AgentDigest(connectionID [32]byte) [32]byte {
	var chainID [32]byte
	binary.BigEndian.PutUint64(chainID[24:], 1337)
	var zeroAddr [32]byte
	name := keccak([]byte("Exchange"))
	version := keccak([]byte("1"))
	domain := keccak(domainTypeHash[:], name[:], version[:], chainID[:], zeroAddr[:])
	source := keccak([]byte("a"))
	msg := keccak(agentTypeHash[:], source[:], connectionID[:])
	return keccak([]byte{0x19, 0x01}, domain[:], msg[:])
}

// SignAction signs the exact actionJSON for the master account. Vault and
// subaccount routing is deliberately unsupported (v1 is master-only), so the
// vault flag is always 0x00.
func SignAction(s Signer, actionJSON string, nonce uint64) (SignedAction, error) {
	return SignActionContext(context.Background(), s, actionJSON, nonce)
}

// SignActionContext propagates cancellation to remote signing implementations.
func SignActionContext(ctx context.Context, s Signer, actionJSON string, nonce uint64) (SignedAction, error) {
	if s == nil {
		return SignedAction{}, errors.New("signer unavailable")
	}
	conn, err := ActionHash(actionJSON, "", nonce)
	if err != nil {
		return SignedAction{}, err
	}
	var sig Signature
	if remote, ok := s.(interface {
		SignAgent(context.Context, [32]byte) (Signature, error)
	}); ok {
		sig, err = remote.SignAgent(ctx, conn)
	} else {
		if err = ctx.Err(); err != nil {
			return SignedAction{}, err
		}
		sig, err = s.SignDigest(AgentDigest(conn))
	}
	if err != nil {
		return SignedAction{}, err
	}
	return SignedAction{
		ActionJSON: actionJSON,
		Nonce:      fmt.Sprint(nonce),
		Signature:  sig,
	}, nil
}

// KeySigner signs with an in-memory secp256k1 key.
type KeySigner struct {
	priv *secp256k1.PrivateKey
	addr string
}

// NewKeySigner builds a signer from 32 raw key bytes.
func NewKeySigner(raw []byte) (*KeySigner, error) {
	if len(raw) != 32 {
		return nil, errors.New("private key must be 32 bytes")
	}
	priv := secp256k1.PrivKeyFromBytes(raw)
	if priv.Key.IsZero() {
		return nil, errors.New("invalid private key")
	}
	return &KeySigner{priv: priv, addr: AddressOf(priv.PubKey())}, nil
}

// GenerateKey returns 32 random key bytes.
func GenerateKey() ([]byte, error) {
	k, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	return k.Serialize(), nil
}

// AddressOf returns the EIP-55 checksummed address of a public key.
func AddressOf(pub *secp256k1.PublicKey) string {
	h := keccak(pub.SerializeUncompressed()[1:])
	return checksum(hex.EncodeToString(h[12:]))
}

func checksum(lowerHex string) string {
	h := keccak([]byte(lowerHex))
	out := []byte(lowerHex)
	for i, c := range out {
		if c >= 'a' && c <= 'f' && (h[i/2]>>(4*(1-uint(i%2))))&0xf >= 8 {
			out[i] = c - 32
		}
	}
	return "0x" + string(out)
}

// Address implements Signer.
func (k *KeySigner) Address() string { return k.addr }

// SignDigest implements Signer. v is normalized to 27/28 like the frontend.
func (k *KeySigner) SignDigest(d [32]byte) (Signature, error) {
	compact := ecdsa.SignCompact(k.priv, d[:], false) // [27+recid] || r || s
	return Signature{
		R: "0x" + hex.EncodeToString(compact[1:33]),
		S: "0x" + hex.EncodeToString(compact[33:65]),
		V: uint64(compact[0]),
	}, nil
}

// RecoverAddress recovers the signer address for a digest, for self-checks.
func RecoverAddress(d [32]byte, sig Signature) (string, error) {
	r, err1 := hex.DecodeString(strings.TrimPrefix(sig.R, "0x"))
	s, err2 := hex.DecodeString(strings.TrimPrefix(sig.S, "0x"))
	if err1 != nil || err2 != nil || len(r) != 32 || len(s) != 32 {
		return "", errors.New("malformed signature")
	}
	v := sig.V
	if v < 27 {
		v += 27
	}
	compact := append(append([]byte{byte(v)}, r...), s...)
	pub, _, err := ecdsa.RecoverCompact(compact, d[:])
	if err != nil {
		return "", err
	}
	return AddressOf(pub), nil
}
