package roc

type SlotRegistry struct {
	basePort    uint32
	activeSlots map[uint32]bool
}

func NewRegistry() *SlotRegistry {
	return &SlotRegistry{
		basePort:    20000,
		activeSlots: make(map[uint32]bool),
	}
}

func (r *SlotRegistry) NextSlot() uint32 {

	var walkingSlot uint32 = 0

	for {
		if !r.activeSlots[walkingSlot] {
			r.activeSlots[walkingSlot] = true
			if walkingSlot > 10 {
				panic("SlotRegistry should always return 1st slot at the moment.")
			}
			return walkingSlot
		} else {
			walkingSlot = walkingSlot + 1
		}
	}
}

func (r *SlotRegistry) FreeSlot(slot uint32) {
	delete(r.activeSlots, slot)
}
