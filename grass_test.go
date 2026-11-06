package main

import "testing"

func TestParseMessage(t *testing.T) {
	raw := `{"subject":"structs.player.1-1","amount":9007199254740993, "stub":true}`
	m, err := parseMessage(raw)
	if err != nil || m.subject != "structs.player.1-1" || string(m.body) != raw {
		t.Fatalf("valid notification changed: %v, %s", err, m.body)
	}
	for _, bad := range []string{`{"subject":`, `{}`, `{"subject":12}`, `{"subject":"a.*"}`,
		`{"subject":">"}`, `{"subject":"a..b"}`, `{"subject":"a\r\nb"}`, `{"subject":".a"}`, `{"subject":"a."}`} {
		if _, err := parseMessage(bad); err == nil {
			t.Errorf("accepted invalid notification %s", bad)
		}
	}
}
