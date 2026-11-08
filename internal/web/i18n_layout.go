package web

// texts of the page frame: the side drawer of narrow screens, which also holds the side
// menu there (merged into texts)
var layoutTexts = map[string]map[string]string{
	"en": {
		"menu_open":   "Menu",
		"menu_close":  "Close menu",
		"lv_empty_m":  "Tap to choose a camera",
		"lv_tile_m":   "Tile #%d (%s) — tap an empty tile to choose a camera from the menu; double-tap enlarges",
		"pb_empty_m":  "Tap to choose a camera",
		"pb_tile_m":   "Tile #%d (%s) — tap an empty tile to choose a camera from the menu; double-tap enlarges",
		"pz_zoom_s":   "Zoom",
		"lv_zoomed_m": "Tile #%d (%s) enlarged — swipe sideways or tap ‹ › for the next camera; double-tap goes back",
		"pb_zoomed_m": "Tile #%d (%s) enlarged — swipe sideways or tap ‹ › for the next camera; double-tap goes back",
		"lv_prev_cam": "Previous camera",
		"lv_next_cam": "Next camera",
	},
	"pl": {
		"menu_open":   "Menu",
		"menu_close":  "Zamknij menu",
		"lv_empty_m":  "Dotknij, aby wybrać kamerę",
		"lv_tile_m":   "Kafelek #%d (%s) — dotknij pustego kafelka, aby wybrać kamerę z menu; podwójne dotknięcie powiększa",
		"pb_empty_m":  "Dotknij, aby wybrać kamerę",
		"pb_tile_m":   "Kafelek #%d (%s) — dotknij pustego kafelka, aby wybrać kamerę z menu; podwójne dotknięcie powiększa",
		"pz_zoom_s":   "Zoom",
		"lv_zoomed_m": "Kafelek #%d (%s) powiększony — przesuń w bok lub dotknij ‹ ›, aby przejść do następnej kamery; podwójne dotknięcie wraca",
		"pb_zoomed_m": "Kafelek #%d (%s) powiększony — przesuń w bok lub dotknij ‹ ›, aby przejść do następnej kamery; podwójne dotknięcie wraca",
		"lv_prev_cam": "Poprzednia kamera",
		"lv_next_cam": "Następna kamera",
	},
	"de": {
		"menu_open":   "Menü",
		"menu_close":  "Menü schließen",
		"lv_empty_m":  "Tippen, um eine Kamera zu wählen",
		"lv_tile_m":   "Kachel #%d (%s) — leere Kachel antippen, um eine Kamera aus dem Menü zu wählen; doppelt tippen vergrößert",
		"pb_empty_m":  "Tippen, um eine Kamera zu wählen",
		"pb_tile_m":   "Kachel #%d (%s) — leere Kachel antippen, um eine Kamera aus dem Menü zu wählen; doppelt tippen vergrößert",
		"pz_zoom_s":   "Zoom",
		"lv_zoomed_m": "Kachel #%d (%s) vergrößert — zur Seite wischen oder ‹ › antippen für die nächste Kamera; doppelt tippen kehrt zurück",
		"pb_zoomed_m": "Kachel #%d (%s) vergrößert — zur Seite wischen oder ‹ › antippen für die nächste Kamera; doppelt tippen kehrt zurück",
		"lv_prev_cam": "Vorherige Kamera",
		"lv_next_cam": "Nächste Kamera",
	},
}

func init() {
	for code, m := range layoutTexts {
		for k, v := range m {
			texts[code][k] = v
		}
	}
}
