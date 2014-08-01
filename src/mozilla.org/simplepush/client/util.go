package client

import (
	"crypto/rand"
	"encoding/hex"
	"io"
)

func GenerateIdSize(prefix string, size int) (result string, err error) {
	bytes := make([]byte, size)
	if _, err = io.ReadFull(rand.Reader, bytes); err != nil {
		return
	}
	results := make([]byte, len(prefix)+hex.EncodedLen(len(bytes)))
	copy(results[:len(prefix)], prefix)
	hex.Encode(results[len(prefix):], bytes)
	return string(results), nil
}

func GenerateId() (string, error) {
	return GenerateIdSize("", 16)
}
