package main

import "testing"

func TestWER(t *testing.T) {
	cases := []struct {
		ref, hyp string
		errs, n  int
	}{
		{"the cat sat", "the cat sat", 0, 3},
		{"THE CAT SAT", "The cat, sat.", 0, 3},
		{"the cat sat", "the dog sat", 1, 3},
		{"the cat sat", "the sat", 1, 3},
		{"the cat sat", "the big cat sat down", 2, 3},
		{"well known", "well-known", 0, 2},
		{"it's", "it’s", 0, 1},
		{"", "", 0, 0},
	}
	for _, c := range cases {
		e, n := werCounts(c.ref, c.hyp)
		if e != c.errs || n != c.n {
			t.Errorf("werCounts(%q,%q) = %d,%d want %d,%d", c.ref, c.hyp, e, n, c.errs, c.n)
		}
	}
	if w := wer("a b c d", "a x c"); w != 0.5 {
		t.Errorf("wer = %v want 0.5", w)
	}
}
