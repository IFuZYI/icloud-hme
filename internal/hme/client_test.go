package hme

import (
	"reflect"
	"testing"
)

func TestValidationURLs(t *testing.T) {
	tests := []struct {
		name string
		host string
		want []string
	}{
		{
			name: "全球账号只用全球端点",
			host: "icloud.com",
			want: []string{"https://setup.icloud.com/setup/ws/1/validate"},
		},
		{
			name: "国区账号回退全球端点",
			host: "icloud.com.cn",
			want: []string{
				"https://setup.icloud.com.cn/setup/ws/1/validate",
				"https://setup.icloud.com/setup/ws/1/validate",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{Host: tt.host}
			if got := client.validationURLs(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("validationURLs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRequestOrigin(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://setup.icloud.com/setup/ws/1/validate", "https://www.icloud.com"},
		{"https://setup.icloud.com.cn/setup/ws/1/validate", "https://www.icloud.com.cn"},
		{"https://p123-maildomainws.icloud.com.cn/v2/hme/list", "https://www.icloud.com.cn"},
		{"https://p123-maildomainws.icloud.com/v2/hme/list", "https://www.icloud.com"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := requestOrigin(tt.url); got != tt.want {
				t.Fatalf("requestOrigin(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}
