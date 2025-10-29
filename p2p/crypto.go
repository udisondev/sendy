package p2p

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"fmt"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

// Curve25519Key represents an encryption key
type Curve25519PublicKey [32]byte
type Curve25519PrivateKey [32]byte

// DeriveEncryptionKeys deterministically derives Curve25519 keys from Ed25519 keys
// This allows reusing existing Ed25519 keys for encryption
func DeriveEncryptionKeys(edPriv ed25519.PrivateKey) (*Curve25519PublicKey, *Curve25519PrivateKey, error) {
	if len(edPriv) != ed25519.PrivateKeySize {
		return nil, nil, fmt.Errorf("invalid ed25519 private key size")
	}

	// Use seed from Ed25519 private key
	seed := edPriv.Seed()

	// Deterministically generate Curve25519 private key from seed
	// Use SHA512 to get 64 bytes, then take first 32
	h := sha512.Sum512(append([]byte("curve25519-encryption:"), seed...))

	var curvePriv Curve25519PrivateKey
	copy(curvePriv[:], h[:32])

	// Apply clamping for Curve25519 private key
	curvePriv[0] &= 248
	curvePriv[31] &= 127
	curvePriv[31] |= 64

	// Generate public key from private
	var curvePub Curve25519PublicKey
	curve25519.ScalarBaseMult((*[32]byte)(&curvePub), (*[32]byte)(&curvePriv))

	return &curvePub, &curvePriv, nil
}

// DerivePublicEncryptionKey derives Curve25519 public key from Ed25519 keys
// Requires private key for secure derivation
func DerivePublicEncryptionKey(edPriv ed25519.PrivateKey) (*Curve25519PublicKey, error) {
	pub, _, err := DeriveEncryptionKeys(edPriv)
	return pub, err
}


// GenerateEncryptionKeys generates a new keypair for encryption
func GenerateEncryptionKeys() (*Curve25519PublicKey, *Curve25519PrivateKey, error) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate encryption keys: %w", err)
	}

	var curvePub Curve25519PublicKey
	var curvePriv Curve25519PrivateKey
	copy(curvePub[:], (*pub)[:])
	copy(curvePriv[:], (*priv)[:])

	return &curvePub, &curvePriv, nil
}

// EncryptMessage encrypts a message for the recipient
// Uses NaCl box for authenticated encryption
func EncryptMessage(message []byte, recipientPub *Curve25519PublicKey, senderPriv *Curve25519PrivateKey) ([]byte, error) {
	// Generate random nonce (24 bytes)
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// Encrypt message
	// box.Seal prepends nonce to encrypted message
	encrypted := box.Seal(
		nonce[:], // Prefix with nonce
		message,
		&nonce,
		(*[32]byte)(recipientPub),
		(*[32]byte)(senderPriv),
	)

	return encrypted, nil
}

// DecryptMessage decrypts a message from sender
func DecryptMessage(encrypted []byte, senderPub *Curve25519PublicKey, recipientPriv *Curve25519PrivateKey) ([]byte, error) {
	if len(encrypted) < 24 {
		return nil, fmt.Errorf("message too short: must be at least 24 bytes")
	}

	// Extract nonce (first 24 bytes)
	var nonce [24]byte
	copy(nonce[:], encrypted[:24])

	// Decrypt (skip first 24 bytes with nonce)
	decrypted, ok := box.Open(
		nil,
		encrypted[24:],
		&nonce,
		(*[32]byte)(senderPub),
		(*[32]byte)(recipientPriv),
	)
	if !ok {
		return nil, fmt.Errorf("decryption failed: authentication failed or corrupted message")
	}

	return decrypted, nil
}

// EncryptPeerMessage encrypts a message using Ed25519 keys of sender and recipient
// This is a convenience function that does key derivation automatically
// IMPORTANT: requires prior exchange of Curve25519 keys via handshake
func EncryptPeerMessage(message []byte, recipientEdPub ed25519.PublicKey, senderEdPriv ed25519.PrivateKey) ([]byte, error) {
	// For encryption, Curve25519 keys are needed, which must be exchanged via handshake
	// Use EncryptMessage with pre-exchanged Curve25519 keys
	return nil, fmt.Errorf("use EncryptMessage with pre-exchanged Curve25519 keys")
}

// SignedMessage represents a message with Ed25519 signature
// This protects against MITM attacks on WebRTC signaling
type SignedMessage struct {
	Payload   []byte // Encrypted message payload
	Signature []byte // Ed25519 signature of the payload
}

// SignMessage signs a message with Ed25519 private key
// Returns signature bytes (64 bytes for Ed25519)
func SignMessage(message []byte, privKey ed25519.PrivateKey) []byte {
	return ed25519.Sign(privKey, message)
}

// VerifySignature verifies Ed25519 signature
// pubKey is the sender's Ed25519 public key (PeerID)
func VerifySignature(message []byte, signature []byte, pubKey ed25519.PublicKey) bool {
	if len(signature) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pubKey, message, signature)
}
