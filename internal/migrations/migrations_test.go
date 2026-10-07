package migrations

import "testing"

func TestLoadMigrationPairs(t *testing.T) {
	scripts, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 5 {
		t.Fatalf("unexpected migration count: %d", len(scripts))
	}
	for i, script := range scripts {
		if script.version != int64(i+1) || script.up == "" || script.down == "" {
			t.Fatalf("unexpected migration script: %+v", script)
		}
	}
}
