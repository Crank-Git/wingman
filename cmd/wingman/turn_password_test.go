package main

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/turn/v5"
)

func turnPasswordFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTURNPasswordFilesRejectInvalidSources(t *testing.T) {
	valid := turnPasswordFile(t, "disposable-token", 0600)
	for _, tc := range []struct {
		name, direct, path, want string
	}{
		{"conflict", "direct-token", valid, "conflicting TURN password sources"},
		{"permissions", "", turnPasswordFile(t, "disposable-token", 0644), "credential file"},
		{"missing", "", filepath.Join(t.TempDir(), "missing"), "credential file"},
		{"empty", "", turnPasswordFile(t, " \n", 0600), "invalid TURN password file"},
		{"space", "", turnPasswordFile(t, "disposable token", 0600), "invalid TURN password file"},
		{"control", "", turnPasswordFile(t, "disposable\x00token", 0600), "invalid TURN password file"},
		{"unicode", "", turnPasswordFile(t, "disposable\u2003token", 0600), "invalid TURN password file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			// An invalid listener exposes credential checks that occur too late.
			err := runServe(ctx, []string{"--local-demo", "--listen", "invalid", "--turn-pass", tc.direct, "--turn-pass-file", tc.path})
			checkTURNPasswordError(t, err, tc.want)
			for _, name := range []string{"offer", "answer"} {
				_, err := parsePeerFlags(name, []string{"--room", "room", "--addr", "invalid", "--pass", tc.direct, "--pass-file", tc.path})
				checkTURNPasswordError(t, err, tc.want)
			}
		})
	}
}

func checkTURNPasswordError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want %s", err, want)
	}
	for _, secret := range []string{"disposable", "direct-token"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("error contains a password")
		}
	}
}

func TestTURNDirectPasswordsRetainValues(t *testing.T) {
	for _, password := range []string{"", " \tprevious password\x00\n", "ordinary"} {
		opts, err := parsePeerFlags("offer", []string{"--room", "room", "--addr", "127.0.0.1:1", "--pass", password})
		if err != nil || opts.pass != password {
			t.Fatalf("direct password changes: %v", err)
		}
	}
}

func TestTURNPasswordFileCLIAllocatesWithoutSecretOutput(t *testing.T) {
	for _, source := range []string{"direct", "file"} {
		t.Run(source, func(t *testing.T) {
			const password = "disposable-token"
			path := turnPasswordFile(t, " \t"+password+"\r\n", 0600)
			serverFlag, serverValue := "--turn-pass", password
			peerFlag, peerValue := "--pass", password
			if source == "file" {
				serverFlag, serverValue = "--turn-pass-file", path
				peerFlag, peerValue = "--pass-file", path
			}
			address, output := startTURNPasswordCLI(t, serverFlag, serverValue)
			for _, name := range []string{"offer", "answer"} {
				opts, err := parsePeerFlags(name, []string{"--room", "room", "--addr", "127.0.0.1:1", "--turn", "turn:" + address, "--user", "disposable-user", peerFlag, peerValue})
				if err != nil {
					t.Fatal(err)
				}
				servers, err := buildICEServers(opts)
				if err != nil || len(servers) != 1 {
					t.Fatalf("ICE servers: %v", err)
				}
				if servers[0].Credential != password || servers[0].Username != "disposable-user" {
					t.Fatal("ICE credentials differ from the configured credentials")
				}
				allocateTURNPassword(t, address, servers[0].Username, servers[0].Credential.(string))
			}
			for _, secret := range []string{password, "disposable-user"} {
				if strings.Contains(output, secret) {
					t.Fatal("startup output contains a credential")
				}
			}
			if !strings.Contains(output, "--pass-file <PATH>") {
				t.Fatal("startup output omits the file option")
			}
		})
	}
}

func startTURNPasswordCLI(t *testing.T, flag, value string) (string, string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = writer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, []string{"--local-demo", "--turn-server", "127.0.0.1:0", "--turn-user", "disposable-user", flag, value})
		_ = writer.Close()
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		os.Stdout = previous
		_ = reader.Close()
	})
	scanner := bufio.NewScanner(reader)
	var output strings.Builder
	var address string
	for scanner.Scan() {
		line := scanner.Text()
		output.WriteString(line + "\n")
		if strings.HasPrefix(line, "turn-server listening on ") {
			address = strings.TrimPrefix(line, "turn-server listening on ")
		}
		if strings.Contains(line, "  relay:") {
			return address, output.String()
		}
	}
	t.Fatalf("TURN startup produces no relay hint: %v", scanner.Err())
	return "", ""
}

func allocateTURNPassword(t *testing.T, address, username, password string) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client, err := turn.NewClient(&turn.ClientConfig{Conn: conn, STUNServerAddr: address, TURNServerAddr: address, Username: username, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Listen(); err != nil {
		t.Fatal(err)
	}
	relay, err := client.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTURNPasswordFileHelpNamesBothFlags(t *testing.T) {
	var output strings.Builder
	usage(&output)
	for _, flag := range []string{"--turn-pass-file PATH", "--pass-file PATH"} {
		if !strings.Contains(output.String(), flag) {
			t.Errorf("help omits %s", flag)
		}
	}
}
