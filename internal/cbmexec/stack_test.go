package cbmexec

import (
	"reflect"
	"testing"
)

func TestStackTargetNoopAtOrAboveFloor(t *testing.T) {
	for _, cur := range []uint64{stackFloor, stackFloor + 1, 32 << 20, 1 << 30, ^uint64(0)} {
		if want, changed := stackTarget(cur, 1<<62); changed || want != cur {
			t.Fatalf("cur=%d: changed=%v want=%d", cur, changed, want)
		}
	}
}

func TestStackTargetRaisesToFloor(t *testing.T) {
	for _, cur := range []uint64{0, 512 << 10, 4 << 20} {
		want, changed := stackTarget(cur, ^uint64(0))
		if !changed || want != stackFloor {
			t.Fatalf("cur=%d: changed=%v want=%d floor=%d", cur, changed, want, stackFloor)
		}
	}
}

func TestStackTargetCappedByHard(t *testing.T) {
	want, changed := stackTarget(512<<10, 4<<20)
	if !changed || want != 4<<20 {
		t.Fatalf("want=%d changed=%v", want, changed)
	}
}

func TestStackTargetHardTiesSoft(t *testing.T) {
	if want, changed := stackTarget(256<<10, 256<<10); changed || want != 256<<10 {
		t.Fatalf("want=%d changed=%v", want, changed)
	}
}

func TestStackLimitArg(t *testing.T) {
	if got := stackLimitArg(8 << 20); got != "8192" {
		t.Fatalf("bytes->KB: got %q", got)
	}
	if got := stackLimitArg((8 << 20) + 511); got != "8192" {
		t.Fatalf("floor rounding: got %q", got)
	}
	if got := stackLimitArg(^uint64(0)); got != "unlimited" {
		t.Fatalf("infinity: got %q", got)
	}
}

func TestStackShellRaisesBelowFloor(t *testing.T) {
	argv, wrapped := stackShell(512<<10, ^uint64(0), "/bin/fake-cbm", []string{"cli", "index_repository"})
	if !wrapped {
		t.Fatal("expected the wrapper below the floor")
	}
	want := []string{
		"/bin/sh",
		"-c",
		`ulimit -s unlimited && exec "$0" "$@"`,
		"/bin/fake-cbm",
		"cli",
		"index_repository",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv:\n got %q\nwant %q", argv, want)
	}
}

func TestStackShellUsesHardLimitValue(t *testing.T) {
	argv, wrapped := stackShell(512<<10, 4<<20, "/bin/fake-cbm", []string{"cli"})
	if !wrapped {
		t.Fatal("expected the wrapper below the floor")
	}
	if argv[2] != `ulimit -s 4096 && exec "$0" "$@"` {
		t.Fatalf("script got %q", argv[2])
	}
}

func TestStackShellDirectAtFloor(t *testing.T) {
	if argv, wrapped := stackShell(8<<20, ^uint64(0), "/bin/fake-cbm", []string{"cli"}); wrapped || argv != nil {
		t.Fatalf("no-op expected, got %v wrapped=%v", argv, wrapped)
	}
}
