package bitcoind

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// RPCAuth is the rpcauth= value of bitcoin.conf for user and password with
// salt, as Bitcoin Core's share/rpcauth/rpcauth.py makes it:
// user:salt$hex(HMAC-SHA256(key salt, password)), the salt as text.
func RPCAuth(user, password, salt string) string {
	m := hmac.New(sha256.New, []byte(salt))
	m.Write([]byte(password))
	return user + ":" + salt + "$" + hex.EncodeToString(m.Sum(nil))
}

// NewRPCAuth makes a random password and its rpcauth= value, with a
// password and salt as rpcauth.py makes them: 32 random bytes in base64url,
// and 16 random bytes in hex.
func NewRPCAuth(user string) (password, rpcauth string) {
	pw, salt := make([]byte, 32), make([]byte, 16)
	rand.Read(pw) // never fails, see crypto/rand
	rand.Read(salt)
	password = base64.RawURLEncoding.EncodeToString(pw)
	return password, RPCAuth(user, password, hex.EncodeToString(salt))
}
