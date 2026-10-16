// Command cbshim is the entrypoint of conductor's browser container: it
// starts Chromium (with its own sandbox) and carries everything between it
// and conductor over the container's stdin/stdout — the DevTools pipe,
// each connection Chromium makes to its proxy address, and file transfers
// in its transfer directory. The container has no network and no mount of
// the host; this stream is its only way out.
//
// It is built into the browser image from this one file (standard library
// only) and speaks the frames of internal/browser/mux.go:
//
//	type(1) channel(4) length(4) payload
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const (
	frameData  = 1
	frameOpen  = 2
	frameClose = 3
	frameFile  = 4
	chanCDP    = 0
	chanFile   = 1
)

var (
	wmu sync.Mutex
	out = bufio.NewWriterSize(os.Stdout, 64<<10)
)

func send(typ byte, ch uint32, p []byte) error {
	var h [9]byte
	h[0] = typ
	binary.BigEndian.PutUint32(h[1:5], ch)
	binary.BigEndian.PutUint32(h[5:9], uint32(len(p)))
	wmu.Lock()
	defer wmu.Unlock()
	if _, err := out.Write(h[:]); err != nil {
		return err
	}
	if _, err := out.Write(p); err != nil {
		return err
	}
	return out.Flush()
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18080", "the proxy address Chromium is given")
	xfer := flag.String("xfer", "", "the transfer directory (files in and out)")
	flag.Parse()
	argv := flag.Args()
	if len(argv) == 0 || *xfer == "" {
		fmt.Fprintln(os.Stderr, "cbshim: usage: cbshim --xfer DIR [--listen ADDR] -- chromium ARGS...")
		os.Exit(2)
	}
	xferDir := filepath.Clean(*xfer)
	if err := os.MkdirAll(filepath.Join(xferDir, "dl"), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "cbshim:", err)
		os.Exit(1)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cbshim: listen:", err)
		os.Exit(1)
	}
	cmdR, cmdW, _ := os.Pipe()
	repR, repW, _ := os.Pipe()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.ExtraFiles = []*os.File{cmdR, repW}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "cbshim: start:", err)
		os.Exit(1)
	}
	cmdR.Close()
	repW.Close()
	exited := make(chan int, 1)
	go func() {
		code := 0
		if err := cmd.Wait(); err != nil {
			code = 1
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
		exited <- code
	}()
	// DevTools replies → channel 0.
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, err := repR.Read(buf)
			if n > 0 && send(frameData, chanCDP, buf[:n]) != nil {
				return
			}
			if err != nil {
				_ = send(frameClose, chanCDP, nil)
				return
			}
		}
	}()
	var cmu sync.Mutex
	conns := map[uint32]net.Conn{}
	next := uint32(chanFile)
	// Chromium's proxy connections → a channel each.
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			cmu.Lock()
			next++
			ch := next
			conns[ch] = c
			cmu.Unlock()
			if send(frameOpen, ch, nil) != nil {
				return
			}
			go func() {
				buf := make([]byte, 32<<10)
				for {
					n, err := c.Read(buf)
					if n > 0 && send(frameData, ch, buf[:n]) != nil {
						break
					}
					if err != nil {
						break
					}
				}
				cmu.Lock()
				_, live := conns[ch]
				delete(conns, ch)
				cmu.Unlock()
				c.Close()
				if live {
					_ = send(frameClose, ch, nil)
				}
			}()
		}
	}()
	// conductor → here.
	go func() {
		in := bufio.NewReaderSize(os.Stdin, 1<<20)
		var put *os.File
		var h [9]byte
		for {
			if _, err := io.ReadFull(in, h[:]); err != nil {
				break
			}
			typ, ch, n := h[0], binary.BigEndian.Uint32(h[1:5]), binary.BigEndian.Uint32(h[5:9])
			if n > 16<<20 {
				break
			}
			p := make([]byte, n)
			if _, err := io.ReadFull(in, p); err != nil {
				break
			}
			switch {
			case ch == chanCDP && typ == frameData:
				if _, err := cmdW.Write(p); err != nil {
					break
				}
			case ch == chanCDP && typ == frameClose:
				cmdW.Close()
			case ch == chanFile && typ == frameFile:
				put = fileRequest(xferDir, p)
			case ch == chanFile && typ == frameData && put != nil:
				if _, err := put.Write(p); err != nil {
					put.Close()
					put = nil
					status("error: " + err.Error())
				}
			case ch == chanFile && typ == frameClose && put != nil:
				err := put.Close()
				put = nil
				if err != nil {
					status("error: " + err.Error())
				} else {
					status("ok")
				}
			case typ == frameData:
				cmu.Lock()
				c := conns[ch]
				cmu.Unlock()
				if c != nil {
					_, _ = c.Write(p)
				}
			case typ == frameClose:
				cmu.Lock()
				c := conns[ch]
				delete(conns, ch)
				cmu.Unlock()
				if c != nil {
					c.Close()
				}
			}
		}
		// conductor is gone: so is the browser.
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}()
	os.Exit(<-exited)
}

// status ends a transfer.
func status(s string) {
	_ = send(frameFile, chanFile, []byte(s))
	_ = send(frameClose, chanFile, nil)
}

// within confines a transfer to the transfer directory: a clean relative
// path below it, no symlink anywhere on the way.
func within(dir, p string) (string, error) {
	c := filepath.Clean(p)
	if !strings.HasPrefix(c, dir+string(filepath.Separator)) {
		return "", fmt.Errorf("outside the transfer directory")
	}
	for d := c; d != dir; d = filepath.Dir(d) {
		if fi, err := os.Lstat(d); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("a symlink on the way")
		}
	}
	return c, nil
}

// fileRequest serves a get at once, or opens a put's file (its data
// follows); nil when nothing is open.
func fileRequest(dir string, p []byte) *os.File {
	var req struct {
		Op   string `json:"op"`
		Path string `json:"path"`
		Max  int64  `json:"max"`
	}
	if err := json.Unmarshal(p, &req); err != nil {
		status("error: bad request")
		return nil
	}
	path, err := within(dir, req.Path)
	if err != nil {
		status("error: " + err.Error())
		return nil
	}
	switch req.Op {
	case "get":
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			status("error: " + err.Error())
			return nil
		}
		defer f.Close()
		buf := make([]byte, 32<<10)
		var total int64
		for {
			n, err := f.Read(buf)
			if n > 0 {
				total += int64(n)
				if req.Max > 0 && total > req.Max {
					status("error: over the size limit")
					return nil
				}
				if send(frameData, chanFile, buf[:n]) != nil {
					return nil
				}
			}
			if err != nil {
				break
			}
		}
		status("ok")
		return nil
	case "put":
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			status("error: " + err.Error())
			return nil
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			status("error: " + err.Error())
			return nil
		}
		return f
	}
	status("error: unknown op")
	return nil
}
