// English: These tests reject export roots that overlap artifact storage through filesystem symlink aliases.
// 中文：本测试拒绝通过文件系统符号链接别名与产物存储根重叠的导出目录。
package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileRootsOverlapDetectsSymlinkAliases(t *testing.T) {
	parent := t.TempDir()
	artifact := filepath.Join(parent, "artifacts")
	exportAlias := filepath.Join(parent, "export-alias")
	if err := os.Mkdir(artifact, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(artifact, exportAlias); err != nil {
		t.Fatal(err)
	}
	if overlaps, err := fileRootsOverlap(artifact, exportAlias); err != nil || !overlaps {
		t.Fatal("Artifact and export roots resolving to the same directory were considered separate")
	}
	export := filepath.Join(parent, "published")
	if err := os.Mkdir(export, 0o700); err != nil {
		t.Fatal(err)
	}
	if overlaps, err := fileRootsOverlap(artifact, export); err != nil || overlaps {
		t.Fatal("distinct sibling Artifact/export roots were considered overlapping")
	}
}
