//go:build windows

package app

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"psi/internal/testutil"
)

const procThreadAttributePseudoConsole = 0x00020016

// TestActualWindowsConPTYInteraction exercises the real interactive entrypoint
// with console input/output. Cross-compilation alone does not cover this path.
func TestActualWindowsConPTYInteraction(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := t.TempDir()
	inRead, inWrite, outRead, outWrite := windows.Handle(0), windows.Handle(0), windows.Handle(0), windows.Handle(0)
	if err := windows.CreatePipe(&inRead, &inWrite, nil, 0); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(inWrite)
	defer func() {
		if inRead != 0 {
			_ = windows.CloseHandle(inRead)
		}
	}()
	if err := windows.CreatePipe(&outRead, &outWrite, nil, 0); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(outRead)
	defer func() {
		if outWrite != 0 {
			_ = windows.CloseHandle(outWrite)
		}
	}()

	var pseudo windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: 80, Y: 24}, inRead, outWrite, 0, &pseudo); err != nil {
		t.Fatalf("CreatePseudoConsole: %v", err)
	}
	defer windows.ClosePseudoConsole(pseudo)
	_ = windows.CloseHandle(inRead)
	inRead = 0
	_ = windows.CloseHandle(outWrite)
	outWrite = 0

	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attributes.Delete()
	if err := attributes.Update(procThreadAttributePseudoConsole, pseudoConsoleValue(pseudo), unsafe.Sizeof(pseudo)); err != nil {
		t.Fatal(err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	appName, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		t.Fatal(err)
	}
	commandLine, err := windows.UTF16PtrFromString(windows.EscapeArg(exe) + " -test.run=^TestAppProcess$")
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := windows.UTF16PtrFromString(work)
	if err != nil {
		t.Fatal(err)
	}
	env := utf16.Encode([]rune(conptyTestEnvironment(home, agentDir)))
	startup := windows.StartupInfoEx{}
	startup.StartupInfo.Cb = uint32(unsafe.Sizeof(startup))
	startup.StartupInfo.Flags = windows.STARTF_USESTDHANDLES
	startup.ProcThreadAttributeList = attributes.List()
	var process windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcess(appName, commandLine, nil, nil, false, flags, &env[0], cwd, &startup.StartupInfo, &process); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process.Thread)
	defer windows.CloseHandle(process.Process)
	defer func() {
		if result, _ := windows.WaitForSingleObject(process.Process, 0); result == uint32(windows.WAIT_TIMEOUT) {
			_ = windows.TerminateProcess(process.Process, 1)
			_, _ = windows.WaitForSingleObject(process.Process, 5000)
		}
	}()

	readChunks := make(chan []byte, 32)
	go func() {
		defer close(readChunks)
		buf := make([]byte, 4096)
		for {
			var n uint32
			err := windows.ReadFile(outRead, buf, &n, nil)
			if n > 0 {
				readChunks <- append([]byte(nil), buf[:n]...)
			}
			if err != nil || n == 0 {
				return
			}
		}
	}()

	screen, raw := testutil.NewVTModel(80, 24), &bytes.Buffer{}
	drain := func() {
		t.Helper()
		for {
			select {
			case chunk, ok := <-readChunks:
				if !ok {
					return
				}
				raw.Write(chunk)
				screen.Feed(chunk)
			default:
				return
			}
		}
	}
	waitFor := func(label string, match func(string) bool) {
		t.Helper()
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			drain()
			if match(screen.Text()) {
				return
			}
			select {
			case chunk, ok := <-readChunks:
				if !ok {
					t.Fatalf("ConPTY closed waiting for %s: %q", label, screen.Text())
				}
				raw.Write(chunk)
				screen.Feed(chunk)
			case <-tick.C:
			case <-deadline.C:
				waitState, _ := windows.WaitForSingleObject(process.Process, 0)
				var exitCode uint32
				_ = windows.GetExitCodeProcess(process.Process, &exitCode)
				t.Fatalf("timeout waiting for %s: screen=%q raw=%q process_wait=%#x exit_code=%d", label, screen.Text(), raw.String(), waitState, exitCode)
			}
		}
	}
	write := func(text string) {
		t.Helper()
		data := []byte(text)
		for len(data) > 0 {
			var n uint32
			if err := windows.WriteFile(inWrite, data, &n, nil); err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				t.Fatal("ConPTY accepted zero input bytes")
			}
			data = data[n:]
		}
	}

	waitFor("inline editor", func(_ string) bool { return semanticEditorReady(screen) })
	// ConPTY may emit its own screen initialization before the child starts.
	// Check only the application's interactive redraws below.
	drain()
	raw.Reset()
	write("abc")
	waitFor("typed draft", func(text string) bool { return strings.Contains(text, "abc") })
	write("\x03")
	waitFor("cleared draft", func(text string) bool { return !strings.Contains(text, "abc") })
	write("/quit\r")
	result := make(chan error, 1)
	go func() {
		code, err := windows.WaitForSingleObject(process.Process, 10_000)
		if err != nil {
			result <- err
			return
		}
		if code != windows.WAIT_OBJECT_0 {
			result <- fmt.Errorf("psi did not exit after /quit (wait result %#x)", code)
			return
		}
		var exitCode uint32
		if err := windows.GetExitCodeProcess(process.Process, &exitCode); err != nil {
			result <- err
		} else if exitCode != 0 {
			result <- fmt.Errorf("psi exited with code %d", exitCode)
		}
		result <- nil
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(12 * time.Second):
		windows.TerminateProcess(process.Process, 1)
		t.Fatal("psi did not settle and exit under ConPTY")
	}
	drain()
	for _, sequence := range [][]byte{[]byte("\x1b[?1049h"), []byte("\x1b[2J")} {
		if at := bytes.Index(raw.Bytes(), sequence); at >= 0 {
			start, end := max(0, at-24), min(raw.Len(), at+len(sequence)+24)
			t.Fatalf("inline session emitted forbidden screen control %q at %d: %q", sequence, at, raw.Bytes()[start:end])
		}
	}
}

func pseudoConsoleValue(handle windows.Handle) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&handle))
}

func conptyTestEnvironment(home, agentDir string) string {
	values := []string{
		"HOME=" + home,
		"LANG=C.UTF-8",
		"PATH=" + os.Getenv("SystemRoot") + `\System32`,
		"PI_CODING_AGENT_DIR=" + agentDir,
		"PI_CODING_AGENT_SESSION_DIR=",
		"PSI_APP_TEST_PROCESS=1",
		"TERM=xterm",
		"USERPROFILE=" + home,
		"SystemRoot=" + os.Getenv("SystemRoot"),
	}
	if temp := os.Getenv("TEMP"); temp != "" {
		values = append(values, "TEMP="+temp, "TMP="+temp)
	}
	sort.Slice(values, func(i, j int) bool { return strings.ToUpper(values[i]) < strings.ToUpper(values[j]) })
	return strings.Join(values, "\x00") + "\x00\x00"
}
