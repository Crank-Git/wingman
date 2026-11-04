//go:build bindintegration

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/dns"
	mdns "github.com/miekg/dns"
)

func TestBINDDoctorConsumesProbe(t *testing.T) {
	opts := doctorOptions{peerOpts: peerOpts{signaling: "dynupdate", addr: os.Getenv("WINGMAN_BIND_ADDR"), zone: "example.test", dnsTSIGKeyFile: os.Getenv("WINGMAN_BIND_KEY")}, timeout: 5 * time.Second}
	plan, err := buildDoctorPlan(opts)
	if err != nil {
		t.Fatal(err)
	}
	room := doctorRoom()
	report := probeDoctor(context.Background(), plan, opts.timeout, func() string { return room })
	if !report.Success {
		t.Fatalf("%+v", report)
	}
	tr := plan.transport.(*dns.DynUpdate)
	for _, kind := range []string{"offer", "answer"} {
		query := new(mdns.Msg)
		query.SetQuestion(room+"."+kind+".sig.example.test.", mdns.TypeTXT)
		response, err := tr.Resolver.Exchange(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Answer) != 0 {
			t.Fatal("doctor leaves probe records")
		}
	}
	command := exec.Command(os.Getenv("WINGMAN_BIND_CLI"), "doctor", "--signaling", "dynupdate", "--addr", opts.addr, "--zone", opts.zone, "--dns-tsig-key-file", opts.dnsTSIGKeyFile)
	if output, err := command.CombinedOutput(); err != nil || !strings.Contains(string(output), "retrieval: PASS") {
		t.Fatalf("doctor CLI: %s %v", output, err)
	}
}

func TestBINDDoctorRejectsAuthentication(t *testing.T) {
	key, err := dns.LoadTSIGKey(os.Getenv("WINGMAN_BIND_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wrong.json")
	if err := os.WriteFile(path, []byte(`{"name":"`+key.Name+`","algorithm":"`+key.Algorithm+`","secret":"d3Jvbmc="}`), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Getenv("WINGMAN_BIND_CLI"), "doctor", "--signaling", "dynupdate", "--addr", os.Getenv("WINGMAN_BIND_ADDR"), "--zone", "example.test", "--dns-tsig-key-file", path, "--timeout", "1s")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "auth: FAIL") || strings.Contains(string(output), "d3Jvbmc=") {
		t.Fatalf("authentication: %s %v", output, err)
	}
}
