// Package fsusage tells the size and the free space of the filesystem of a path.
package fsusage

// Usage of a filesystem, in bytes.
type Usage struct {
	Total, Free, Avail uint64 // Avail: free for unprivileged users
}

// Used is Total - Free.
func (u Usage) Used() uint64 { return u.Total - u.Free }

// Percent of the space in use (as df shows it: of what users can have).
func (u Usage) Percent() float64 {
	if u.Used()+u.Avail == 0 {
		return 0
	}
	return 100 * float64(u.Used()) / float64(u.Used()+u.Avail)
}
