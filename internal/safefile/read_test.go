package safefile

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

func TestReadErrorCategories(t *testing.T) {
	for _, tt := range []struct {
		name  string
		cause error
		want  string
	}{
		{"missing", fs.ErrNotExist, "file does not exist"},
		{"permission", fs.ErrPermission, "permission denied"},
		{"other", errors.New("SECRET backend failure"), "file I/O error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cause := &fs.PathError{Op: "open", Path: "/SECRET/file", Err: fmt.Errorf("SECRET context: %w", tt.cause)}
			err := readError("configuration", cause)
			if err.Error() != "configuration: "+tt.want {
				t.Fatalf("unsafe or incorrect diagnostic: %v", err)
			}
			if tt.name != "other" && !errors.Is(err, tt.cause) {
				t.Fatalf("lost error category: %v", err)
			}
		})
	}
}
