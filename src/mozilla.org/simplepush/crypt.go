package simplepush

import (

    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "encoding/base64"
)

func genKey(strength int) ([]byte, error) {
    k := make([]byte, strength)
    if _, err := rand.Read(k); err != nil {
        return nil, err
    }
    return k, nil
}

func Encode(key, value []byte) (string, error) {
    if key == nil {
        return string(value), nil
    }
    // cleanup the val string
    if len(value) == 0 {
        return "", nil
    }
    // pad value to 4 byte bounds

    iv, err := genKey(len(key))
    if err != nil{
        return "", err
    }
    if block, err := aes.NewCipher(key); err != nil {
        return "", err
    } else {
        stream := cipher.NewCTR(block, iv)
        stream.XORKeyStream(value, value)
    }
    enc := append(iv, value...)
    return base64.URLEncoding.EncodeToString(enc), nil
}

func Decode(key []byte, rvalue string) ([]byte, error) {
    if key == nil || len(key) == 0{
        return []byte(rvalue), nil
    }
    // NOTE: using the URLEncoding.Decode(...) seems to muck with the
    // returned value. Using string, which wants to return a cleaner
    // version.
    value, err := base64.URLEncoding.DecodeString(string(rvalue))
    if err != nil {
        return nil, err
    }

    keySize := len(key)
    iv := value[:keySize]
    value = value[keySize:]

    if block, err := aes.NewCipher(key); err != nil {
        return nil, err
    } else {
        stream := cipher.NewCTR(block, iv)
        stream.XORKeyStream(value, value)
    }
    return value, nil
}
