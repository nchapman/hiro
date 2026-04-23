//go:build linux

package landlock

import "testing"

func TestFileAccessMaskDropsDirOnlyBits(t *testing.T) {
	// Full RW on ABI v3 includes directory-only bits (MAKE_*, REMOVE_*,
	// READ_DIR, REFER) plus the ABI v3 TRUNCATE bit. Applied to a regular
	// file, only EXECUTE/READ_FILE/WRITE_FILE/TRUNCATE should survive.
	full := bestAccessMask(3)
	got := fileAccessMask(full, 3)
	want := uint64(accessFsExecute | accessFsReadFile | accessFsWriteFile | accessFsTruncate)
	if got != want {
		t.Fatalf("fileAccessMask(full, abi=3) = %#x, want %#x", got, want)
	}

	// On ABI v1 the TRUNCATE bit isn't supported — it should not appear even
	// if the caller passed it in, because the ruleset never declared it.
	got = fileAccessMask(full, 1)
	want = uint64(accessFsExecute | accessFsReadFile | accessFsWriteFile)
	if got != want {
		t.Fatalf("fileAccessMask(full, abi=1) = %#x, want %#x", got, want)
	}
}

func TestFileAccessMaskReadOnly(t *testing.T) {
	// READ mask (execute+read-file+read-dir). On a file, READ_DIR must drop.
	got := fileAccessMask(accessFsRead, 3)
	want := uint64(accessFsExecute | accessFsReadFile)
	if got != want {
		t.Fatalf("fileAccessMask(read, abi=3) = %#x, want %#x", got, want)
	}
}
