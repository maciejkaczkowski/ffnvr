package profile

import (
	"crypto/rand"
	"fmt"
)

// Dashboards: the saved views of the live view of a user (dashboards.json).
//
//	{"version": 1, "dashboards": [{"id": "5f0c…", "name": "mon01", "size": 9,
//	  "tiles": [{"position": 1, "nvr": "nvr01", "camera": "ch01", "quality": "lq"}]}]}
//
// The cameras are named, not referenced: one that disappears from a recorder leaves
// its tile behind, ignored by the panel.
type Dashboards struct {
	Version    int         `json:"version"`
	Dashboards []Dashboard `json:"dashboards"`
}

// DashboardsFile is the name of the file in a profile.
const DashboardsFile = "dashboards"

// DashboardsVersion is the shape written now.
const DashboardsVersion = 1

// DashboardSizes: the tiles a view may have.
var DashboardSizes = []int{1, 4, 9, 16, 25}

type Dashboard struct {
	ID    string     `json:"id"` // a UUID
	Name  string     `json:"name"`
	Size  int        `json:"size"`
	Tiles []DashTile `json:"tiles"`
}

type DashTile struct {
	Position int    `json:"position"` // from 1
	Nvr      string `json:"nvr"`
	Camera   string `json:"camera"`
	Quality  string `json:"quality,omitempty"` // hq, lq; "": the panel's choice
}

// Find a view by its id (nil: none).
func (d *Dashboards) Find(id string) *Dashboard {
	for i := range d.Dashboards {
		if d.Dashboards[i].ID == id {
			return &d.Dashboards[i]
		}
	}
	return nil
}

// Delete a view; false when there is none.
func (d *Dashboards) Delete(id string) bool {
	for i := range d.Dashboards {
		if d.Dashboards[i].ID == id {
			d.Dashboards = append(d.Dashboards[:i], d.Dashboards[i+1:]...)
			return true
		}
	}
	return false
}

// NewID returns a random UUID (version 4) for a view.
func NewID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
