// Package execstack clears the executable-stack request of shared libraries.
// A library whose PT_GNU_STACK program header has the X flag (usually built
// from assembly without a .note.GNU-stack section, not because it runs code
// on the stack) cannot be loaded with dlopen by glibc 2.41 and later, which
// refuse to make the stack executable. Clearing the flag keeps the stack
// non-executable and lets the library load; conda-forge's julia ships such
// a libopenlibm.so.
package execstack

import (
	"bytes"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	ptGNUStack = 0x6474e551
	pfX        = 1
)

// Clear removes the X flag from the PT_GNU_STACK header of every shared
// library (*.so, *.so.*) under dir and returns the ones it changed. A
// changed library is written to a new file renamed over the old one, so a
// hard link to it elsewhere (pixi's package cache) keeps the original.
func Clear(dir string) ([]string, error) {
	var cleared []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if !d.Type().IsRegular() || !(strings.HasSuffix(name, ".so") || strings.Contains(name, ".so.")) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		off, ok := stackFlagsOffset(b)
		if !ok {
			return nil
		}
		order := byteOrder(b)
		flags := order.Uint32(b[off:])
		if flags&pfX == 0 {
			return nil
		}
		order.PutUint32(b[off:], flags&^pfX)
		if err := replace(p, b); err != nil {
			return err
		}
		cleared = append(cleared, p)
		return nil
	})
	return cleared, err
}

func byteOrder(b []byte) binary.ByteOrder {
	if b[5] == 2 {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

// stackFlagsOffset is where the p_flags of b's PT_GNU_STACK header is, when
// b is an ELF file with one.
func stackFlagsOffset(b []byte) (int, bool) {
	if len(b) < 52 || !bytes.Equal(b[:4], []byte("\x7fELF")) || (b[5] != 1 && b[5] != 2) {
		return 0, false
	}
	o := byteOrder(b)
	var phoff uint64
	var phentsize, phnum uint16
	var flagsAt int
	switch b[4] {
	case 2: // ELF64: p_type, p_flags, ...
		if len(b) < 64 {
			return 0, false
		}
		phoff, phentsize, phnum, flagsAt = o.Uint64(b[32:]), o.Uint16(b[54:]), o.Uint16(b[56:]), 4
	case 1: // ELF32: p_type, p_offset, p_vaddr, p_paddr, p_filesz, p_memsz, p_flags
		phoff, phentsize, phnum, flagsAt = uint64(o.Uint32(b[28:])), o.Uint16(b[42:]), o.Uint16(b[44:]), 24
	default:
		return 0, false
	}
	for i := uint64(0); i < uint64(phnum); i++ {
		h := phoff + i*uint64(phentsize)
		if h+uint64(phentsize) > uint64(len(b)) || int(phentsize) < flagsAt+4 {
			return 0, false
		}
		if o.Uint32(b[h:]) == ptGNUStack {
			return int(h) + flagsAt, true
		}
	}
	return 0, false
}

// replace writes b to a new file beside p with p's mode and renames it over p.
func replace(p string, b []byte) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	tmp := p + ".execstack-tmp"
	if err := os.WriteFile(tmp, b, fi.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chmod(tmp, fi.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chtimes(tmp, fi.ModTime(), fi.ModTime()); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
