package authentication

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

type replayCipher struct{ aead cipher.AEAD }

func newReplayCipher(key []byte) (*replayCipher, error) {
	if len(key) != 32 {
		return nil, errors.New("refresh replay encryption key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &replayCipher{aead: aead}, nil
}

func (c *replayCipher) encrypt(plaintext, additionalData string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), []byte(additionalData))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (c *replayCipher) decrypt(encoded, additionalData string) (string, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(sealed) < c.aead.NonceSize() {
		return "", ErrInvalidRefreshToken
	}
	nonce := sealed[:c.aead.NonceSize()]
	plaintext, err := c.aead.Open(nil, nonce, sealed[c.aead.NonceSize():], []byte(additionalData))
	if err != nil {
		return "", fmt.Errorf("decrypt refresh replay: %w", ErrInvalidRefreshToken)
	}
	return string(plaintext), nil
}

func replayAAD(sessionID, requestID, oldHash, newHash string) string {
	return sessionID + "\x00" + requestID + "\x00" + oldHash + "\x00" + newHash
}
