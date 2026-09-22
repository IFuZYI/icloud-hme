package srp

import (
	"encoding/hex"
	"hash"
	"math/big"
	"regexp"
)

func padTo(bytes []byte, length int) []byte {
	paddingLength := length - len(bytes)
	if paddingLength <= 0 {
		return bytes
	}
	out := make([]byte, length)
	copy(out[paddingLength:], bytes)
	return out
}

func padToN(number *big.Int, params *SRPParams) []byte {
	return padTo(number.Bytes(), params.NLengthBits/8)
}

func hashToBytes(h hash.Hash) []byte {
	return h.Sum(nil)
}

func hashToInt(h hash.Hash) *big.Int {
	U := new(big.Int)
	U.SetBytes(hashToBytes(h))
	return U
}

func intFromBytes(bytes []byte) *big.Int {
	i := new(big.Int)
	i.SetBytes(bytes)
	return i
}

func intToBytes(i *big.Int) []byte {
	return i.Bytes()
}

// nonHexRE 匹配非十六进制字符;预编译为包级变量,避免每次调用重新编译正则。
var nonHexRE = regexp.MustCompile("[^0-9a-fA-F]")

func bytesFromHexString(s string) []byte {
	h := nonHexRE.ReplaceAll([]byte(s), []byte(""))
	b, _ := hex.DecodeString(string(h))
	return b
}
