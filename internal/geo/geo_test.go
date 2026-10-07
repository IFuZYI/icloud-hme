package geo

import (
	"strings"
	"testing"
	"time"
)

func TestParseIPWhoIs(t *testing.T) {
	body := []byte(`{"ip":"1.2.3.4","success":true,"country":"United States","timezone":{"id":"America/Denver","abbr":"MDT"}}`)
	tz, err := parseIPWhoIs(body)
	if err != nil || tz != "America/Denver" {
		t.Fatalf("parseIPWhoIs = %q, %v", tz, err)
	}
}

func TestParseIPWhoIsRejectsFailure(t *testing.T) {
	if _, err := parseIPWhoIs([]byte(`{"success":false,"message":"reserved range"}`)); err == nil {
		t.Fatal("success=false 应报错")
	}
	if _, err := parseIPWhoIs([]byte(`not json`)); err == nil {
		t.Fatal("非 JSON 应报错")
	}
	if _, err := parseIPWhoIs([]byte(`{"success":true}`)); err == nil {
		t.Fatal("缺时区应报错")
	}
}

func TestParseIPInfo(t *testing.T) {
	tz, err := parseIPInfo([]byte(`{"ip":"1.2.3.4","city":"Denver","timezone":"America/Denver"}`))
	if err != nil || tz != "America/Denver" {
		t.Fatalf("parseIPInfo = %q, %v", tz, err)
	}
	if _, err := parseIPInfo([]byte(`{"ip":"1.2.3.4"}`)); err == nil {
		t.Fatal("缺时区应报错")
	}
}

func TestParseIPAPI(t *testing.T) {
	tz, err := parseIPAPI([]byte(`{"status":"success","timezone":"Asia/Shanghai"}`))
	if err != nil || tz != "Asia/Shanghai" {
		t.Fatalf("parseIPAPI = %q, %v", tz, err)
	}
	if _, err := parseIPAPI([]byte(`{"status":"fail","message":"reserved range"}`)); err == nil || !strings.Contains(err.Error(), "reserved range") {
		t.Fatalf("失败响应应携带服务端消息, got %v", err)
	}
	if _, err := parseIPAPI([]byte(`{"status":"fail"}`)); err == nil {
		t.Fatal("无消息的失败应报错")
	}
}

func TestTimezoneNamesLoadable(t *testing.T) {
	// 三个解析器返回的名字必须是 time.LoadLocation 可加载的 IANA 名称。
	for _, tz := range []string{"America/Denver", "Asia/Shanghai", "Europe/London", "UTC"} {
		if _, err := time.LoadLocation(tz); err != nil {
			t.Fatalf("时区 %s 应可加载: %v", tz, err)
		}
	}
}

func TestHostOfHidesCredentials(t *testing.T) {
	got := hostOf("https://user:secret@example.com:8080/path")
	if got != "example.com" || strings.Contains(got, "secret") || strings.Contains(got, "user") {
		t.Fatalf("hostOf 泄露凭据: %q", got)
	}
}
