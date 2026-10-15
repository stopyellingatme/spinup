package server

import (
	"crypto/ecdh"
	"encoding/binary"
	"math/big"
	"strings"
)

// publicKeyWire rejects truncated keys before bootstrap changes SSH access.
func publicKeyWire(algorithm string, data []byte) bool {
	take := func() []byte {
		if len(data) < 4 {
			data = nil
			return nil
		}
		size := binary.BigEndian.Uint32(data)
		data = data[4:]
		if uint64(size) > uint64(len(data)) {
			data = nil
			return nil
		}
		value := data[:size]
		data = data[size:]
		return value
	}
	if string(take()) != algorithm {
		return false
	}
	switch algorithm {
	case "ssh-ed25519", "sk-ssh-ed25519@openssh.com":
		if len(take()) != 32 {
			return false
		}
	case "ssh-rsa":
		exponent, modulus := take(), take()
		if len(exponent) == 0 || len(modulus) == 0 || exponent[0]&0x80 != 0 || modulus[0]&0x80 != 0 {
			return false
		}
		if new(big.Int).SetBytes(exponent).Cmp(big.NewInt(1)) <= 0 || new(big.Int).SetBytes(modulus).Sign() == 0 {
			return false
		}
	default:
		curveName := string(take())
		var curve ecdh.Curve
		switch curveName {
		case "nistp256":
			curve = ecdh.P256()
		case "nistp384":
			curve = ecdh.P384()
		case "nistp521":
			curve = ecdh.P521()
		default:
			return false
		}
		if algorithm != "ecdsa-sha2-"+curveName && (algorithm != "sk-ecdsa-sha2-nistp256@openssh.com" || curveName != "nistp256") {
			return false
		}
		if _, err := curve.NewPublicKey(take()); err != nil {
			return false
		}
	}
	if strings.HasPrefix(algorithm, "sk-") && len(take()) == 0 {
		return false
	}
	return len(data) == 0
}
