package terminal

import "github.com/hypernewbie/psi/internal/secretmem"

// Release wipes the owned payload and detaches it from this Input. Borrowed
// inputs are left untouched. Copies alias the same payload: after an ownership
// transfer, the previous owner must not release or reuse its copy.
func (in *Input) Release() {
	if in == nil || !in.owned {
		return
	}
	secretmem.Wipe(in.Key[:cap(in.Key)])
	secretmem.Wipe(in.Reply[:cap(in.Reply)])
	in.Key = nil
	in.Reply = nil
	in.owned = false
}
