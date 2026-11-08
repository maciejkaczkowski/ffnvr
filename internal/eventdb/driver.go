package eventdb

// The SQLite driver: modernc.org/sqlite, SQLite's C source translated to Go, so the
// binary stays static (CGO_ENABLED=0).
import _ "modernc.org/sqlite"

const driverName = "sqlite"
