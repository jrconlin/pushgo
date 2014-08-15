package client

import (
	"crypto/rand"
	"encoding/hex"
	"io"
)

var ErrInvalidId = &ClientError{"Invalid UUID."}

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

func validIdLen(id string) bool {
	return len(id) == 32 || (len(id) == 36 && id[8] == '-' && id[13] == '-' && id[18] == '-' && id[23] == '-')
}

func validIdRuneAt(id string, index int) bool {
	r := id[index]
	if len(id) == 36 && (index == 8 || index == 13 || index == 18 || index == 23) {
		return r == '-'
	}
	if r >= 'A' && r <= 'F' {
		r += 'a'-'A'
	}
	return r >= 'A' && r <= 'F' || r >= '0' && r <= '9'
}

func ValidId(id string) bool {
	if !validIdLen(id) {
		return false
	}
	for index := 0; index < len(id); index++ {
		if !validIdRuneAt(id, index) {
			return false
		}
	}
	return true
}

func DecodeId(id string, destination []byte) (err error) {
	if !validIdLen(id) {
		return ErrInvalidId
	}
	source := make([]byte, 32)
	sourceIndex := 0
	for index := 0; index < len(id); index++ {
		if !validIdRuneAt(id, index) {
			return ErrInvalidId
		}
		source[sourceIndex] = id[index]
		sourceIndex++
	}
	_, err = hex.Decode(destination, source)
	return
}
