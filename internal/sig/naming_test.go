package sig

import "testing"

func TestKindStringRoundTrip(t *testing.T) {
	for _, k := range []Kind{Offer, Answer} {
		parsed, err := ParseKind(k.String())
		if err != nil {
			t.Fatalf("ParseKind(%q): %v", k, err)
		}
		if parsed != k {
			t.Fatalf("ParseKind(%q) = %d, want %d", k, parsed, k)
		}
	}
}

func TestParseKindInvalid(t *testing.T) {
	if _, err := ParseKind("nope"); err == nil {
		t.Fatal("expected error for invalid kind")
	}
}

func TestPollName(t *testing.T) {
	got := PollName("example.com", "room1", Offer)
	want := "room1.offer.sig.example.com"
	if got != want {
		t.Fatalf("PollName = %q, want %q", got, want)
	}
	// PollName omits the seq/total and payload labels that a fragment-write
	// name carries, so it must differ from RendezvousName.
	if got == RendezvousName("example.com", "room1", Offer, 0, 1, "payload") {
		t.Fatal("PollName should omit seq/total and payload labels")
	}
}

func TestInvalidKindString(t *testing.T) {
	// An out-of-range Kind has no DNS label; String reports the gap.
	if got := Kind(99).String(); got != "" {
		t.Fatalf("Kind(99).String() = %q, want empty", got)
	}
}

func TestRendezvousNameRoundTrip(t *testing.T) {
	zone := "example.com"
	room := "abc123token"
	payload := "znxferb"
	name := RendezvousName(zone, room, Offer, 3, 17, payload)

	parsed, err := ParseRendezvousName(name)
	if err != nil {
		t.Fatalf("ParseRendezvousName: %v", err)
	}
	if parsed.Zone != zone || parsed.Room != room || parsed.Kind != Offer ||
		parsed.Seq != 3 || parsed.Total != 17 || parsed.Payload != payload {
		t.Fatalf("round-trip mismatch: %+v", parsed)
	}
}

func TestRendezvousNameAnswer(t *testing.T) {
	name := RendezvousName("sig.test", "room1", Answer, 0, 1, "abcdef")
	parsed, err := ParseRendezvousName(name)
	if err != nil {
		t.Fatalf("ParseRendezvousName: %v", err)
	}
	if parsed.Kind != Answer {
		t.Fatalf("kind = %d, want Answer", parsed.Kind)
	}
}

func TestParseRendezvousNameTooShort(t *testing.T) {
	if _, err := ParseRendezvousName("room.payload.offer.sig"); err == nil {
		t.Fatal("expected error for name with too few fields")
	}
}

func TestParseRendezvousNameMissingSig(t *testing.T) {
	// A name that skips the "sig" signaling label entirely.
	name := "room.0-1.payload.offer.example.com"
	if _, err := ParseRendezvousName(name); err == nil {
		t.Fatal("expected error when sig label is missing")
	}
}

func TestParseRendezvousNameBadSeq(t *testing.T) {
	name := "room.notanumber-2.payload.offer.sig.example.com"
	if _, err := ParseRendezvousName(name); err == nil {
		t.Fatal("expected error for non-numeric sequence")
	}
}

func TestParseRendezvousNameBadTotal(t *testing.T) {
	name := "room.2-notanumber.payload.offer.sig.example.com"
	if _, err := ParseRendezvousName(name); err == nil {
		t.Fatal("expected error for non-numeric total")
	}
}

func TestParseRendezvousNameSeqNotLessThanTotal(t *testing.T) {
	name := "room.5-2.payload.offer.sig.example.com"
	if _, err := ParseRendezvousName(name); err == nil {
		t.Fatal("expected error when seq >= total")
	}
}

// TestParseRendezvousNameBadKind exercises the ParseKind failure inside
// ParseRendezvousName (the kind label is present but unknown).
func TestParseRendezvousNameBadKind(t *testing.T) {
	name := "room.0-1.payload.bogus.sig.example.com"
	if _, err := ParseRendezvousName(name); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

// TestParseRendezvousNameSeqNoDash exercises the missing dash in the
// sequence/total label (e.g. "5" instead of "0-1").
func TestParseRendezvousNameSeqNoDash(t *testing.T) {
	name := "room.5.payload.offer.sig.example.com"
	if _, err := ParseRendezvousName(name); err == nil {
		t.Fatal("expected error when seq label has no dash")
	}
}
