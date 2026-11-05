package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDoctorOutputRedactsProbeErrors(t *testing.T) {
	secret := "password-marker secret-marker v=0 raw-SDP-marker"
	report := probeDoctor(context.Background(), doctorPlan{name: "dynupdate", authenticated: true, transport: &doctorFake{publishErr: errors.New(secret), cleanupErr: errors.New(secret)}}, time.Second, func() string { return "doctor-123" })
	for _, render := range []func(*bytes.Buffer, doctorReport) error{
		func(w *bytes.Buffer, r doctorReport) error { return writeDoctorReport(w, r) },
		func(w *bytes.Buffer, r doctorReport) error { return writeDoctorJSON(w, r) },
	} {
		var out bytes.Buffer
		if err := render(&out, report); err != nil {
			t.Fatal(err)
		}
		for _, marker := range strings.Fields(secret) {
			if strings.Contains(out.String(), marker) {
				t.Fatalf("output contains %q", marker)
			}
		}
	}
}

func TestDoctorRendersVersionedJSON(t *testing.T) {
	for _, success := range []bool{true, false} {
		report := doctorReport{Transport: "dynupdate", Success: success, Checks: []doctorCheck{{Name: "dns_tcp", Success: success, DurationMS: 12, Correction: "private correction"}}}
		var out bytes.Buffer
		if err := writeDoctorJSON(&out, report); err != nil {
			t.Fatal(err)
		}
		want := `{"schema_version":1,"transport":"dynupdate","checks":[{"name":"dns_tcp","success":` + boolJSON(success) + `,"duration_ms":12,"error_code":""}],"success":` + boolJSON(success) + "}\n"
		if out.String() != want {
			t.Fatalf("got %s want %s", &out, want)
		}
	}
	if err := writeDoctorJSON(doctorErrorWriter{}, doctorReport{}); err == nil {
		t.Fatal("ignores output failure")
	}
}

func boolJSON(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestDoctorRendersStageCorrections(t *testing.T) {
	for _, tc := range []struct{ stage, code, hint string }{
		{"dns_udp", "probe_failed", "UDP"},
		{"dns_tcp", "probe_failed", "TCP"},
		{"tls", "probe_failed", "certificate"},
		{"auth", "probe_failed", "credential"},
		{"publication", "deadline_exceeded", "--timeout"},
		{"retrieval", "canceled", "Run doctor again"},
		{"configuration", "configuration", "endpoint"},
		{"publication", "probe_failed", "publish"},
		{"retrieval", "probe_failed", "retrieve"},
		{"cleanup", "probe_failed", "--room doctor-123"},
	} {
		t.Run(tc.stage+tc.code, func(t *testing.T) {
			check := doctorCheck{Name: tc.stage, DurationMS: 7, ErrorCode: tc.code, Correction: "Check the selected endpoint, network access, and configured credentials."}
			if tc.stage == "cleanup" {
				check.Correction = "Run wingman cleanup with the original configuration and --room doctor-123."
			}
			var out bytes.Buffer
			if err := writeDoctorReport(&out, doctorReport{Transport: "dns", Checks: []doctorCheck{check}}); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{tc.stage, "FAIL", "7ms", tc.code, tc.hint} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q in %q", want, &out)
				}
			}
		})
	}
	var out bytes.Buffer
	if err := writeDoctorReport(&out, doctorReport{Transport: "https", Success: true, Checks: []doctorCheck{{Name: "tls", Success: true, DurationMS: 2}}}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "doctor transport=https result=PASS\ntls: PASS (2ms)\n" {
		t.Fatal(out.String())
	}
}

func checkDoctorOutputCLI(t *testing.T, bin string) {
	t.Helper()
	checkDoctorMalformedAddressCLI(t, bin)
	srv, err := startServe()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	for _, tc := range []struct {
		name, addr string
		success    bool
	}{{"reachable", srv.Addr(), true}, {"unreachable", "127.0.0.1:0", false}} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, "doctor", "--addr", tc.addr, "--timeout", "100ms", "--json")
			out, err := cmd.Output()
			var exit *exec.ExitError
			if (err == nil) != tc.success || (err != nil && !errors.As(err, &exit)) {
				t.Fatalf("exit: %v", err)
			}
			var report struct {
				SchemaVersion int `json:"schema_version"`
				Transport     string
				Checks        []struct {
					Name      string
					Success   bool
					ErrorCode string `json:"error_code"`
				}
				Success bool
			}
			if err := json.Unmarshal(out, &report); err != nil {
				t.Fatalf("%s: %v", out, err)
			}
			if report.SchemaVersion != 1 || report.Success != tc.success || report.Transport != "dns" || len(report.Checks) != 4 {
				t.Fatalf("%s", out)
			}
			if report.Checks[0].Name != "dns_udp" || report.Checks[1].Name != "dns_tcp" {
				t.Fatalf("%s", out)
			}
		})
	}
}

func checkDoctorMalformedAddressCLI(t *testing.T, bin string) {
	t.Helper()
	for _, structured := range []bool{false, true} {
		t.Run("malformed-json="+boolJSON(structured), func(t *testing.T) {
			args := []string{"doctor", "--addr", "not-an-endpoint", "--timeout", "100ms"}
			if structured {
				args = append(args, "--json")
			}
			out, err := exec.Command(bin, args...).CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() == 0 {
				t.Fatalf("exit: %v; output: %s", err, out)
			}
			if structured {
				line := bytes.SplitN(out, []byte("\n"), 2)[0]
				var report struct {
					Success bool
					Checks  []struct {
						Name      string
						ErrorCode string `json:"error_code"`
					}
				}
				if err := json.Unmarshal(line, &report); err != nil {
					t.Fatal(err)
				}
				if report.Success || len(report.Checks) != 1 || report.Checks[0].Name != "configuration" || report.Checks[0].ErrorCode != "configuration" {
					t.Fatalf("%s", out)
				}
			} else if !strings.Contains(string(out), "configuration: FAIL") || !strings.Contains(string(out), "HOST:PORT") {
				t.Fatalf("%s", out)
			}
		})
	}
}
