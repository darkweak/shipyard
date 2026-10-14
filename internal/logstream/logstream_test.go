package logstream

import (
	"reflect"
	"testing"
)

func TestLineWriter(t *testing.T) {
	var got []string
	w := NewLineWriter(func(s string) { got = append(got, s) })
	w.Write([]byte("a\nb"))
	w.Write([]byte("c\r\n50%\r100%\ntail"))
	w.Flush()
	want := []string{"a", "bc", "50%", "100%", "tail"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSubscribeReplaysHistory(t *testing.T) {
	h := NewHub()
	s := h.Open("x")
	s.Write("one")
	hist, ch, unsub := s.Subscribe()
	defer unsub()
	s.Write("two")
	h.Close("x")
	if !reflect.DeepEqual(hist, []string{"one"}) {
		t.Fatalf("history %q", hist)
	}
	if l := <-ch; l != "two" {
		t.Fatalf("got %q", l)
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed")
	}
}
