package simplepush
// taken from http://www.ashishbanerjee.com/home/go/go-generate-uuid


import (
    "encoding/hex"
    "crypto/rand"
)

func GenUUID4() (string, error) {
    uuid := make([]byte, 16)
    n, err := rand.Read(uuid)
    if n != len(uuid) || err != nil {
        return "", err
    }

    uuid[8] = 0x80
    uuid[4] = 0x40

    return hex.EncodeToString(uuid), nil
}
