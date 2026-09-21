package main

import (
	"testing"
	"time"
)

func TestParseSessionTTL(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "空值取默认 12h", raw: "", want: 12 * time.Hour},
		{name: "合法下限 15m", raw: "15m", want: 15 * time.Minute},
		{name: "合法上限 168h", raw: "168h", want: 168 * time.Hour},
		{name: "合法中间值 24h", raw: "24h", want: 24 * time.Hour},
		{name: "低于下限报错", raw: "14m", wantErr: true},
		{name: "高于上限报错", raw: "200h", wantErr: true},
		{name: "无法解析报错", raw: "abc", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSessionTTL(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSessionTTL(%q) 期望报错,实际返回 %v", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSessionTTL(%q) 意外报错: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("parseSessionTTL(%q) = %v, 期望 %v", tc.raw, got, tc.want)
			}
		})
	}
}
