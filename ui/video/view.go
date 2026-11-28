package video

import (
	"github.com/vivaui/viva/ui"
)

// View shows the player's picture, fit into the box the page gives it. The
// picture keeps its aspect: contain fits it inside, cover fills the box and
// clips the overflow, fill stretches it. The view paints nothing until the
// first picture arrives, and keeps the box it is given.
//
// A player has one view: declare it in the page's own paint, not inside a
// Memo, so the view is there when a picture wakes the page. Declare it with
// the other controls, and keep the player in the model.
func View(c ui.C, label string, p *Player) ui.Box {
	if p == nil {
		panic("video: View needs a player")
	}
	p.prepare(c)
	return ui.Control(c, label, &p.handlers).Draw(p.drawFn)
}
