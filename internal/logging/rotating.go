package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// rotatingWriter 是按大小滚动的日志写入器,线程安全,无外部依赖。
//
// 当当前文件写满 maxSize 字节后,把 app.log 依次改名为 app.log.1 … app.log.N
// (N=maxBackups),丢弃最旧的一份,再打开新的 app.log 继续写。maxBackups 为 0
// 时不保留历史,滚动即清空当前文件。
type rotatingWriter struct {
	mu         sync.Mutex
	dir        string
	name       string
	path       string
	maxSize    int64
	maxBackups int
	size       int64
	f          *os.File
}

// newRotatingWriter 创建滚动写入器。会创建目录(0o755),并以追加方式打开当前文件,
// size 从已有文件大小续算,使重启后不会立刻误触发滚动。
func newRotatingWriter(dir, name string, maxSize int64, maxBackups int) (*rotatingWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %w", err)
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开日志文件失败: %w", err)
	}
	var size int64
	if info, statErr := f.Stat(); statErr == nil {
		size = info.Size()
	}
	return &rotatingWriter{
		dir:        dir,
		name:       name,
		path:       path,
		maxSize:    maxSize,
		maxBackups: maxBackups,
		size:       size,
		f:          f,
	}, nil
}

// Write 实现 io.Writer。写入前若超过大小上限则先滚动。
func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.f == nil {
		return 0, fmt.Errorf("日志文件已关闭")
	}
	if w.maxSize > 0 && w.size+int64(len(p)) > w.maxSize && w.size > 0 {
		if err := w.rotateLocked(); err != nil {
			// 滚动失败不丢日志:继续写当前文件,避免因改名失败而静默丢弃。
			return w.f.Write(p)
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotateLocked 关闭当前文件,滚动历史文件并重新打开新文件。须持锁调用。
func (w *rotatingWriter) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	if w.maxBackups > 0 {
		// 丢弃最旧一份,其余依次后移:app.log.(N-1) → app.log.N。
		oldest := fmt.Sprintf("%s.%d", w.path, w.maxBackups)
		_ = os.Remove(oldest)
		for i := w.maxBackups - 1; i >= 1; i-- {
			src := fmt.Sprintf("%s.%d", w.path, i)
			dst := fmt.Sprintf("%s.%d", w.path, i+1)
			_ = os.Rename(src, dst)
		}
		_ = os.Rename(w.path, w.path+".1")
	} else {
		// 不保留历史:直接删除当前文件。
		_ = os.Remove(w.path)
	}

	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// 打不开新文件:尝试恢复旧文件句柄,避免后续写入 panic。
		if reopened, reopenErr := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); reopenErr == nil {
			w.f = reopened
		}
		return err
	}
	w.f = f
	w.size = 0
	return nil
}

// Close 关闭底层文件。
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
