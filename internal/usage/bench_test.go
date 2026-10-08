package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkUpdateCold(b *testing.B) {
	acct := os.Getenv("USAGE_BENCH_ACCT")
	if acct == "" {
		b.Skip("USAGE_BENCH_ACCT not set")
	}
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	opts := Options{Accounts: []Account{{Name: "bench", Dir: acct}}, Now: now}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		x := New()
		if err := x.Update(context.Background(), opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUpdateWarm(b *testing.B) {
	acct := os.Getenv("USAGE_BENCH_ACCT")
	if acct == "" {
		b.Skip("USAGE_BENCH_ACCT not set")
	}
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	opts := Options{Accounts: []Account{{Name: "bench", Dir: acct}}, Now: now}

	x := New()
	if err := x.Update(context.Background(), opts); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := x.Update(context.Background(), opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSave(b *testing.B) {
	acct := os.Getenv("USAGE_BENCH_ACCT")
	if acct == "" {
		b.Skip("USAGE_BENCH_ACCT not set")
	}
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	opts := Options{Accounts: []Account{{Name: "bench", Dir: acct}}, Now: now}

	x := New()
	if err := x.Update(context.Background(), opts); err != nil {
		b.Fatal(err)
	}
	dir := b.TempDir()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := x.Save(filepath.Join(dir, "usage.json")); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRepoUses(b *testing.B) {
	acct := os.Getenv("USAGE_BENCH_ACCT")
	if acct == "" {
		b.Skip("USAGE_BENCH_ACCT not set")
	}
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	opts := Options{Accounts: []Account{{Name: "bench", Dir: acct}}, Now: now}

	x := New()
	if err := x.Update(context.Background(), opts); err != nil {
		b.Fatal(err)
	}
	repos, err := filepath.Glob("/home/dns/git/*")
	if err != nil {
		b.Fatal(err)
	}
	repos = append(repos, "/home/dns/git")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		x.RepoUses(repos, Window{Days: 30, Now: now})
	}
}
