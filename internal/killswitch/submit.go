package killswitch

// CanSubmit is the cheap hot-path predicate the order path asks before it begins
// a new-exposure submission (ADR-0004 point 1). allowed=false ⇒ do not even
// start. It reads only the in-process mirror (no store round-trip). For the
// irreversible submit itself, pair Reserve with a final Reconfirm to close the
// TOCTOU window.
func (g *Guard) CanSubmit(symbol string) (allowed bool, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	blocked, reason := g.evaluateLocked(symbol)
	return !blocked, reason
}

// Reserve is CanSubmit that also captures the guard generation for a later
// Reconfirm. The caller uses it at the start of the submit critical section
// (before the prepared-journal append), then calls Reconfirm immediately before
// the irreversible POST. allowed=false ⇒ do not proceed.
func (g *Guard) Reserve(symbol string) (r Reservation, allowed bool, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	blocked, reason := g.evaluateLocked(symbol)
	return Reservation{symbol: symbol, gen: g.gen}, !blocked, reason
}

// Reconfirm is the fail-closed final re-check ADR-0004 point 1 requires: called
// at the last instant before the irreversible submit. It blocks if the guard now
// blocks the reserved symbol OR the generation advanced since Reserve (a global
// trip happened in the window). Either way, allowed=false ⇒ abort the submission.
func (g *Guard) Reconfirm(r Reservation) (allowed bool, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if blocked, reason := g.evaluateLocked(r.symbol); blocked {
		return false, reason
	}
	if r.gen != g.gen {
		return false, reasonGenerationChanged
	}
	return true, ""
}

// NotifyScanComplete opens the startup replay gate: the reconciler (#35) calls it
// once its unresolved-intent scan has finished re-deriving per-symbol blocks
// (ADR-0004 point 3). While a global halt is in effect the gate stays closed
// regardless — CanSubmit blocks on the halt, and only an explicit ClearHalt (or a
// boot-halt affordance cleared by it) can reopen it. Because #36 flips the
// sentinel to running before starting the scan, this open can never precede the
// running-flip (stale-clean window closed — the ordering is #36's, killswitch is
// passive here).
func (g *Guard) NotifyScanComplete() {
	g.mu.Lock()
	g.scanComplete = true
	g.mu.Unlock()
}
