package terminal

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestPTYAcquireRestore(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	original, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	stdin := os.Stdin
	os.Stdin = slave
	defer func() { os.Stdin = stdin }()
	tty, _, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Restore()
	raw, _ := term.GetState(int(slave.Fd()))
	if reflect.DeepEqual(raw, original) {
		t.Fatal("Acquire did not put the terminal in raw mode")
	}
	// Drain the actual terminal output so prompt redraws cannot block the PTY.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 4096)
		for {
			if _, err := master.Read(buf); err != nil {
				return
			}
		}
	}()
	console := NewConsole(slave, slave)
	console.Draw()
	if _, err := master.WriteString("hello 🌍\x1b[200~\nsecond line\x1b[201~\r"); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var submitted string
	for submitted == "" {
		select {
		case input := <-console.Inputs:
			text, ok, quit, _ := console.Handle(input, false)
			if quit {
				t.Fatal("unexpected quit")
			}
			if ok {
				submitted = text
			}
		case <-timer.C:
			t.Fatal("actual terminal input did not submit")
		}
	}
	if submitted != "hello 🌍\nsecond line" {
		t.Fatalf("paste/UTF-8 corrupted: %q", submitted)
	}
	console.Close()
	tty.Restore()
	after, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(after, original) {
		t.Fatalf("terminal attributes not restored: %v", err)
	}
	master.Close()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("PTY reader did not finish")
	}
}
