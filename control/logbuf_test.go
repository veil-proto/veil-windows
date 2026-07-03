package control

import "testing"

func TestLogBufferAppendAndSince(t *testing.T) {
	b := NewLogBuffer(10)

	l0 := b.Append("line 0")
	l1 := b.Append("line 1")
	l2 := b.Append("line 2")

	if l0.Seq != 1 || l1.Seq != 2 || l2.Seq != 3 {
		t.Fatalf("expected sequential 1-based Seq 1,2,3 got %d,%d,%d", l0.Seq, l1.Seq, l2.Seq)
	}

	all := b.Since(0)
	if len(all) != 3 {
		t.Fatalf("Since(0) = %d lines, want 3", len(all))
	}

	fromOne := b.Since(l0.Seq)
	if len(fromOne) != 2 || fromOne[0].Msg != "line 1" {
		t.Fatalf("Since(l0.Seq) = %+v, want [line 1, line 2]", fromOne)
	}

	none := b.Since(l2.Seq)
	if len(none) != 0 {
		t.Fatalf("Since(l2.Seq) = %+v, want empty", none)
	}
}

func TestLogBufferEvictsOldestAtCapacity(t *testing.T) {
	b := NewLogBuffer(3)
	for i := 0; i < 5; i++ {
		b.Append("line")
	}
	all := b.Since(0)
	if len(all) != 3 {
		t.Fatalf("retained %d lines, want 3 (capacity)", len(all))
	}
	// The retained lines should be the last 3 appended: Seq 3, 4, 5 (1-based).
	if all[0].Seq != 3 || all[2].Seq != 5 {
		t.Fatalf("unexpected retained sequence range: first=%d last=%d", all[0].Seq, all[2].Seq)
	}
}

func TestLogBufferWriteSplitsLines(t *testing.T) {
	b := NewLogBuffer(10)
	n, err := b.Write([]byte("alpha\nbeta\ngamma\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len("alpha\nbeta\ngamma\n") {
		t.Fatalf("Write returned n=%d, want %d", n, len("alpha\nbeta\ngamma\n"))
	}
	got := b.Since(0)
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3: %+v", len(got), got)
	}
	want := []string{"alpha", "beta", "gamma"}
	for i, w := range want {
		if got[i].Msg != w {
			t.Fatalf("line %d = %q, want %q", i, got[i].Msg, w)
		}
	}
}

func TestLogBufferWriteIgnoresEmptyLines(t *testing.T) {
	b := NewLogBuffer(10)
	b.Write([]byte("\n\nonly-line\n\n"))
	got := b.Since(0)
	if len(got) != 1 || got[0].Msg != "only-line" {
		t.Fatalf("got %+v, want single 'only-line'", got)
	}
}

func TestNewLogBufferDefaultCapacity(t *testing.T) {
	b := NewLogBuffer(0)
	if b.cap != defaultLogCapacity {
		t.Fatalf("cap = %d, want default %d", b.cap, defaultLogCapacity)
	}
}
