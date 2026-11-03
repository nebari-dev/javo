package execstack

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

const (
	ptLoad   = 1
	pfW, pfR = 2, 4
)

// elf64 is a minimal little-endian ELF64 shared object with a PT_LOAD and a
// PT_GNU_STACK program header, the stack one with flags stack.
func elf64(stack uint32) []byte {
	const ehsize, phentsize = 64, 56
	b := make([]byte, ehsize+2*phentsize)
	copy(b, "\x7fELF")
	b[4], b[5], b[6] = 2, 1, 1 // ELFCLASS64, little-endian, version
	le := binary.LittleEndian
	le.PutUint16(b[16:], 3)  // ET_DYN
	le.PutUint16(b[18:], 62) // x86-64
	le.PutUint32(b[20:], 1)
	le.PutUint64(b[32:], ehsize) // e_phoff
	le.PutUint16(b[52:], ehsize)
	le.PutUint16(b[54:], phentsize)
	le.PutUint16(b[56:], 2) // e_phnum
	le.PutUint32(b[ehsize:], ptLoad)
	le.PutUint32(b[ehsize+4:], pfR|pfX)
	le.PutUint32(b[ehsize+phentsize:], ptGNUStack)
	le.PutUint32(b[ehsize+phentsize+4:], stack)
	return b
}

func stackFlags(t *testing.T, p string) uint32 {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return binary.LittleEndian.Uint32(b[64+56+4:])
}

func ino(t *testing.T, p string) uint64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return uint64(fi.Sys().(*syscall.Stat_t).Ino)
}

// A shared library that asks for an executable stack (glibc 2.41 and later
// refuse to dlopen it) loses only the X bit of its PT_GNU_STACK header;
// libraries that do not ask, other files and the hard-linked original (the
// package cache) stay as they were.
func TestClearDropsTheExecutableStackOfSharedLibraries(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "lib", "julia", "libopenlibm.so")
	versioned := filepath.Join(dir, "lib", "libfoo.so.1.2")
	clean := filepath.Join(dir, "lib", "libok.so")
	cache := filepath.Join(t.TempDir(), "cached-libopenlibm.so")
	if err := os.MkdirAll(filepath.Dir(lib), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, b := range map[string][]byte{cache: elf64(pfR | pfW | pfX), versioned: elf64(pfR | pfW | pfX), clean: elf64(pfR | pfW), filepath.Join(dir, "notes.so"): []byte("not an ELF file")} {
		if err := os.WriteFile(p, b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(cache, lib); err != nil {
		t.Fatal(err)
	}

	cleared, err := Clear(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared) != 2 {
		t.Fatalf("cleared %v, want the two libraries that asked", cleared)
	}
	for _, p := range []string{lib, versioned} {
		if f := stackFlags(t, p); f != pfR|pfW {
			t.Errorf("%s: stack flags %#x, want RW", p, f)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o755 {
			t.Errorf("%s: mode %v", p, fi.Mode())
		}
	}
	if f := stackFlags(t, cache); f != pfR|pfW|pfX {
		t.Error("the hard-linked original in the package cache was changed")
	}
	if ino(t, lib) == ino(t, cache) {
		t.Error("the library still shares its inode with the cache")
	}
	if again, err := Clear(dir); err != nil || len(again) != 0 {
		t.Fatalf("second pass cleared %v, %v", again, err)
	}
}
