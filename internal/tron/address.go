package tron

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"math/big"
	"strings"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func ValidAddress(address string) bool {
	if len(address) != 34 || address[0] != 'T' {
		return false
	}
	n := new(big.Int)
	for _, c := range address {
		i := strings.IndexRune(base58Alphabet, c)
		if i < 0 {
			return false
		}
		n.Mul(n, big.NewInt(58))
		n.Add(n, big.NewInt(int64(i)))
	}
	b := n.Bytes()
	if len(b) != 25 || b[0] != 0x41 {
		return false
	}
	first := sha256.Sum256(b[:21])
	second := sha256.Sum256(first[:])
	return subtle.ConstantTimeCompare(b[21:], second[:4]) == 1
}

// NormalizeAddress accepts checked Base58Check or the 21-byte 41-prefixed
// hexadecimal representation returned in native contract parameters.
func NormalizeAddress(address string) (string, bool) {
	if ValidAddress(address) {
		return address, true
	}
	if len(address) != 42 {
		return "", false
	}
	b, err := hex.DecodeString(address)
	if err != nil || b[0] != 0x41 {
		return "", false
	}
	first := sha256.Sum256(b)
	second := sha256.Sum256(first[:])
	b = append(b, second[:4]...)
	n := new(big.Int).SetBytes(b)
	rem := new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.QuoRem(n, big.NewInt(58), rem)
		out = append(out, base58Alphabet[rem.Int64()])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out), true
}
