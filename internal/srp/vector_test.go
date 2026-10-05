package srp

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"
)

// SRP 回归向量: 固定输入 → 固定输出的确定性断言。
//
// 输入取自 reference/iCloud_Distribution 的同源实现(compare_test.go),
// 用于防止后续改动破坏 SRP 计算(协议错误会导致所有密码登录失败,
// 而这类错误在单元测试之外很难定位)。期望值是当前实现的实际输出,
// 已与上游 JS/Go 实现比对一致。
func TestSRPVectorRegression(t *testing.T) {
	params := GetParams(2048)
	params.NoUserNameInX = true // GSA 模式: x 不含用户名

	aBytes, err := hex.DecodeString("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	if err != nil {
		t.Fatal(err)
	}
	client := NewSRPClient(params, aBytes)

	passKey, err := hex.DecodeString("a1a2a3a4a5a6a7a8a9b0b1b2b3b4b5b6b7b8b9c0c1c2c3c4c5c6c7c8c9d0d1d2")
	if err != nil {
		t.Fatal(err)
	}
	salt, err := base64.StdEncoding.DecodeString("KiacjYK6Wb2Jt8R8TVaHfg==")
	if err != nil {
		t.Fatal(err)
	}
	B, err := base64.StdEncoding.DecodeString("ZMYMUDlFB7O5kXRVXJWjHLY7s2o5BrGWZ8BIH4YSBuAyClvB2wW2a1O6eWPTRqzJk0JTLZcJ0hFaFz4NNPti/RwJKUQhGlZ7aXLK1smWQYjAiK0LAo7L0PbNywk1vA26K64H1iqQaKq5CHBNfIe/qRW1A9fRZZJypMbBQmlOfosaG0hjriF+5IkaGg2E053d6xUv3deEJJD/XG+9Taw8ObRm7JAExvalenS12pDxbBSvezGmmYE+sIjjqnoNEfU/uuqIwPwDkzUxUw15GyPhr8ziLuszowGeKOR1sXoxgKrL68oZuBIYQ0oztljGEZhqIks0kQ1t8XVVe+7knG7Hlw==")
	if err != nil {
		t.Fatal(err)
	}

	client.ProcessClientChanllenge([]byte("testuser@example.com"), passKey, salt, B)

	// 固定输入的确定性断言。期望值由本实现输出并与上游同源实现
	// (reference/iCloud_Distribution compare_test.go) 的算法比对确认;
	// 任何改动破坏 SRP 计算都会在这里立即暴露。
	const (
		wantA  = "630acdff5d334462d92a29e0b7fa6e20020f3333292f6d3a640f1c7a76ad9d317531c57979952e5736c88db118d060dc0539a812b9b0af3b4002380a9f28ae4a7c45a896542de05fbcf76a4e7e0739b9a55d5d6c7aba4f1e1b58729a79bc084d5ff513eaec33ce978f5bad87e579b5a95fc773198e22697b2eadab9eb94f84cdcf1fe94ff09f88d4ca46e968bba443ff71167571f19feb052869bd28d7dabf963b7fe399a1f70e7e08d00e1a3778ed1dddc3325dd09e05d31e774d1fd295c4abfbc613446232004d67cb03d6a034d2ce6ca0a544a0ff5b434b4b4267fa6c6d72acbbda2efc1ef1d1fe36d35382b089abe556862aec35b29d3d0cdf359a9cfed3"
		wantM1 = "4d2e4151252fcd854c8da62a4bfd1b618bd4d0296bf0c6c24bfbe4e7e57f18b4"
		wantM2 = "28a2bafd511b409caf2fef5491bcc4460abd737bc7f90c8f28ccac62e40fdf50"
	)
	if got := hex.EncodeToString(client.A.Bytes()); got != wantA {
		t.Fatalf("A = %s, want %s", got, wantA)
	}
	if got := hex.EncodeToString(client.M1); got != wantM1 {
		t.Fatalf("M1 = %s, want %s", got, wantM1)
	}
	if got := hex.EncodeToString(client.M2); got != wantM2 {
		t.Fatalf("M2 = %s, want %s", got, wantM2)
	}
}

// 同输入必须完全确定(无隐藏随机源), 且不同密码产生不同 M1。
func TestSRPDeterministicAndPasswordSensitive(t *testing.T) {
	params := GetParams(2048)
	params.NoUserNameInX = true

	aBytes := make([]byte, 32)
	for i := range aBytes {
		aBytes[i] = byte(i + 1)
	}
	salt := []byte("0123456789abcdef")
	B := make([]byte, 256)
	for i := range B {
		B[i] = byte(0xAB)
	}

	compute := func(passKeyHex string) (m1 string) {
		passKey, _ := hex.DecodeString(passKeyHex)
		c := NewSRPClient(params, append([]byte(nil), aBytes...))
		c.ProcessClientChanllenge([]byte("user@example.com"), passKey, salt, B)
		return fmt.Sprintf("%x", c.M1)
	}

	keyA := "a1a2a3a4a5a6a7a8a9b0b1b2b3b4b5b6b7b8b9c0c1c2c3c4c5c6c7c8c9d0d1d2"
	keyB := "b1b2b3b4b5b6b7b8b9c0c1c2c3c4c5c6c7c8c9d0d1d2d3d4d5d6d7d8d9e0e1e2"

	if compute(keyA) != compute(keyA) {
		t.Fatal("同一输入两次计算结果不同 — 存在隐藏随机源")
	}
	if compute(keyA) == compute(keyB) {
		t.Fatal("不同密码密钥应产生不同 M1")
	}
}

// 未知位数 panic 是可接受的行为(参数表只支持固定几组),但要固定住行为,
// 避免静默返回错误参数导致协议错误。
func TestGetParamsUnknownSizePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("未知位数应 panic")
		}
	}()
	_ = GetParams(1234)
}

// 已知位数返回非空参数。
func TestGetParamsKnownSizes(t *testing.T) {
	for _, bits := range []int{1024, 1536, 2048, 4096} {
		p := GetParams(bits)
		if p == nil || p.N == nil || p.N.Sign() <= 0 {
			t.Fatalf("GetParams(%d) 返回无效参数", bits)
		}
	}
}
