package main

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testSSHServer struct {
	hostname string
	addr     string
}

var testHosts = []testSSHServer{
	{"remote1", "remote1:2222"},
	{"remote2", "remote2:2222"},
	{"remote3", "remote3:2222"},
}

func must(err error, msg string) {
	if err == nil {
		return
	}
	panic(fmt.Errorf("%s: %s", msg, err))
}

const testUserName = "testuser"

func launchGoSSHa() {
	initialize(true)
	go runProxy()
}

func TestMain(m *testing.M) {
	code := func() int {
		rand.Seed(time.Now().UnixNano())
		tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("gossha-test-%d", rand.Int()))
		sshDir := filepath.Join(tmpDir, ".ssh")

		idRsaFile, err := os.Open("./docker/test_key")
		must(err, "")
		idRsaPubFile, err := os.Open("./docker/test_key.pub")
		must(err, "")
		idRsa, err := io.ReadAll(idRsaFile)
		must(err, "")
		idRsaPub, err := io.ReadAll(idRsaPubFile)
		must(err, "")

		must(os.MkdirAll(sshDir, 0700), "Could not create temp dir")
		must(os.WriteFile(filepath.Join(sshDir, "id_rsa"), idRsa, 0600), "Could not write test private key")
		must(os.WriteFile(filepath.Join(sshDir, "id_rsa.pub"), idRsaPub, 0600), "Could not write test public key")
		defer os.RemoveAll(tmpDir)

		os.Setenv("LOGNAME", testUserName)
		os.Setenv("HOME", tmpDir)
		os.Setenv("SSH_AUTH_SOCK", "")

		disconnectAfterUse = false
		maxConnections = 0

		launchGoSSHa()

		return m.Run()
	}()

	os.Exit(code)
}

type testResult struct {
	hosts     map[string]testSSHServer
	hostsLeft map[string]struct{}
	replies   map[string]*Reply
}

func makeTestResult() *testResult {
	return &testResult{
		hostsLeft: make(map[string]struct{}),
		hosts:     make(map[string]testSSHServer),
		replies:   make(map[string]*Reply),
	}
}

func waitReply(t *testing.T, r *testResult, timeout time.Duration) {
	timeoutCh := time.After(timeout)

	for {
		select {
		case reply := <-repliesChan:
			switch reply := reply.(type) {
			case *FinalReply:
				return
			case *Reply:
				_, ok := r.hostsLeft[reply.Hostname]
				if !ok {
					t.Fatalf("Got reply for unknown host: %s", reply.Hostname)
				}
				delete(r.hostsLeft, reply.Hostname)

				r.replies[reply.Hostname] = reply
			}
		case <-timeoutCh:
			t.Fatalf("Timed out, hosts left: %#v", r.hostsLeft)
		}
	}
}

func makeProxyRequest(timeout time.Duration) *ProxyRequest {
	return &ProxyRequest{
		Action:  "ssh",
		Cmd:     "hostname",
		Timeout: uint64(timeout / time.Millisecond),
	}
}

func checkSuccess(t *testing.T, r *testResult) {
	for _, reply := range r.replies {
		srv := r.hosts[reply.Hostname]

		if strings.Trim(reply.Stdout, "\n") != srv.hostname {
			t.Fatalf("Expected '%s', got '%s' in stdout", srv.hostname, strings.Trim(reply.Stdout, "\n"))
		}

		if reply.Stderr != "" {
			t.Fatalf("Expected '', got '%s' in stderr", reply.Stderr)
		}
	}

	if len(r.hostsLeft) != 0 {
		t.Fatalf("Extra servers left: %#v", r.hostsLeft)
	}
}

func TestBasic(t *testing.T) {
	r := makeTestResult()

	for _, srv := range testHosts {
		r.hosts[srv.addr] = srv
		r.hostsLeft[srv.addr] = struct{}{}
	}

	req := makeProxyRequest(maxTimeout / 2)

	for h := range r.hostsLeft {
		req.Hosts = append(req.Hosts, h)
	}

	requestsChan <- req

	waitReply(t, r, maxTimeout)
	checkSuccess(t, r)
}

func TestDisconnectAfterUse(t *testing.T) {
	disconnectAfterUse = true
	TestBasic(t)
}

func TestLimitConcurrency(t *testing.T) {
	maxConnections = 1
	maxAnsweredServers := len(testHosts)
	minAnsweredServers := len(testHosts)

	r := makeTestResult()

	for _, srv := range testHosts {
		r.hosts[srv.addr] = srv
		r.hostsLeft[srv.addr] = struct{}{}
	}

	req := makeProxyRequest(time.Second)

	for h := range r.hostsLeft {
		req.Hosts = append(req.Hosts, h)
	}

	requestsChan <- req

	waitReply(t, r, 2*time.Second)

	answeredServers := len(r.hosts) - len(r.hostsLeft)

	if answeredServers < minAnsweredServers {
		t.Fatalf("Too few servers responded: got %d, expected %d", answeredServers, minAnsweredServers)
	}

	if answeredServers > maxAnsweredServers {
		t.Fatalf("Too many servers responded: got %d, expected %d", answeredServers, maxAnsweredServers)
	}
}

func TestFileUpload(t *testing.T) {
	r := makeTestResult()

	for _, srv := range testHosts {
		r.hosts[srv.addr] = srv
		r.hostsLeft[srv.addr] = struct{}{}
	}

	content := fmt.Sprintf("%d", time.Now().Unix())
	targetPath := filepath.Join("/tmp", fmt.Sprintf("upload-%d.txt", time.Now().Unix()))
	sourcePath := filepath.Join(os.TempDir(), fmt.Sprintf("upload-%d.txt", time.Now().Unix()))
	os.WriteFile(sourcePath, []byte(content), 0666)

	req := &ProxyRequest{
		Action: "scp",
		Source: sourcePath,
		Target: targetPath,
	}
	for h := range r.hostsLeft {
		req.Hosts = append(req.Hosts, h)
	}

	requestsChan <- req

	waitReply(t, r, maxTimeout)

	r = makeTestResult()
	for _, srv := range testHosts {
		r.hosts[srv.addr] = srv
		r.hostsLeft[srv.addr] = struct{}{}
	}

	req = &ProxyRequest{
		Action: "ssh",
		Cmd:    "cat " + targetPath,
	}
	for h := range r.hostsLeft {
		req.Hosts = append(req.Hosts, h)
	}

	requestsChan <- req

	waitReply(t, r, maxTimeout)
	for _, reply := range r.replies {
		if reply.Stdout != content {
			t.Fatalf("Expected '%s', got '%s' in stdout", content, reply.Stdout)
		}

		if reply.Stderr != "" {
			t.Fatalf("Expected '', got '%s' in stderr", reply.Stderr)
		}
	}

	if len(r.hostsLeft) != 0 {
		t.Fatalf("Extra servers left: %#v", r.hostsLeft)
	}
}
