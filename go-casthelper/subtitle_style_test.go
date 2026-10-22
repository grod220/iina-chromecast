package main

import "testing"

func TestUnsupportedCaptionWindowFallsBackToVisibleBackground(t *testing.T) {
	style := subtitleStyle(map[string]any{"fontScale": 1.1, "backgroundColor": "#00000000", "windowType": "ROUNDED_CORNERS", "windowColor": "#00000080", "edgeType": "OUTLINE"})
	if style["backgroundColor"] != "#00000080" || style["windowColor"] != "#00000000" || style["windowType"] != "NONE" || style["edgeType"] != "NONE" {
		t.Fatal("unsupported window styling would hide the background or add a text edge effect", style)
	}
	if style["fontScale"] != 1.1 {
		t.Fatal("changing background reset the user's size")
	}
}

func TestPartialSubtitleAppearanceRetainsReadableDefaults(t *testing.T) {
	initial := subtitleStyle()
	large := subtitleStyle(initial, map[string]any{"fontScale": 1.25})
	if large["foregroundColor"] != "#FFFFFFFF" || large["backgroundColor"] != "#00000099" || large["edgeType"] != "NONE" || large["fontGenericFamily"] != "SANS_SERIF" || large["windowType"] != "NONE" {
		t.Fatal("changing size lost the readable caption style", large)
	}
	transparent := subtitleStyle(large, map[string]any{"backgroundColor": "#00000000"})
	if transparent["fontScale"] != 1.25 || transparent["backgroundColor"] != "#00000000" || transparent["windowType"] != "NONE" || transparent["edgeType"] != "OUTLINE" {
		t.Fatal("explicit user appearance was not preserved", transparent)
	}
	if initial["fontScale"] != 1.0 || large["backgroundColor"] != "#00000099" {
		t.Fatal("appearance changes mutated a previous snapshot")
	}
}
