package database_test

import (
	"context"
	"github.com/proxy/proxy-manager/internal/database"
	"path/filepath"
	"testing"
)

func TestMigrationsIdempotent(t *testing.T) {
	s, err := database.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = s.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("expected migrations, got %d", n)
	}
}
