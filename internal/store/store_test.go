package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "footman.db")
	s, err := Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, p
}

func TestMigrateIdempotent(t *testing.T) {
	ctx := context.Background()
	s, p := open(t)
	v, _ := s.SchemaVersion(ctx)
	if v != 1 {
		t.Fatalf("version %d", v)
	}
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode %q %v", mode, err)
	}
	_ = s.Close()
	s2, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	var n int
	_ = s2.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n)
	if n != 1 {
		t.Fatalf("migrations recorded %d", n)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	err := applyMigrations(ctx, s.db, []migration{
		{1, "a", ""},
		{2, "bad", `CREATE TABLE x (a); SELECT nope FROM missing;`},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if v, _ := s.SchemaVersion(ctx); v != 1 {
		t.Fatalf("version %d", v)
	}
	if _, err := s.db.Exec(`SELECT * FROM x`); err == nil {
		t.Fatal("table x should have been rolled back")
	}
}

func TestHistoryJobsMemoryKV(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	if _, err := s.AddHistory(ctx, "transcript", "hello"); err != nil {
		t.Fatal(err)
	}
	_, _ = s.AddHistory(ctx, "command", "open mail")
	h, _ := s.RecentHistory(ctx, "", 10)
	if len(h) != 2 || h[0].Text != "open mail" {
		t.Fatalf("%+v", h)
	}
	if h, _ = s.RecentHistory(ctx, "transcript", 10); len(h) != 1 {
		t.Fatalf("%+v", h)
	}

	if err := s.CreateJob(ctx, "j1", "code-edit", "queued", "in"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateJob(ctx, "j1", "failed", "", "boom"); err != nil {
		t.Fatal(err)
	}
	j, err := s.GetJob(ctx, "j1")
	if err != nil || j.Status != "failed" || j.Error != "boom" || j.Input != "in" {
		t.Fatalf("%+v %v", j, err)
	}
	if err := s.UpdateJob(ctx, "nope", "x", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.GetJob(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if l, _ := s.ListJobs(ctx, "failed"); len(l) != 1 {
		t.Fatal(l)
	}

	_ = s.Remember(ctx, "pet", "cat")
	_ = s.Remember(ctx, "pet", "dog")
	f, _ := s.Facts(ctx)
	if len(f) != 1 || f[0].Value != "dog" {
		t.Fatalf("%+v", f)
	}
	if err := s.Forget(ctx, "pet"); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(ctx, "pet"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}

	if _, err := s.GetKV(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	_ = s.SetKV(ctx, "k", "1")
	_ = s.SetKV(ctx, "k", "2")
	if v, _ := s.GetKV(ctx, "k"); v != "2" {
		t.Fatal(v)
	}
	_ = s.DeleteKV(ctx, "k")
	if _, err := s.GetKV(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestActionLogAppendOnly(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	id, err := s.AppendAction(ctx, ActionEntry{Actor: "alex", Action: "send", Tool: "mail", Args: `{"k":"[redacted]"}`, Result: "ok", Confirmation: "voice", Reason: "asked"})
	if err != nil || id == 0 {
		t.Fatal(id, err)
	}
	if _, err := s.db.Exec(`UPDATE action_log SET result='x'`); err == nil {
		t.Fatal("update should be rejected")
	}
	if _, err := s.db.Exec(`DELETE FROM action_log`); err == nil {
		t.Fatal("delete should be rejected")
	}
	a, _ := s.RecentActions(ctx, 10)
	if len(a) != 1 || a[0].Tool != "mail" || a[0].Confirmation != "voice" {
		t.Fatalf("%+v", a)
	}
}

func TestPrune(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	base := time.Now()
	s.now = func() time.Time { return base.Add(-100 * 24 * time.Hour) }
	_, _ = s.AddHistory(ctx, "transcript", "old")
	_, _ = s.AppendAction(ctx, ActionEntry{Actor: "a", Action: "old", Tool: "t"})
	s.now = func() time.Time { return base }
	_, _ = s.AddHistory(ctx, "transcript", "new")
	_, _ = s.AppendAction(ctx, ActionEntry{Actor: "a", Action: "new", Tool: "t"})

	day := 24 * time.Hour
	p, err := s.Prune(ctx, 30*day, 30*day)
	if err != nil || p.History != 1 || p.ActionLog != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	if a, _ := s.RecentActions(ctx, 10); len(a) != 1 || a[0].Action != "new" {
		t.Fatalf("%+v", a)
	}
	if h, _ := s.RecentHistory(ctx, "", 10); len(h) != 1 || h[0].Text != "new" {
		t.Fatalf("%+v", h)
	}
	// Gate is closed again afterwards.
	if _, err := s.db.Exec(`DELETE FROM action_log`); err == nil {
		t.Fatal("delete should be rejected after prune")
	}
	// Zero ages leave data alone.
	if p, _ := s.Prune(ctx, 0, 0); p != (Pruned{}) {
		t.Fatalf("%+v", p)
	}
}
