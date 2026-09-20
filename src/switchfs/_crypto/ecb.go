package _crypto

import (
	"crypto/aes"
	"errors"
)

func DecryptAes128Ecb(data, key []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, errors.New("ecb: key must be 16 bytes for AES-128")
	}
	if len(data) == 0 || len(data)%16 != 0 {
		return nil, errors.New("ecb: data length must be a non-zero multiple of 16")
	}
	cipher, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	decrypted := make([]byte, len(data))
	size := 16

	for bs, be := 0, size; bs < len(data); bs, be = bs+size, be+size {
		cipher.Decrypt(decrypted[bs:be], data[bs:be])
	}

	return decrypted, nil
}
