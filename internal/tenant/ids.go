package tenant

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"strings"
	"time"
)

var idEncoding = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

// NewID returns prefix + a time-ordered, 128-bit ID (48-bit ms timestamp +
// 80 random bits), e.g. "u_01j9...".
func NewID(prefix string) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixMilli())<<16)
	if _, err := rand.Read(b[6:]); err != nil {
		panic("tenant: crypto/rand: " + err.Error())
	}
	return prefix + idEncoding.EncodeToString(b[:])
}

// ID prefixes.
const (
	PrefixUser         = "u_"
	PrefixPlan         = "plan_"
	PrefixSubscription = "sub_"
	PrefixKey          = "pk_"
	PrefixOrder        = "ord_"
)

const keySecretBytes = 32

// NewKeySecret returns a fresh access key: its public ID and secret. The
// credential handed to the user is FormatKey(id, secret).
func NewKeySecret() (id, secret string) {
	b := make([]byte, keySecretBytes)
	if _, err := rand.Read(b); err != nil {
		panic("tenant: crypto/rand: " + err.Error())
	}
	return NewID(PrefixKey), idEncoding.EncodeToString(b)
}

// FormatKey renders the full credential "pk_<id>_<secret>".
func FormatKey(id, secret string) string { return id + "_" + secret }

// ParseKey splits "pk_<id>_<secret>". IDs and secrets never contain '_'
// beyond the prefix, so the last '_' separates them.
func ParseKey(credential string) (id, secret string, ok bool) {
	if !strings.HasPrefix(credential, PrefixKey) {
		return "", "", false
	}
	i := strings.LastIndexByte(credential, '_')
	if i <= len(PrefixKey) || i == len(credential)-1 {
		return "", "", false
	}
	return credential[:i], credential[i+1:], true
}
