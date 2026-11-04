package main

import (
	"testing"

	"github.com/pion/stun/v4"
	"github.com/pion/webrtc/v4"
)

func TestBuildICEServersPreservesSecureDefaultPort(t *testing.T) {
	opts, err := parsePeerFlags("offer", []string{"--room", "port-test", "--addr", "127.0.0.1:5353", "--turn", "turns:relay.example", "--user", "user", "--pass", "password"})
	if err != nil {
		t.Fatal(err)
	}
	servers, err := buildICEServers(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || len(servers[0].URLs) != 1 {
		t.Fatalf("ICE servers = %v, want one URL", servers)
	}
	server := servers[0]
	if server.URLs[0] != "turns:relay.example:5349" {
		t.Errorf("URL = %q, want turns:relay.example:5349", server.URLs[0])
	}
	uri, err := stun.ParseURI(server.URLs[0])
	if err != nil {
		t.Fatal(err)
	}
	if uri.Port != 5349 || uri.Scheme != stun.SchemeTypeTURNS || uri.Proto != stun.ProtoTypeTCP {
		t.Errorf("Pion URI = %+v, want secure TCP on port 5349", uri)
	}
	if server.Username != "user" || server.Credential != "password" || server.CredentialType != webrtc.ICECredentialTypePassword {
		t.Error("ICE credentials change")
	}
}
