// Package crypto seals credential blobs with AES-256-GCM under the master key.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// Sealer encrypts and decrypts small secrets. Envelope: "G1" || nonce(12) || ciphertext.
type Sealer struct {
	aead cipher.AEAD
}

// ErrBadEnvelope is returned when a blob is not a valid sealed envelope.
var ErrBadEnvelope = errors.New("crypto: malformed envelope")

const envelopeVersion = "G1"

// New builds a Sealer for a 32-byte key.
func New(key []byte) (*Sealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plaintext, binding it to aad (the owning record id).
func (s *Sealer) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: nonce: %w", err)
	}
	out := make([]byte, 0, len(envelopeVersion)+len(nonce)+len(plaintext)+s.aead.Overhead())
	out = append(out, envelopeVersion...)
	out = append(out, nonce...)
	return s.aead.Seal(out, nonce, plaintext, aad), nil
}

// Open decrypts an envelope produced by Seal with the same aad.
func (s *Sealer) Open(blob, aad []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(blob) < len(envelopeVersion)+ns || string(blob[:len(envelopeVersion)]) != envelopeVersion {
		return nil, ErrBadEnvelope
	}
	nonce := blob[len(envelopeVersion) : len(envelopeVersion)+ns]
	pt, err := s.aead.Open(nil, nonce, blob[len(envelopeVersion)+ns:], aad)
	if err != nil {
		return nil, fmt.Errorf("crypto: %w", err)
	}
	return pt, nil
}
