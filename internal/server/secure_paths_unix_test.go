//go:build unix

package server

import (
	"os"
	"testing"
)

func TestWritableByUntrustedDirectoryPolicy(t *testing.T) {
	for _, test := range []struct {
		name string
		mode os.FileMode
		gid  uint32
		want bool
	}{
		{name: "root group write is privileged", mode: 0o775, gid: 0, want: false},
		{name: "non-root group write is untrusted", mode: 0o775, gid: 1000, want: true},
		{name: "world write remains untrusted for root group", mode: 0o777, gid: 0, want: true},
		{name: "ordinary directory is trusted", mode: 0o755, gid: 1000, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := writableByUntrusted(test.mode, test.gid); got != test.want {
				t.Fatalf("writableByUntrusted(%#o, %d) = %v, want %v", test.mode.Perm(), test.gid, got, test.want)
			}
		})
	}
}
