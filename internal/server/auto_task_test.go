package server

import (
	"sync"
	"testing"
	"time"

	"icloud-hme/internal/hme"
)

type taskBackend struct {
	fakeBackend
	mu     sync.Mutex
	labels []string
}

func (f *taskBackend) CreateAlias(_ string, label string) (*hme.CreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labels = append(f.labels, label)
	return &hme.CreateResult{Email: label}, nil
}
func TestNormalizeAliasTaskValidation(t *testing.T) {
	in := aliasTaskInput{Enabled: true, AccountID: "a", IntervalMinutes: 60, BatchCount: 5, MaxTotal: 999, LabelPrefix: "注册"}
	if _, e := normalizeAliasTask(in); e != nil {
		t.Fatal(e)
	}
	in.MaxTotal = 1000
	if _, e := normalizeAliasTask(in); e == nil {
		t.Fatal("max_total=1000 should fail")
	}
}
func TestAutoTaskRunStopsAtTotal(t *testing.T) {
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	task, e := m.create(aliasTaskInput{Enabled: false, AccountID: "a", IntervalMinutes: 60, BatchCount: 5, MaxTotal: 3, LabelPrefix: "注册"})
	if e != nil {
		t.Fatal(e)
	}
	m.mu.Lock()
	task.Enabled = true
	m.tasks[task.ID] = task
	m.mu.Unlock()
	out := m.runOnce(task.ID)
	if len(be.labels) != 3 || be.labels[0] != "注册001" || be.labels[2] != "注册003" {
		t.Fatalf("labels=%v", be.labels)
	}
	if out.CreatedCount != 3 || out.Enabled != true {
		t.Fatalf("task=%+v", out)
	}
	m.runOnce(task.ID)
	if len(be.labels) != 3 {
		t.Fatal("task should stop at max_total")
	}
}
func TestAutoTaskStartDoesNotDuplicate(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	task, e := m.create(aliasTaskInput{Enabled: true, AccountID: "a", IntervalMinutes: 1, BatchCount: 1, MaxTotal: 1, LabelPrefix: "x"})
	if e != nil {
		t.Fatal(e)
	}
	m.start()
	m.start()
	time.Sleep(5 * time.Millisecond)
	m.close()
	if task.ID == "" {
		t.Fatal("missing id")
	}
}
