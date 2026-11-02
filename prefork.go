package main

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// master is the PHP process of -prefork: it boots the application once and forks the workers,
// which share its OPcache and, until they write to it, its memory. Each worker connects to a Unix
// socket the gateway listens on; the master replaces the ones that exit. Its stdin takes commands
// ("kill <pid>"), and closing it stops the master and its workers.
type master struct {
	cmd      *exec.Cmd
	stdin    *os.File
	listener net.Listener
	exited   chan struct{}
	stopping bool
	mu       sync.Mutex
}

func (p *pool) startMaster() error {
	cfg := p.cfg
	// In the upload directory: PHP must see it (the PHP command may be a container)
	socket := filepath.Join(cfg.uploadDir, fmt.Sprintf("phpworker-%d.sock", os.Getpid()))
	command, err := cfg.phpCommand(socket, strconv.Itoa(cfg.workers))
	if err != nil {
		return err
	}
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		listener.Close()
		return err
	}
	cmd := exec.Command(command[0], command[1:]...)
	// The workers answer on their connections: anything on stdout is stray output
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, os.Stderr, os.Stderr
	err = cmd.Start()
	stdinR.Close()
	if err != nil {
		stdinW.Close()
		listener.Close()
		return err
	}
	m := &master{cmd: cmd, stdin: stdinW, listener: listener, exited: make(chan struct{})}
	p.master = m
	go func() {
		_ = cmd.Wait()
		close(m.exited)
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.stopping {
			// Its workers keep serving, but no one would replace them
			log.Fatal("the PHP master process exited; restart the gateway")
		}
	}()

	accepted := make(chan *worker)
	go m.accept(cfg, accepted)

	timeout := time.After(cfg.bootTimeout)
	for range cfg.workers {
		select {
		case w := <-accepted:
			p.idle <- w
		case <-m.exited:
			p.close()
			return errors.New("starting PHP master: it exited before its workers were ready")
		case <-timeout:
			p.close()
			return errors.New("starting PHP master: timed out waiting for its workers")
		}
	}
	// From now on, the workers the master forks to replace others join the pool
	go func() {
		for w := range accepted {
			if p.closing.Load() {
				w.in.Close()
				continue
			}
			p.idle <- w
		}
	}()
	return nil
}

// accept turns every connection into a worker once it says it is ready
func (m *master) accept(cfg config, accepted chan<- *worker) {
	defer close(accepted)
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			return // closed by stop()
		}
		reader := bufio.NewReader(conn)
		var msg struct {
			Ready bool `json:"ready"`
			Pid   int  `json:"pid"`
		}
		_ = conn.SetReadDeadline(time.Now().Add(cfg.bootTimeout))
		err = readFrame(reader, &msg)
		_ = conn.SetReadDeadline(time.Time{})
		if err != nil || !msg.Ready {
			log.Printf("a forked PHP worker did not start: %v", err)
			conn.Close()
			continue
		}
		pid := msg.Pid
		accepted <- &worker{in: conn, out: conn, reader: reader, kill: func() { m.command(fmt.Sprintf("kill %d", pid)) }}
	}
}

func (m *master) command(line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.stopping {
		_, _ = m.stdin.WriteString(line + "\n")
	}
}

// stop closes the master's stdin: it stops its workers and exits. It is killed after stopGrace.
func (m *master) stop() {
	m.mu.Lock()
	m.stopping = true
	m.stdin.Close()
	m.mu.Unlock()
	select {
	case <-m.exited:
	case <-time.After(stopGrace):
		_ = m.cmd.Process.Kill()
		<-m.exited
	}
	m.listener.Close()
	_ = os.Remove(m.listener.Addr().String())
}
