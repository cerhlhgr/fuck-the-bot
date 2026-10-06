package migrations

import "testing"

func TestLoadMigrationPairs(t *testing.T) {
	scripts, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 1 || scripts[0].version != 1 || scripts[0].up == "" || scripts[0].down == "" {
		t.Fatalf("unexpected migration scripts: %+v", scripts)
	}
}
