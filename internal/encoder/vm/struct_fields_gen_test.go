package vm_test

import (
	"os/exec"
	"testing"
)

func TestStructFieldsGenInSync(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping codegen check in -short mode")
	}
	cmd := exec.Command("go", "run", "gen_struct_fields.go", "-check")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("struct_fields_gen.go out of sync: %v\n%s", err, string(out))
	}
}
