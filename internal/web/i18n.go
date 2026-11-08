package web

// The texts of the panel; the language comes from the lang cookie (the selector of the
// sign in page), else from the browser.
var texts = map[string]map[string]string{
	"en": {
		"tagline": "Central management for video recorders", "language": "Language",
		"username": "Username", "password": "Password", "forgot": "Forgot password?",
		"forgot_help": "Passwords are reset by the administrator (/user set <name> password=...).",
		"signin":      "Sign in", "clear": "Clear", "show_pwd": "Show or hide password",
		"no_rights": "This account has no rights in the panel (policy= of /user).",
		"bad_login": "Wrong username or password.", "blocked": "Too many failed attempts. Try again in a minute.",
		"signed_in_as": "Signed in as", "signout": "Sign out", "recorders": "Recorders",
		"no_recorders": "No recorder is assigned to your account (valid-users of /nvr).",
		"cameras":      "cameras", "camera": "Camera", "channel": "Channel", "alias": "Alias", "state": "State",
		"on": "enabled", "off": "disabled", "local": "local",
	},
	"pl": {
		"tagline": "Centralne zarządzanie rejestratorami wideo", "language": "Język",
		"username": "Nazwa użytkownika", "password": "Hasło", "forgot": "Nie pamiętasz hasła?",
		"forgot_help": "Hasła zmienia administrator (/user set <nazwa> password=...).",
		"signin":      "Zaloguj", "clear": "Wyczyść", "show_pwd": "Pokaż lub ukryj hasło",
		"no_rights": "To konto nie ma żadnych uprawnień w panelu (policy= w /user).",
		"bad_login": "Nieprawidłowa nazwa użytkownika lub hasło.", "blocked": "Zbyt wiele nieudanych prób. Spróbuj za minutę.",
		"signed_in_as": "Zalogowano jako", "signout": "Wyloguj", "recorders": "Rejestratory",
		"no_recorders": "Do Twojego konta nie przypisano żadnego rejestratora (valid-users w /nvr).",
		"cameras":      "kamery", "camera": "Kamera", "channel": "Kanał", "alias": "Alias", "state": "Stan",
		"on": "włączona", "off": "wyłączona", "local": "lokalny",
	},
	"de": {
		"tagline": "Zentrale Verwaltung für Videorekorder", "language": "Sprache",
		"username": "Benutzername", "password": "Passwort", "forgot": "Passwort vergessen?",
		"forgot_help": "Passwörter setzt der Administrator zurück (/user set <name> password=...).",
		"signin":      "Anmelden", "clear": "Leeren", "show_pwd": "Passwort anzeigen oder verbergen",
		"no_rights": "Dieses Konto hat keine Rechte im Panel (policy= von /user).",
		"bad_login": "Falscher Benutzername oder falsches Passwort.", "blocked": "Zu viele Fehlversuche. Bitte in einer Minute erneut versuchen.",
		"signed_in_as": "Angemeldet als", "signout": "Abmelden", "recorders": "Rekorder",
		"no_recorders": "Ihrem Konto ist kein Rekorder zugeordnet (valid-users von /nvr).",
		"cameras":      "Kameras", "camera": "Kamera", "channel": "Kanal", "alias": "Alias", "state": "Status",
		"on": "aktiv", "off": "inaktiv", "local": "lokal",
	},
}

var languages = []struct{ Code, Name string }{{"en", "English"}, {"pl", "Polski"}, {"de", "Deutsch"}}
