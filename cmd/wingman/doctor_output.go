package main

import (
	"encoding/json"
	"fmt"
	"io"
)

func writeDoctorJSON(w io.Writer, report doctorReport) error {
	type checkJSON struct {
		Name       string `json:"name"`
		Success    bool   `json:"success"`
		DurationMS int64  `json:"duration_ms"`
		ErrorCode  string `json:"error_code"`
	}
	result := struct {
		SchemaVersion int         `json:"schema_version"`
		Transport     string      `json:"transport"`
		Checks        []checkJSON `json:"checks"`
		Success       bool        `json:"success"`
	}{SchemaVersion: 1, Transport: report.Transport, Checks: make([]checkJSON, 0, len(report.Checks)), Success: report.Success}
	for _, check := range report.Checks {
		result.Checks = append(result.Checks, checkJSON{check.Name, check.Success, check.DurationMS, check.ErrorCode})
	}
	if err := json.NewEncoder(w).Encode(result); err != nil {
		return fmt.Errorf("write doctor JSON: %w", err)
	}
	return nil
}

func doctorStatus(success bool) string {
	if success {
		return "PASS"
	}
	return "FAIL"
}

func writeDoctorReport(w io.Writer, report doctorReport) error {
	if _, err := fmt.Fprintf(w, "doctor transport=%s result=%s\n", report.Transport, doctorStatus(report.Success)); err != nil {
		return fmt.Errorf("write doctor summary: %w", err)
	}
	for _, check := range report.Checks {
		if _, err := fmt.Fprintf(w, "%s: %s (%dms)", check.Name, doctorStatus(check.Success), check.DurationMS); err != nil {
			return fmt.Errorf("write doctor stage: %w", err)
		}
		detail := ""
		if !check.Success {
			detail = fmt.Sprintf(" [%s] %s", check.ErrorCode, doctorCorrection(check))
		}
		if _, err := fmt.Fprintln(w, detail); err != nil {
			return fmt.Errorf("write doctor result: %w", err)
		}
	}
	return nil
}

func doctorCorrection(check doctorCheck) string {
	// Cleanup identifies the generated room without copying endpoint or credential input.
	if check.Name == "cleanup" && check.Correction != "" {
		return check.Correction
	}
	switch check.ErrorCode {
	case "deadline_exceeded":
		return "Check endpoint access. Increase --timeout if the service needs more time."
	case "canceled":
		return "Run doctor again when the operation can complete."
	}
	switch check.Name {
	case "dns_udp":
		return "Check the DNS endpoint and permit UDP traffic to its port."
	case "dns_tcp":
		return "Check the DNS endpoint and permit TCP traffic to its port."
	case "tls":
		return "Check the TLS certificate trust, hostname, and expiry."
	case "auth":
		return "Check the configured credential file and the service authentication policy."
	case "publication":
		return "Check endpoint access and the service permission to publish probe data."
	case "retrieval":
		return "Check endpoint access and the service permission to retrieve probe data."
	default:
		return "Check the selected endpoint and credential files."
	}
}
