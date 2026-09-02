package buildapp

import "testing"

func TestParseLaunchHit(t *testing.T) {
	got, err := ParseLaunchHit(`{"found":true,"text":"rocket_launch Launch!","tag":"P","inShadow":true,"x":100,"y":200,"w":120,"h":40,"cx":160,"cy":220}`)
	if err != nil {
		t.Fatalf("ParseLaunchHit() error = %v", err)
	}
	if !got.Found || !got.InShadow {
		t.Fatalf("ParseLaunchHit() flags = %#v", got)
	}
	if got.Text != "rocket_launch Launch!" || got.Tag != "P" {
		t.Fatalf("ParseLaunchHit() element = %#v", got)
	}
	if got.CX != 160 || got.CY != 220 || got.W <= 1 || got.H <= 1 {
		t.Fatalf("ParseLaunchHit() geometry = %#v", got)
	}
}

func TestParseLaunchHitRejectsMissingHit(t *testing.T) {
	for _, raw := range []string{"", `{"found":false}`} {
		if _, err := ParseLaunchHit(raw); err == nil {
			t.Fatalf("ParseLaunchHit(%q) expected an error", raw)
		}
	}
}
