package generate

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRetainedExampleIsFresh(t *testing.T) {
	root, e := filepath.Abs("../../examples/users")
	if e != nil {
		t.Fatal(e)
	}
	if e = CheckFresh(context.Background(), root); e != nil {
		t.Fatal(e)
	}
}
