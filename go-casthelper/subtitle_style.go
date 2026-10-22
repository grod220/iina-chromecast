package main

import "strings"

// The default receiver on this installation accepts caption-window properties
// in status replies but does not draw the window. Keep the native fallback
// visible, with no edge effect when a background is present.
func subtitleStyle(overrides ...map[string]any) map[string]any {
	style := map[string]any{
		"fontScale":                 1.0,
		"fontGenericFamily":         "SANS_SERIF",
		"fontStyle":                 "NORMAL",
		"foregroundColor":           "#FFFFFFFF",
		"backgroundColor":           "#00000099",
		"edgeType":                  "NONE",
		"edgeColor":                 "#00000000",
		"windowType":                "NONE",
		"windowColor":               "#00000000",
		"windowRoundedCornerRadius": 10.0,
	}
	for _, values := range overrides {
		for key, value := range values {
			style[key] = value
		}
	}
	color, _ := style["backgroundColor"].(string)
	if style["windowType"] == "ROUNDED_CORNERS" {
		color, _ = style["windowColor"].(string)
	}
	style["backgroundColor"] = color
	style["windowType"] = "NONE"
	style["windowColor"] = "#00000000"
	style["edgeType"] = "NONE"
	style["edgeColor"] = "#00000000"
	if strings.HasSuffix(color, "00") {
		style["windowType"] = "NONE"
		style["edgeType"] = "OUTLINE"
		style["edgeColor"] = "#000000FF"
	}
	return style
}
