package server

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// writeJSONAtomic 以 0600 权限将 v 序列化写入 file。
//
// 先写同目录临时文件再 rename,保证读取方不会看到写了一半的内容;
// 父目录不存在时自动创建。
func writeJSONAtomic(file string, v any) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
