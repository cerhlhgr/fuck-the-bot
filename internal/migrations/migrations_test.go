package migrations

import "testing"

func TestLoadMigrationPairs(t *testing.T) {
	scripts, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 4 || scripts[0].version != 1 || scripts[1].version != 2 || scripts[2].version != 3 || scripts[3].version != 4 || scripts[0].up == "" || scripts[0].down == "" || scripts[1].up == "" || scripts[1].down == "" || scripts[2].up == "" || scripts[2].down == "" || scripts[3].up == "" || scripts[3].down == "" {
		t.Fatalf("unexpected migration scripts: %+v", scripts)
	}
}
